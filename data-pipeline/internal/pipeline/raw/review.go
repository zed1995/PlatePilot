package raw

import "encoding/json"

// Review is one Google Local Review JSONL record.
//
// UserID, Name, and Pics are raw identity fields: they may be read for the
// deterministic review ID but must never be persisted to the curated layer.
//
// Pics is kept as raw JSON on purpose. The source spells it as a nested array
// of objects, [{"url": ["https://..."]}], not as an array of strings, so a
// typed []string field made every review carrying a photo fail to decode.
// Because the field is read for nothing in the curated layer, keeping it opaque
// removes a whole class of silent data loss for free.
type Review struct {
	UserID string          `json:"user_id"`
	Name   string          `json:"name"`
	Time   int64           `json:"time"`
	Rating int             `json:"rating"`
	Text   *string         `json:"text"`
	Pics   json.RawMessage `json:"pics,omitempty"`
	Resp   json.RawMessage `json:"resp,omitempty"`
	GmapID string          `json:"gmap_id"`
}
