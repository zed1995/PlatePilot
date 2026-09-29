package curate

import "strings"

// PriceLevel maps a raw price string to a 1-4 level. Anything that is not a run
// of one to four '$' characters (foreign currency, ranges, free text) yields
// nil so that "unknown" is never confused with level 0.
func PriceLevel(raw *string) *int {
	if raw == nil {
		return nil
	}
	value := strings.TrimSpace(*raw)
	if value == "" || len(value) > 4 {
		return nil
	}
	for _, r := range value {
		if r != '$' {
			return nil
		}
	}
	level := len(value)
	return &level
}
