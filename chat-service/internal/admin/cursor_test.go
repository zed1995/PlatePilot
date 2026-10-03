package admin

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
)

func TestIDCursorRoundTrip(t *testing.T) {
	cursor, err := encodeID(1234)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decodeID(cursor)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != 1234 {
		t.Errorf("got %d, want 1234", got)
	}
}

func TestTimedCursorRoundTrip(t *testing.T) {
	at := time.Date(2021, 9, 1, 12, 30, 0, 0, time.UTC)
	cursor, err := encodeTimed(at, 42)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	gotAt, gotID, err := decodeTimed(cursor)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !gotAt.Equal(at) {
		t.Errorf("time got %v, want %v", gotAt, at)
	}
	if gotID != 42 {
		t.Errorf("id got %d, want 42", gotID)
	}
}

func TestEmptyCursorIsFirstPage(t *testing.T) {
	id, err := decodeID("")
	if err != nil || id != 0 {
		t.Errorf("empty id cursor: got (%d, %v)", id, err)
	}
	at, rid, err := decodeTimed("")
	if err != nil || rid != 0 || !at.IsZero() {
		t.Errorf("empty timed cursor: got (%v, %d, %v)", at, rid, err)
	}
}

func TestMalformedCursorRejected(t *testing.T) {
	if _, err := decodeID("!!!not-base64!!!"); err == nil {
		t.Fatal("expected error for invalid base64")
	} else if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Errorf("got code %s, want invalid_argument", errs.CodeOf(err))
	}

	// Valid encoding but an unusable payload.
	encoded := base64.RawURLEncoding.EncodeToString([]byte("garbage-payload"))
	if _, err := decodeID(encoded); err == nil {
		t.Fatal("expected error for invalid payload")
	} else if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Errorf("got code %s, want invalid_argument", errs.CodeOf(err))
	}
}
