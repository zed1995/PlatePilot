package middleware

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/network"

	"github.com/zed/platepilot/chat-service/internal/transport/httperr"
)

// fakeAddr is a net.Addr for the decision tests.
type fakeAddr struct{ value string }

func (f fakeAddr) Network() string { return "tcp" }
func (f fakeAddr) String() string  { return f.value }

// fakeConn carries only the peer address; every other network.Conn method is
// left unimplemented because the guard never calls it.
type fakeConn struct {
	network.Conn
	addr net.Addr
}

func (f fakeConn) RemoteAddr() net.Addr { return f.addr }

func TestIsLoopbackRemote(t *testing.T) {
	cases := []struct {
		name string
		addr net.Addr
		want bool
	}{
		{"ipv4 loopback with port", fakeAddr{"127.0.0.1:8080"}, true},
		{"ipv4 loopback without port", fakeAddr{"127.0.0.1"}, true},
		{"ipv6 loopback", fakeAddr{"[::1]:443"}, true},
		{"private lan", fakeAddr{"192.168.1.5:3000"}, false},
		{"public address", fakeAddr{"8.8.8.8:53"}, false},
		{"unspecified address", fakeAddr{"0.0.0.0:0"}, false},
		{"unparseable value", fakeAddr{"not-an-address"}, false},
		{"nil address", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLoopbackRemote(tc.addr); got != tc.want {
				t.Errorf("isLoopbackRemote(%v) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

// guardedEngine builds an engine whose probe route carries the guard and a
// handler that reports whether it actually ran.
func guardedEngine() *server.Hertz {
	h := server.Default()
	h.GET("/probe", LoopbackOnly(), func(_ context.Context, c *app.RequestContext) {
		c.String(http.StatusOK, "ok")
	})
	return h
}

func TestLoopbackOnlyRejectsNonLoopbackPeer(t *testing.T) {
	h := guardedEngine()

	// The unit-test context carries no connection, so its peer address is
	// the unspecified address: the non-loopback path.
	w := ut.PerformRequest(h.Engine, http.MethodGet, "/probe", nil)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	var envelope httperr.Response
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	if envelope.Error.Code != "unauthorized" {
		t.Errorf("error code = %q, want unauthorized", envelope.Error.Code)
	}
	if w.Body.String() == "ok" {
		t.Error("the guarded handler must not run for a non-loopback peer")
	}
}

func TestLoopbackOnlyAllowsLoopbackPeer(t *testing.T) {
	h := guardedEngine()

	var c app.RequestContext
	c.SetConn(fakeConn{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 54321}})
	c.Request.SetRequestURI("/probe")
	c.Request.Header.SetMethod(http.MethodGet)
	h.Engine.ServeHTTP(context.Background(), &c)

	if c.Response.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)",
			c.Response.StatusCode(), c.Response.Body())
	}
	if string(c.Response.Body()) != "ok" {
		t.Errorf("guarded handler must run for a loopback peer, body = %q", c.Response.Body())
	}
}

// TestLoopbackOnlyIgnoresForwardedHeader ensures a remote caller cannot claim
// a local origin by setting a forwarding header: the guard reads the peer
// address only.
func TestLoopbackOnlyIgnoresForwardedHeader(t *testing.T) {
	h := guardedEngine()

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/probe", nil,
		ut.Header{Key: "X-Forwarded-For", Value: "127.0.0.1"},
	)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; forwarded header must not be trusted", w.Code)
	}
}
