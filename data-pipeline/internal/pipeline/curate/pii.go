package curate

import "regexp"

// Patterns are intentionally conservative; they mask obvious PII without
// mangling ordinary prose.
var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	phonePattern = regexp.MustCompile(`(?:\+?1[\s.\-]?)?(?:\(\d{3}\)|\d{3})[\s.\-]?\d{3}[\s.\-]?\d{4}\b`)
)

// ScrubPII masks email addresses and phone numbers so they never reach the
// curated collections or RAG evidence.
func ScrubPII(text string) string {
	text = emailPattern.ReplaceAllString(text, "[redacted-email]")
	text = phonePattern.ReplaceAllString(text, "[redacted-phone]")
	return stripControlRunes(text)
}

// controlRunes matches C0 and C7F control characters, plus the Unicode line and
// paragraph separators that carry no meaning in review prose.
//
// NUL is the important one: PostgreSQL rejects it outright (SQLSTATE 22021),
// where a document store accepts it silently. Removing it here rather than at
// the storage boundary keeps the rule with the other text curation and applies
// identically to every backend.
var controlRunes = regexp.MustCompile(`[\x00-\x{08}\x{0b}\x{0c}\x{0e}-\x{1f}\x{7f}\x{2028}\x{2029}]`)

// stripControlRunes removes control characters that cannot appear in curated
// text. Tab, newline, and carriage return are preserved because they are
// legitimate formatting in a review.
func stripControlRunes(text string) string {
	return controlRunes.ReplaceAllString(text, "")
}
