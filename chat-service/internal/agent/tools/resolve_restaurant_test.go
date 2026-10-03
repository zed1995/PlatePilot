package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/search"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
)

// ---- fake resolver --------------------------------------------------------

type fakeResolver struct {
	rows      []search.RestaurantCandidate
	err       error
	called    int
	lastText  string
	lastLimit int
}

func (f *fakeResolver) MatchByText(
	_ context.Context, text string, limit int,
) ([]search.RestaurantCandidate, error) {
	f.called++
	f.lastText = text
	f.lastLimit = limit
	return f.rows, f.err
}

func candidate(id int64, name, borough, address string, score float64) search.RestaurantCandidate {
	return search.RestaurantCandidate{
		RestaurantID: id,
		Name:         name,
		Borough:      borough,
		Address:      address,
		Cuisines:     []string{"pizza"},
		Rating:       ptrFloat(4.4),
		Score:        score,
	}
}

// resolveResult decodes the tool's structured payload.
func resolveResult(t *testing.T, out domaintool.ToolResult) ResolveResult {
	t.Helper()
	var result ResolveResult
	if err := json.Unmarshal(out.Data, &result); err != nil {
		t.Fatalf("decode resolve result: %v (raw %s)", err, out.Data)
	}
	return result
}

// ---- the four outcomes ----------------------------------------------------

// The user said a name and the corpus has exactly that name: one restaurant, no
// question to ask.
func TestResolveRestaurantExactMatchIsResolved(t *testing.T) {
	repo := &fakeResolver{rows: []search.RestaurantCandidate{
		candidate(42, "Katz's Delicatessen", "manhattan", "205 E Houston St", 0.98),
	}}

	out := invoke(t, ResolveRestaurantEntry(repo, ResolveConfig{}).Handler,
		`{"name":"Katz's Delicatessen"}`)
	if out.Status != domaintool.ToolStatusOK {
		t.Fatalf("status = %s err = %+v", out.Status, out.Error)
	}
	result := resolveResult(t, out)
	if result.Status != ResolveResolved {
		t.Fatalf("status = %q, want resolved (candidates %+v)", result.Status, result.Candidates)
	}
	if result.Restaurant == nil || result.Restaurant.RestaurantID != 42 {
		t.Fatalf("restaurant = %+v, want id 42", result.Restaurant)
	}
	if result.Similarity != 0.98 {
		t.Fatalf("similarity = %v, want 0.98", result.Similarity)
	}
	if !strings.Contains(out.Content, "get_restaurant_evidence") {
		t.Fatalf("a resolved id is a scope, not an answer; the tool must say so:\n%s", out.Content)
	}
}

// A typo still resolves, and the similarity travels with it so the answer can
// say how sure the match was rather than presenting it as exact.
func TestResolveRestaurantFuzzyMatchResolvesWithItsSimilarity(t *testing.T) {
	repo := &fakeResolver{rows: []search.RestaurantCandidate{
		candidate(43, "Katz's Delicatessen", "manhattan", "205 E Houston St", 0.71),
	}}

	result := resolveResult(t, invoke(t, ResolveRestaurantEntry(repo, ResolveConfig{}).Handler,
		`{"name":"katz deli"}`))
	if result.Status != ResolveResolved {
		t.Fatalf("status = %q, want resolved", result.Status)
	}
	if result.Similarity != 0.71 {
		t.Fatalf("similarity = %v, want the match's own score", result.Similarity)
	}
	if !strings.Contains(invoke(t, ResolveRestaurantEntry(repo, ResolveConfig{}).Handler,
		`{"name":"katz deli"}`).Content, "0.710") {
		t.Fatal("the tool summary must show the similarity the id was accepted at")
	}
}

