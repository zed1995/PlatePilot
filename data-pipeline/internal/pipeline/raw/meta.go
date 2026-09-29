package raw

import (
	"encoding/json"
	"fmt"
)

// Meta is one Google Local Meta JSONL record.
type Meta struct {
	Name            string     `json:"name"`
	Address         *string    `json:"address"`
	GmapID          string     `json:"gmap_id"`
	Description     *string    `json:"description"`
	Latitude        *float64   `json:"latitude"`
	Longitude       *float64   `json:"longitude"`
	Category        []string   `json:"category"`
	AvgRating       *float64   `json:"avg_rating"`
	NumOfReviews    *int       `json:"num_of_reviews"`
	Price           *string    `json:"price"`
	Hours           [][]string `json:"hours"`
	MISC            MISC       `json:"MISC"`
	State           *string    `json:"state"`
	RelativeResults []string   `json:"relative_results"`
	URL             string     `json:"url"`
}

// MISC is the raw attribute object: topic -> list of labels. The source is
// loose (values are usually arrays of strings, but a bare string occurs too),
// so MISC accepts both and ignores shapes it does not understand.
type MISC map[string][]string

// UnmarshalJSON decodes the MISC object leniently.
func (m *MISC) UnmarshalJSON(data []byte) error {
	trimmed := string(data)
	if trimmed == "" || trimmed == "null" {
		*m = nil
		return nil
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("MISC: %w", err)
	}
	out := make(MISC, len(fields))
	for topic, value := range fields {
		out[topic] = decodeStringList(value)
	}
	*m = out
	return nil
}

// decodeStringList accepts a string, an array of strings, or an array of mixed
// values (from which only strings are kept). Anything else yields no labels:
// an odd attribute must never reject an otherwise valid restaurant.
func decodeStringList(data json.RawMessage) []string {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		return list
	}
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		return []string{single}
	}
	var mixed []any
	if err := json.Unmarshal(data, &mixed); err == nil {
		out := make([]string, 0, len(mixed))
		for _, v := range mixed {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
