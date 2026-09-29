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
	return text
}