// Two branches of the same name is a normal state of the corpus, not a failure:
// the tool refuses to pick and asks.
func TestResolveRestaurantTiesAreAmbiguous(t *testing.T) {
	repo := &fakeResolver{rows: []search.RestaurantCandidate{
		candidate(7, "Joe's Pizza", "manhattan", "7 Carmine St", 0.92),
		candidate(8, "Joe's Pizza", "brooklyn", "235 Bedford Ave", 0.90),
	}}

	out := invoke(t, ResolveRestaurantEntry(repo, ResolveConfig{}).Handler, `{"name":"Joe's Pizza"}`)
	result := resolveResult(t, out)
	if result.Status != ResolveAmbiguous {
		t.Fatalf("status = %q, want ambiguous", result.Status)
	}
	if result.Restaurant != nil {
		t.Fatalf("an ambiguous match must not pin a restaurant: %+v", result.Restaurant)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want both", result.Candidates)
	}
	if !strings.Contains(out.Content, "必须先请用户选择") {
		t.Fatalf("the summary must forbid guessing:\n%s", out.Content)
	}
	if !strings.Contains(out.Content, "不要猜测") || !strings.Contains(out.Content, "不要先取证据") {
		t.Fatalf("the summary must also forbid reading evidence before the user chooses:\n%s", out.Content)
	}
}

// A clear winner with a rejected near-miss behind it is resolved: the ambiguity
// test runs on the matches that survived the floor, not on everything returned.
func TestResolveRestaurantClearWinnerIsNotAmbiguous(t *testing.T) {
	repo := &fakeResolver{rows: []search.RestaurantCandidate{
		candidate(7, "Joe's Pizza", "manhattan", "7 Carmine St", 0.95),
		candidate(8, "Joe's Shanghai", "manhattan", "9 Pell St", 0.60),
	}}

	result := resolveResult(t, invoke(t, ResolveRestaurantEntry(repo, ResolveConfig{}).Handler,
		`{"name":"Joe's Pizza"}`))
	if result.Status != ResolveResolved {
		t.Fatalf("status = %q, want resolved", result.Status)
	}
	if result.Restaurant == nil || result.Restaurant.RestaurantID != 7 {
		t.Fatalf("restaurant = %+v, want id 7", result.Restaurant)
	}
}

// Nothing close enough is a normal outcome, and the summary has to say so: the
// correct next move is a search by need, not a bad guess.
func TestResolveRestaurantNothingAboveTheFloorIsNotFound(t *testing.T) {
	repo := &fakeResolver{rows: []search.RestaurantCandidate{
		candidate(9, "Somewhere Else Entirely", "queens", "1 Main St", 0.31),
	}}

	out := invoke(t, ResolveRestaurantEntry(repo, ResolveConfig{}).Handler,
		`{"name":"Joe's Pizza"}`)
	result := resolveResult(t, out)
	if result.Status != ResolveNotFound {
		t.Fatalf("status = %q, want not_found", result.Status)
	}
	if result.Restaurant != nil {
		t.Fatalf("not_found must not pin a restaurant: %+v", result.Restaurant)
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("a match below the floor is not a candidate: %+v", result.Candidates)
	}
	if result.Similarity != 0 {
		t.Fatalf("similarity = %v, want 0 when nothing matched", result.Similarity)
	}
	if !strings.Contains(out.Content, "search_restaurants") {
		t.Fatalf("not_found must point at the fallback:\n%s", out.Content)
	}
	if !strings.Contains(out.Content, "这是正常结果") {
		t.Fatalf("not_found must not read as an error:\n%s", out.Content)
	}
}

// ---- hints ----------------------------------------------------------------

// The borough is how a user disambiguates two branches of the same name, and it
// has to narrow before the ambiguity test rather than after it.
func TestResolveRestaurantBoroughHintNarrowsTheField(t *testing.T) {
	repo := &fakeResolver{rows: []search.RestaurantCandidate{
		candidate(7, "Joe's Pizza", "manhattan", "7 Carmine St", 0.92),
		candidate(8, "Joe's Pizza", "brooklyn", "235 Bedford Ave", 0.90),
	}}
	entry := ResolveRestaurantEntry(repo, ResolveConfig{})

	if got := resolveResult(t, invoke(t, entry.Handler, `{"name":"Joe's Pizza"}`)).Status; got != ResolveAmbiguous {
		t.Fatalf("without a hint: status = %q, want ambiguous", got)
	}

	result := resolveResult(t, invoke(t, entry.Handler,
		`{"name":"Joe's Pizza","borough":"manhattan"}`))
	if result.Status != ResolveResolved {
		t.Fatalf("with a borough hint: status = %q, want resolved", result.Status)
	}
	if result.Restaurant == nil || result.Restaurant.RestaurantID != 7 {
		t.Fatalf("restaurant = %+v, want the manhattan one", result.Restaurant)
	}
}

