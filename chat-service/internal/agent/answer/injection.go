package answer

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/search"
)

// Injection defence for the untrusted text that reaches the model's context.
//
// Three channels carry text this service did not write: review and document
// bodies, candidate fields, and long-term memories (which are themselves
// model-derived from a user message). None of them can be sanitised without
// breaking the "citations are never truncated" rule — rewriting a review would
// make the quoted text differ from the stored one — so the defence is
// detection, declaration, and a value domain rather than escaping:
//
//   - Detection: a block whose text contains a structural delimiter or an
//     instruction-override phrase is marked `suspicious="true"` in the prompt
//     and the turn records a warning, because a silent suppression would be
//     worse than a visible one.
//   - Declaration: the system instruction states that everything inside the
//     tags is material and never an instruction.
//   - Value domain: the write tools accept only ids the turn actually saw, so
//     an injected model cannot request a booking it invented.
//
// The structurale defences that really bound the worst case — the confirmation
// gate, citation validation, and the tool-argument JSON schema — are unchanged
// and live elsewhere; this file is the depth in front of them.

// tagDelimiterPattern matches a literal that could open or close one of the
// prompt's structural tags. Whitespace and case variants are covered because
// `</ evidence >` closes a tag on the wire just as `</evidence>` does.
var tagDelimiterPattern = regexp.MustCompile(
	`(?i)<\s*/?\s*(evidence|filters|candidates|soft_conditions|gaps|thread_context|recent_turns|memory|evidence_adequacy)\b`)

// instructionOverridePattern matches the phrases whose only purpose is to
// override the system instruction. It is deliberately narrow: a broad match
// would flag ordinary review text ("服务很到位，我照着菜单点的") and train the
// reader to ignore the warning.
var instructionOverridePattern = regexp.MustCompile(
	`(?i)(忽略[^\n]{0,6}指令|无视[^\n]{0,6}指令|不要(再)?(遵守|听取)[^\n]{0,6}(指令|规则)|` +
		`ignore\s+(all\s+)?(the\s+)?(previous|prior|above|preceding|earlier)\s+(instruction|prompt|rule)s?|` +
		`disregard\s+(all\s+)?(the\s+)?(previous|prior|above|preceding)\s+(instruction|prompt|rule)s?|` +
		`forget\s+(all\s+)?(the\s+)?(previous|prior|above)\s+(instruction|prompt|rule)s?)`)

// detectInjectionRisk returns a description for every injection signal found in
// content. An empty result means nothing suspicious was seen.
func detectInjectionRisk(content string) []string {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	var found []string
	for _, match := range tagDelimiterPattern.FindAllString(content, -1) {
		found = append(found, fmt.Sprintf("标签字面量 %q", strings.TrimSpace(match)))
	}
	for _, match := range instructionOverridePattern.FindAllString(content, -1) {
		found = append(found, fmt.Sprintf("指令覆盖措辞 %q", strings.TrimSpace(match)))
	}
	return found
}

// injectionWarnings renders the detected signals as user-visible warnings, one
// per source, so the turn records where the risk was seen.
func injectionWarnings(source string, signals []string) []string {
	if len(signals) == 0 {
		return nil
	}
	out := make([]string, 0, len(signals))
	for _, signal := range signals {
		out = append(out, fmt.Sprintf("检索资料中检出疑似注入（%s）：%s", source, signal))
	}
	return out
}

// candidateText joins every free-text field of a candidate, which is the whole
// untrusted surface of a candidate row.
func candidateText(candidate search.RestaurantCandidate) string {
	parts := []string{candidate.Name, candidate.Address, candidate.Borough}
	parts = append(parts, candidate.Cuisines...)
	return strings.Join(parts, " ")
}

// ScanUntrusted reports the injection signals in the untrusted text a turn is
// about to put in the model's context. It is called by the answer node so the
// warning reaches the run's `warnings`, which is the user-visible record that
// something in the retrieved material looked like an instruction.
func ScanUntrusted(items []evidence.Evidence, candidates []search.RestaurantCandidate) []string {
	var out []string
	for _, item := range items {
		out = append(out, injectionWarnings(
			fmt.Sprintf("证据 %d", item.EvidenceID), detectInjectionRisk(item.Content))...)
	}
	for _, candidate := range candidates {
		out = append(out, injectionWarnings(
			fmt.Sprintf("候选 %d", candidate.RestaurantID),
			detectInjectionRisk(candidateText(candidate)))...)
	}
	return out
}
