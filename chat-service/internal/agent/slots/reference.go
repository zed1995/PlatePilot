package slots

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/zed1995/platepilot/shared/domain/conversation"
)

// ReferenceKind names how a referring phrase was understood. The set is closed
// because each kind fails differently, and the failure is what a caller needs to
// report: an ordinal out of range means the list is shorter than the user
// thought, while an unmatched modifier means no candidate is what they
// described.
type ReferenceKind string

const (
	// ReferenceOrdinal is "第一家 / 第二家 / 最后一家": a position in the
	// thread's candidate snapshot.
	ReferenceOrdinal ReferenceKind = "ordinal"
	// ReferenceDemonstrative is "这家 / 那家 / 它": whatever the thread already
	// pinned, or the top candidate when nothing is pinned.
	ReferenceDemonstrative ReferenceKind = "demonstrative"
	// ReferenceModifier is "那家安静的 / 布鲁克林那家": a description that has
	// to be matched inside the snapshot.
	ReferenceModifier ReferenceKind = "modifier"
)

// Reference is the outcome of reading a referring phrase against a thread's
// candidate snapshot.
type Reference struct {
	Kind ReferenceKind
	// Position is the 1-based position the phrase named, zero when it named
	// none.
	Position int
	// RestaurantID and Name are set only when the reference actually pinned a
	// restaurant. A zero id means the phrase was understood but the snapshot
	// cannot satisfy it.
	RestaurantID int64
	Name         string
	// Note explains, in the user's language, how the phrase was read. It is what
	// lets the answer open with "按你对第 2 家的追问" instead of silently
	// answering about a restaurant the user never named.
	Note string
}

// ResolveReference reads a referring phrase out of a message.
//
// The second result reports whether a referring phrase was recognised at all.
// A recognised reference with a zero RestaurantID is not a failure: the
// ordinal named a position the snapshot does not have, or the description
// matched nothing in it. The caller reports that rather than guessing, because
// answering about the wrong restaurant is worse than saying the list is
// shorter than the user assumed.
//
// It never calls a model. Ordinals and demonstratives are decidable from the
// message and the snapshot alone, and a rule that decides them the same way
// every time is worth more than a generation that usually agrees — the whole
// point of persisting candidates is that "第二家" is a fact about the thread
// rather than an interpretation of it. When this returns no reference, the
// model still sees the candidate list and can resolve it itself.
func ResolveReference(
	text string, candidates []conversation.Candidate, selected int64,
) (Reference, bool) {
	if len(candidates) == 0 {
		return Reference{}, false
	}
	if ref, ok := resolveOrdinal(text, candidates); ok {
		return ref, true
	}
	if ref, ok := resolveModifier(text, candidates); ok {
		return ref, true
	}
	if ref, ok := resolveDemonstrative(text, candidates, selected); ok {
		return ref, true
	}
	return Reference{}, false
}

// ---------------------------------------------------------------------------
// Ordinals
// ---------------------------------------------------------------------------

// numeralClass matches an Arabic or Chinese count. Only the forms a person
// writes when counting a short list are accepted, which is what keeps "第十一次"
// from being read as a candidate position.
const numeralClass = `[0-9]+|[一二三四五六七八九十两]+`

// classifierClass matches the measure word that makes a count refer to a
// restaurant. It is required rather than optional on purpose: "第二次" is an
// occasion, not a candidate, and accepting a bare "第二" would resolve it to the
// first restaurant in the list.
const classifierClass = `家|间|个|位|名|条|店`

var (
	// ordinalRefPattern matches "第 2 家", "第二家", "第3间".
	ordinalRefPattern = regexp.MustCompile(`第\s*(` + numeralClass + `)\s*(?:` + classifierClass + `)`)
	// lastRefPattern matches "最后一家", "最后面那个". The 一 is optional because
	// the phrase is written both ways and carries no count.
	lastRefPattern = regexp.MustCompile(
		`(?:最后|最后面|末尾|结尾)\s*一?\s*(?:` + classifierClass + `)`)
)

// resolveOrdinal reads "第 N 家" and "最后一家".
func resolveOrdinal(text string, candidates []conversation.Candidate) (Reference, bool) {
	if lastRefPattern.MatchString(text) {
		position := len(candidates)
		return atPosition(candidates, position, "最后一家", "按你说的最后一家"), true
	}
	match := ordinalRefPattern.FindStringSubmatch(text)
	if match == nil {
		return Reference{}, false
	}
	position, ok := parseCount(match[1])
	if !ok {
		return Reference{}, false
	}
	phrase := match[0]
	return atPosition(candidates, position, phrase,
		"按你说的第 "+strconv.Itoa(position)+" 家"), true
}

// atPosition turns a 1-based position into a reference.
func atPosition(
	candidates []conversation.Candidate, position int, phrase, note string,
) Reference {
	ref := Reference{Kind: ReferenceOrdinal, Position: position, Note: note}
	if position < 1 || position > len(candidates) {
		// The user referred to something real; the list is just shorter than
		// they thought. Saying so is the answer, not an error.
		ref.Note = phrase + "超出了本轮候选范围（当前只有 " +
			strconv.Itoa(len(candidates)) + " 家）"
		return ref
	}
	candidate := candidates[position-1]
	ref.RestaurantID = candidate.RestaurantID
	ref.Name = candidate.Name
	ref.Note = note + "（" + describeCandidate(candidate) + "）"
	return ref
}