func TestResolveRestaurantAddressHintNarrowsTheField(t *testing.T) {
	repo := &fakeResolver{rows: []search.RestaurantCandidate{
		candidate(7, "Joe's Pizza", "manhattan", "7 Carmine St", 0.91),
		candidate(8, "Joe's Pizza", "manhattan", "150 E Houston St", 0.89),
	}}
	entry := ResolveRestaurantEntry(repo, ResolveConfig{})

	result := resolveResult(t, invoke(t, entry.Handler,
		`{"name":"Joe's Pizza","address_hint":"houston"}`))
	if result.Status != ResolveResolved {
		t.Fatalf("status = %q, want resolved", result.Status)
	}
	if result.Restaurant == nil || result.Restaurant.RestaurantID != 8 {
		t.Fatalf("restaurant = %+v, want the Houston St one", result.Restaurant)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("the hint must leave one candidate: %+v", result.Candidates)
	}
}

// A hint that matches nothing leaves nothing to answer with. Returning the
// unfiltered list would make the hint look honoured when it was not.
func TestResolveRestaurantHintWithNoSurvivorIsNotFound(t *testing.T) {
	repo := &fakeResolver{rows: []search.RestaurantCandidate{
		candidate(7, "Joe's Pizza", "manhattan", "7 Carmine St", 0.91),
	}}

	result := resolveResult(t, invoke(t, ResolveRestaurantEntry(repo, ResolveConfig{}).Handler,
		`{"name":"Joe's Pizza","borough":"queens"}`))
	if result.Status != ResolveNotFound {
		t.Fatalf("status = %q, want not_found", result.Status)
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want none", result.Candidates)
	}
}

// ---- arguments ------------------------------------------------------------

func TestResolveRestaurantRequiresAName(t *testing.T) {
	repo := &fakeResolver{}
	_, err := ResolveRestaurantEntry(repo, ResolveConfig{}).Handler(
		context.Background(), json.RawMessage(`{"name":"   "}`))
	if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if repo.called != 0 {
		t.Fatal("an empty name must not reach the repository")
	}
}

// A borough nobody recognises would filter every row away and read as "no such
// restaurant", which is a different and more confusing answer.
func TestResolveRestaurantRejectsAnUnknownBorough(t *testing.T) {
	repo := &fakeResolver{}
	_, err := ResolveRestaurantEntry(repo, ResolveConfig{}).Handler(
		context.Background(), json.RawMessage(`{"name":"Joe's Pizza","borough":"atlantis"}`))
	if errs.CodeOf(err) != errs.CodeRetrievalInvalidFilter {
		t.Fatalf("err = %v, want retrieval_invalid_filter", err)
	}
	if repo.called != 0 {
		t.Fatal("an invalid borough must not reach the repository")
	}
}

func TestResolveRestaurantLimitIsClampedAndDefaulted(t *testing.T) {
	repo := &fakeResolver{}
	entry := ResolveRestaurantEntry(repo, ResolveConfig{})

	invoke(t, entry.Handler, `{"name":"x"}`)
	if repo.lastLimit != defaultResolveLimit {
		t.Fatalf("default limit = %d, want %d", repo.lastLimit, defaultResolveLimit)
	}
	invoke(t, entry.Handler, `{"name":"x","limit":2}`)
	if repo.lastLimit != 2 {
		t.Fatalf("limit = %d, want 2", repo.lastLimit)
	}
	// The configured limit is a ceiling: the schema advertises the knob, so a
	// larger value cannot silently do nothing, but it cannot exceed what the
	// operator allowed either.
	invoke(t, entry.Handler, `{"name":"x","limit":99}`)
	if repo.lastLimit != defaultResolveLimit {
		t.Fatalf("over-ceiling limit = %d, want %d", repo.lastLimit, defaultResolveLimit)
	}
	invoke(t, ResolveRestaurantEntry(repo, ResolveConfig{Limit: maxResolveLimit}).Handler,
		`{"name":"x","limit":99}`)
	if repo.lastLimit != maxResolveLimit {
		t.Fatalf("limit = %d, want the raised ceiling %d", repo.lastLimit, maxResolveLimit)
	}
}

