package admin

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
)

// idPosition is the encoded keyset position of a listing ordered by one
// descending id.
type idPosition struct {
	ID int64 `json:"id"`
}

// timedPosition is the encoded keyset position of a listing ordered by a
// descending time followed by a descending id.
type timedPosition struct {
	Time time.Time `json:"t"`
	ID   int64     `json:"id"`
}

// encode renders payload as an opaque, URL-safe string. Callers and clients
// must not parse it; keeping the shape internal lets the ordering change
// without a client-facing break.
func encode(payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", errs.Wrap(errs.CodeInternal, "encode cursor", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// decode parses an opaque cursor into target. An empty cursor is not an error;
// it represents the first page.
func decode(cursor string, target any) error {
	if cursor == "" {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return errs.New(errs.CodeInvalidArgument, "malformed cursor")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return errs.New(errs.CodeInvalidArgument, "malformed cursor")
	}
	return nil
}

// encodeID renders an id-only position.
func encodeID(id int64) (string, error) {
	return encode(idPosition{ID: id})
}

// decodeID parses an id-only cursor, returning zero for an empty cursor.
func decodeID(cursor string) (int64, error) {
	var position idPosition
	if err := decode(cursor, &position); err != nil {
		return 0, err
	}
	return position.ID, nil
}

// encodeTimed renders a time-plus-id position.
func encodeTimed(at time.Time, id int64) (string, error) {
	return encode(timedPosition{Time: at.UTC(), ID: id})
}

// decodeTimed parses a time-plus-id cursor, returning the zero time for an
// empty cursor.
func decodeTimed(cursor string) (time.Time, int64, error) {
	var position timedPosition
	if err := decode(cursor, &position); err != nil {
		return time.Time{}, 0, err
	}
	return position.Time, position.ID, nil
}