// ---------------------------------------------------------------------------
// Modifiers
// ---------------------------------------------------------------------------

// modifierBody matches the description inside a possessive reference. It is
// either a run with no spaces at all, or one with internal spaces when it
// starts with a Latin letter.
//
// The second form exists because the corpus's names are English: "那家 Joe's
// Shanghai 的" is one description, and a class that rejected whitespace
// outright would see only the marker and lose the name the user actually
// wrote. It is deliberately anchored to a leading Latin letter so that an
// ordinary Chinese clause carrying a stray space ("那家不错 我喜欢的") is not
// swallowed whole — that phrase is not a description of anything.
const modifierBody = `[^\s，。！？；,.;的]{1,12}` +
	`|[A-Za-z][A-Za-z0-9'&.\-]*(?:\s+[A-Za-z0-9'&.\-]+){0,3}`

var (
	// modifierAfterPattern matches "那家安静的" and "那家 Shanghai 的" — the
	// classifier first, the description before the possessive 的. The trailing
	// \s* is what lets a Latin description keep the space that precedes 的.
	modifierAfterPattern = regexp.MustCompile(
		`(?:这|那)\s*(?:` + classifierClass + `)\s*(` + modifierBody + `)\s*的`)
	// modifierBeforePattern matches "布鲁克林那家" — the description first. The
	// two-rune minimum keeps a leading preposition ("在哪家") from being read as
	// a description.
	modifierBeforePattern = regexp.MustCompile(
		`([^，。！？；,.;\s的]{2,12})\s*(?:这|那)\s*(?:` + classifierClass + `)`)
)

// resolveModifier matches a referring description inside the snapshot.
//
// A description arrives in two strengths, and they are not equally trustworthy.
// The possessive form ("那家安静的") is explicit: the trailing 的 marks what
// precedes it as a description, so it is honoured even when it matches nothing,
// and the caller reports the mismatch. A bare prefix ("布鲁克林那家") is weaker —
// it is often just the surrounding clause, as in "我第一次去这家店" — so it only
// counts when it actually names exactly one candidate, and otherwise the phrase
// falls through to the demonstrative it contains.
//
// A description that fits more than one candidate is reported rather than
// narrowed arbitrarily: two candidates that both answer to "安静的" are exactly
// the case where the user has to say which, and picking one would turn their
// question into a different question.
func resolveModifier(text string, candidates []conversation.Candidate) (Reference, bool) {
	explicit := false
	modifier := ""
	if match := modifierAfterPattern.FindStringSubmatch(text); match != nil {
		modifier, explicit = match[1], true
	} else if match := modifierBeforePattern.FindStringSubmatch(text); match != nil {
		modifier = match[1]
	} else {
		return Reference{}, false
	}
	modifier = strings.TrimSpace(modifier)
	if modifier == "" {
		return Reference{}, false
	}

	var matched []conversation.Candidate
	for _, candidate := range candidates {
		if candidateMatchesModifier(candidate, modifier) {
			matched = append(matched, candidate)
		}
	}
	if len(matched) != 1 && !explicit {
		return Reference{}, false
	}
	ref := Reference{Kind: ReferenceModifier, Note: "按你说的「" + modifier + "」"}
	switch len(matched) {
	case 1:
		ref.RestaurantID = matched[0].RestaurantID
		ref.Name = matched[0].Name
		ref.Position = matched[0].Position
		ref.Note += "（" + describeCandidate(matched[0]) + "）"
	case 0:
		ref.Note = "你说的「" + modifier + "」在本轮候选里没有对应"
	default:
		ref.Note = "你说的「" + modifier + "」在本轮候选里对应 " +
			strconv.Itoa(len(matched)) + " 家，无法确定是哪一家"
	}
	return ref, true
}

// candidateMatchesModifier reports whether one candidate answers to a
// description.
//
// The search runs over the name and the recorded match reasons, which is what
// the snapshot carries. Reasons quote the condition that placed the candidate,
// so a description that repeats a filter ("布鲁克林") is found there — this is
// the reason the reasons are persisted alongside the position at all.
func candidateMatchesModifier(candidate conversation.Candidate, modifier string) bool {
	needles := modifierTokens(modifier)
	for _, text := range append([]string{candidate.Name}, candidate.Reasons...) {
		haystack := strings.ToLower(text)
		for _, needle := range needles {
			if needle != "" && strings.Contains(haystack, needle) {
				return true
			}
		}
	}
	return false
}

