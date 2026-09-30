package postgres

import (
	"strconv"
	"strings"

	"github.com/zed/platepilot/shared/domain/evidence"
)

// optionalString returns nil for an empty value so the column stores SQL NULL
// rather than an empty string. An empty title is "unknown"; "" is not the same
// statement, and a default-"" column would erase that distinction.
func optionalString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// boroughOf reads the denormalised borough. It lives in metadata because that
// is where the builders put it, and the column exists only to make the partial
// index predicate a local test.
func boroughOf(doc evidence.KnowledgeDocument) string {
	if doc.Metadata == nil {
		return ""
	}
	value, _ := doc.Metadata["borough"].(string)
	return value
}

// orEmptyMap returns an empty map for nil metadata so json.Marshal writes {}
// rather than null, matching the column default.
func orEmptyMap(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	return metadata
}

// vectorLiteral renders a vector as the pgvector text input form.
//
// float32 is formatted through strconv with full precision rather than being
// cast from float64, so the stored value is exactly what the model produced and
// a later recomputation agrees with it.
func vectorLiteral(vec []float32) string {
	var b strings.Builder
	// "[1,0.5]" is 2 + 3 + 10*n characters; sizing avoids a mid-loop grow for
	// the 1024-dimension vectors this pipeline uses.
	b.Grow(2 + len(vec)*11)
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// sourceIDDelimiter joins the source record ids of one document so they can
// travel through a flat text[] parameter. It is the ASCII unit separator, which
// cannot occur in an identifier from the source dataset; an empty string yields
// an empty array rather than a one-element array holding "".
const sourceIDDelimiter = "\x1f"