func TestResolveRestaurantTrimsTheName(t *testing.T) {
	repo := &fakeResolver{}
	invoke(t, ResolveRestaurantEntry(repo, ResolveConfig{}).Handler, `{"name":"  Joe's Pizza  "}`)
	if repo.lastText != "Joe's Pizza" {
		t.Fatalf("looked up %q, want the trimmed name", repo.lastText)
	}
}

// The thresholds that produced a verdict travel with it: a surprising "not
// found" should be readable against the setting that caused it.
func TestResolveRestaurantReportsTheThresholdsItUsed(t *testing.T) {
	repo := &fakeResolver{rows: []search.RestaurantCandidate{
		candidate(9, "Somewhere Else", "queens", "1 Main St", 0.40),
	}}

	result := resolveResult(t, invoke(t, ResolveRestaurantEntry(repo, ResolveConfig{
		MinSimilarity: 0.30,
		AmbiguityGap:  0.25,
	}).Handler, `{"name":"Joe's Pizza"}`))
	if result.MinSimilarity != 0.30 || result.AmbiguityGap != 0.25 {
		t.Fatalf("thresholds = %v/%v, want the configured ones", result.MinSimilarity, result.AmbiguityGap)
	}
	if result.Status != ResolveResolved {
		t.Fatalf("status = %q; at a 0.30 floor this is a match", result.Status)
	}
}

func TestNormalizeResolveConfigFillsOnlyWhatIsMissing(t *testing.T) {
	got := NormalizeResolveConfig(ResolveConfig{MinSimilarity: 0.8})
	if got.MinSimilarity != 0.8 {
		t.Fatalf("min similarity = %v, want the supplied 0.8", got.MinSimilarity)
	}
	if got.AmbiguityGap != DefaultResolveAmbiguityGap {
		t.Fatalf("ambiguity gap = %v, want the default", got.AmbiguityGap)
	}
	if got.Limit != defaultResolveLimit {
		t.Fatalf("limit = %d, want the default", got.Limit)
	}
}

// The tool only turns a name into an id. It is read-only, and it is never the
// thing that answers a question.
func TestResolveRestaurantEntryIsAReadOnlyNameLookup(t *testing.T) {
	entry := ResolveRestaurantEntry(&fakeResolver{}, ResolveConfig{})
	if entry.Spec.Name != ResolveRestaurantToolName {
		t.Fatalf("name = %q", entry.Spec.Name)
	}
	if !entry.Spec.ReadOnly {
		t.Fatal("resolve_restaurant must be read-only")
	}
	if !strings.Contains(entry.Spec.Description, "get_restaurant_evidence") {
		t.Fatal("the description must say a resolved id still needs evidence")
	}
	var schema map[string]any
	if err := json.Unmarshal(entry.Spec.Parameters, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "name" {
		t.Fatalf("required = %v, want [name]", required)
	}
}

// The repository returning nothing is the same outcome as everything scoring
// too low: there is no restaurant by that name.
func TestResolveRestaurantEmptyRepositoryIsNotFound(t *testing.T) {
	repo := &fakeResolver{}
	result := resolveResult(t, invoke(t, ResolveRestaurantEntry(repo, ResolveConfig{}).Handler,
		`{"name":"Nowhere Grill"}`))
	if result.Status != ResolveNotFound {
		t.Fatalf("status = %q, want not_found", result.Status)
	}
	if result.Candidates == nil {
		t.Fatal("candidates must serialise as [] rather than null")
	}
}