// modifierTokens expands a description into the spellings a candidate's name or
// recorded reasons might use.
//
// The expansion is what lets "布鲁克林那家" find a candidate whose reason reads
// "匹配行政区：brooklyn". The snapshot stores the corpus's spelling and the user
// wrote theirs; the alias tables in this package are the only place that knows
// the two are the same word, so the resolver asks them rather than comparing
// strings and hoping.
func modifierTokens(modifier string) []string {
	tokens := []string{strings.ToLower(strings.TrimSpace(modifier))}
	if borough, ok := canonicalBorough(modifier); ok {
		tokens = append(tokens, strings.ToLower(borough))
	}
	if neighborhood, ok := canonicalNeighborhood(modifier); ok {
		tokens = append(tokens, strings.ToLower(neighborhood))
	}
	for _, cuisine := range canonicalCuisines(modifier) {
		tokens = append(tokens, strings.ToLower(cuisine))
	}
	return tokens
}

// ---------------------------------------------------------------------------
// Demonstratives
// ---------------------------------------------------------------------------

// demonstrativeMarkers are the phrases that mean "the one we are already
// talking about".
//
// English markers are matched on word boundaries so that "it" cannot fire
// inside "kitchen" or "with".
var demonstrativeMarkers = []string{
	"这家", "那家", "这间", "那间", "这个", "那个", "这座", "这座店",
	"这家店", "那家店", "该家", "该店", "此家", "此店",
	"它", "这里", "那里", "这儿", "那儿",
}

var demonstrativeEnglish = regexp.MustCompile(`(?i)\b(this one|that one|that place|this place|the one)\b`)

// resolveDemonstrative reads "这家 / 那家 / 它".
//
// It prefers whatever the thread already pinned, because that is what "它"
// means in a conversation that has been talking about one restaurant. With
// nothing pinned, the top candidate is what the user is looking at — the answer
// said "为你找到这几家" and the first is the recommendation, not an arbitrary
// pick.
func resolveDemonstrative(
	text string, candidates []conversation.Candidate, selected int64,
) (Reference, bool) {
	marker := ""
	for _, candidate := range demonstrativeMarkers {
		if strings.Contains(text, candidate) {
			if len(candidate) > len(marker) {
				marker = candidate
			}
		}
	}
	if marker == "" {
		if match := demonstrativeEnglish.FindString(text); match != "" {
			marker = match
		}
	}
	if marker == "" {
		return Reference{}, false
	}

	ref := Reference{Kind: ReferenceDemonstrative, Note: "按你说的「" + marker + "」"}
	if selected != 0 {
		ref.RestaurantID = selected
		if candidate, ok := findByID(candidates, selected); ok {
			ref.Position = candidate.Position
			ref.Name = candidate.Name
			ref.Note += "（本线程已锁定的 " + describeCandidate(candidate) + "）"
			return ref, true
		}
		// Pinned to a restaurant the snapshot no longer lists. The id is still
		// the right answer — the snapshot is a window, not the address book —
		// so it is kept and the name is simply unknown.
		ref.Note += "（本线程已锁定的餐厅）"
		return ref, true
	}

	top := candidates[0]
	ref.RestaurantID = top.RestaurantID
	ref.Position = top.Position
	ref.Name = top.Name
	ref.Note += "（本轮推荐第一家 " + describeCandidate(top) + "）"
	return ref, true
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// describeCandidate renders the few facts a note can use to name a candidate.
func describeCandidate(candidate conversation.Candidate) string {
	if strings.TrimSpace(candidate.Name) == "" {
		return "id " + strconv.FormatInt(candidate.RestaurantID, 10)
	}
	return candidate.Name
}

func findByID(candidates []conversation.Candidate, id int64) (conversation.Candidate, bool) {
	for _, candidate := range candidates {
		if candidate.RestaurantID == id {
			return candidate, true
		}
	}
	return conversation.Candidate{}, false
}

// chineseDigits maps the numerals a person writes when counting a short list.
// 两 is included because "两家" is how the quantity is said, not written.
var chineseDigits = map[rune]int{
	'零': 0, '〇': 0,
	'一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
	'五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
}

// parseCount reads an Arabic integer or a Chinese numeral up to 99.
//
// The ceiling is deliberate. Candidate snapshots are short, and a parser that
// accepted "一百" would only ever be used to reject it — the honest answer for a
// position nobody has is "the list is shorter than you thought", which needs no
// large-number support to say.
func parseCount(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if value, err := strconv.Atoi(raw); err == nil {
		return value, value >= 1
	}
	parts := strings.SplitN(raw, "十", 2)
	switch len(parts) {
	case 1:
		digit, ok := chineseDigit(parts[0])
		return digit, ok && digit >= 1
	case 2:
		tens := 1
		if parts[0] != "" {
			digit, ok := chineseDigit(parts[0])
			if !ok || digit < 1 {
				return 0, false
			}
			tens = digit
		}
		ones := 0
		if parts[1] != "" {
			digit, ok := chineseDigit(parts[1])
			if !ok {
				return 0, false
			}
			ones = digit
		}
		return tens*10 + ones, true
	}
	return 0, false
}

// chineseDigit reads a single Chinese numeral rune.
func chineseDigit(raw string) (int, bool) {
	runes := []rune(raw)
	if len(runes) != 1 {
		return 0, false
	}
	digit, ok := chineseDigits[runes[0]]
	return digit, ok
}
