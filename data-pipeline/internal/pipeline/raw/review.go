package raw

import "encoding/json"

// Review is one Google Local Review JSONL record.
//
// UserID, Name, and Pics are raw identity fields: they may be read for the
// deterministic review ID but must never be persisted to the curated layer.
type Review struct {
	UserID string          `json:"user_id"`
	Name   string          `json:"name"`
	Time   int64           `json:"time"`
	Rating int             `json:"rating"`
	Text   *string         `json:"text"`
	Pics   []string        `json:"pics"`
	Resp   json.RawMessage `json:"resp"`
	GmapID string          `json:"gmap_id"`
}
