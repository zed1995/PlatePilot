package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

type echoQuery struct {
	Name string `query:"name"`
}

func (q echoQuery) Validate() error {
	if strings.TrimSpace(q.Name) == "" {
		return errors.New("name is required")
	}
	return nil
}

type submitBody struct {
	Name string `json:"name"`
}

func (b submitBody) Validate() error {
	if strings.TrimSpace(b.Name) == "" {
		return errors.New("name is required")
	}
	return nil
}

func registerEchoRoutes(h *server.Hertz) {
	h.GET("/echo", func(ctx context.Context, c *app.RequestContext) {
		var req echoQuery
		if err := BindAndValidate(c, &req); err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		c.JSON(http.StatusOK, map[string]string{"name": req.Name})
	})
	h.POST("/submit", func(ctx context.Context, c *app.RequestContext) {
		var body submitBody
		if err := BindAndValidate(c, &body); err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		c.JSON(http.StatusOK, map[string]string{"name": body.Name})
	})
}

func TestBindAndValidateAcceptsValidRequest(t *testing.T) {
	h := testRouter()
	registerEchoRoutes(h)

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/echo?name=Joe", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
}

func TestBindAndValidateReportsValidationFailure(t *testing.T) {
	h := testRouter()
	registerEchoRoutes(h)

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/echo?name=", nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", w.Code, w.Body.String())
	}
	resp := decodeError(t, w.Body.Bytes())
	if resp.Error.Code != "validation_failed" {
		t.Fatalf("error code = %q, want validation_failed", resp.Error.Code)
	}
	if resp.Error.RequestID == "" {
		t.Fatal("validation error must carry the request id")
	}
}

func TestBindAndValidateReportsMalformedBody(t *testing.T) {
	h := testRouter()
	registerEchoRoutes(h)

	payload := "{not json"
	w := ut.PerformRequest(h.Engine, http.MethodPost, "/submit",
		&ut.Body{Body: strings.NewReader(payload), Len: len(payload)},
		ut.Header{Key: "Content-Type", Value: "application/json"},
	)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	resp := decodeError(t, w.Body.Bytes())
	if resp.Error.Code != "invalid_argument" {
		t.Fatalf("error code = %q, want invalid_argument", resp.Error.Code)
	}
}
