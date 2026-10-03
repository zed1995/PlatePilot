package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/search"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
)

// RestaurantResolver is the name-to-candidate slice of the restaurant
// repository.
//
// It is deliberately narrower than store.RestaurantRepository: resolving a name
// proposes candidates and never enforces conditions, so the tool cannot acquire
// the ability to filter by accident.
type RestaurantResolver interface {
	MatchByText(ctx context.Context, text string, limit int) ([]search.RestaurantCandidate, error)
}

// ResolveRestaurantToolName is the tool name the model sees.
const ResolveRestaurantToolName = "resolve_restaurant"

// Resolve statuses. The set is closed and each value is a different thing for
// the caller to do: answer from evidence, ask a question, or search by need.
type ResolveStatus string

const (
	// ResolveResolved means one candidate is clearly the best match.
	ResolveResolved ResolveStatus = "resolved"
	// ResolveAmbiguous means two or more candidates match about equally well,
	// and the user has to choose. It is not an error: it is the name being
	// genuinely shared, which is common enough in this corpus for it to be a
	// normal outcome rather than a failure.
	ResolveAmbiguous ResolveStatus = "ambiguous"
	// ResolveNotFound means nothing matched well enough. It is also not an
	// error — the correct next step is to search by need or ask the user to
	// spell the name, and answering with a bad guess would be worse than
	// answering with nothing.
	ResolveNotFound ResolveStatus = "not_found"
)

// Resolve defaults. They are exported because the runner's configuration knob
// and the tool's default are the same value, and two literals would be two
// places for the threshold to drift.
const (
	DefaultResolveMinSimilarity = 0.55
	DefaultResolveAmbiguityGap  = 0.10
	defaultResolveLimit         = 3
	maxResolveLimit             = 5
)

// ResolveConfig tunes the three judgements the tool makes.
type ResolveConfig struct {
	// MinSimilarity is the floor below which a match is not a match. Without
	// it, a name nobody typed would resolve onto whatever the trigram index
	// thought was closest.
	MinSimilarity float64
	// AmbiguityGap is how close the top two scores must be for the answer to
	// be "which one did you mean". A gap of zero would ask a question every
	// time two restaurants shared a prefix; a large one would guess between two
	// genuinely identical names.
	AmbiguityGap float64
	// Limit is how many candidates to consider, and therefore how many the
	// clarification question can offer. It is also the ceiling on what the model
	// may ask for: an operator who set three because three options is as many as
	// a question can carry should not be overridden by a model that passed five.
	Limit int
}

// NormalizeResolveConfig fills unset fields with the package defaults.
func NormalizeResolveConfig(cfg ResolveConfig) ResolveConfig {
	if cfg.MinSimilarity <= 0 {
		cfg.MinSimilarity = DefaultResolveMinSimilarity
	}
	if cfg.AmbiguityGap <= 0 {
		cfg.AmbiguityGap = DefaultResolveAmbiguityGap
	}
	cfg.Limit = clampTopK(cfg.Limit, 1, maxResolveLimit, defaultResolveLimit)
	return cfg
}

// ResolvedRestaurant is the projection of a restaurant a caller can act on.
//
// It carries no score: the score belongs to the match, not to the restaurant,
// and a struct that mixed the two would let a caller store a similarity as
// though it were a property of the place.
type ResolvedRestaurant struct {
	RestaurantID int64    `json:"restaurant_id"`
	Name         string   `json:"name"`
	Address      string   `json:"address,omitempty"`
	Borough      string   `json:"borough,omitempty"`
	Cuisines     []string `json:"cuisines,omitempty"`
	Rating       *float64 `json:"rating,omitempty"`
	PriceLevel   *int     `json:"price_level,omitempty"`
}

// ResolveCandidate is one restaurant plus how well the name matched it.
type ResolveCandidate struct {
	ResolvedRestaurant
	Similarity float64 `json:"similarity"`
}

// ResolveResult is what resolve_restaurant returns.
type ResolveResult struct {
	// Name is the text the model asked to resolve, echoed so the transcript
	// shows what was looked up rather than only what was found.
	Name string `json:"name"`
	// Status decides what the caller does next.
	Status ResolveStatus `json:"status"`
	// Restaurant is set only when Status is resolved.
	Restaurant *ResolvedRestaurant `json:"restaurant,omitempty"`
	// Candidates are the matches considered, best first. They are returned for
	// the ambiguous and not_found cases too: a not_found with "the best score
	// was 0.31" is debuggable, and one without is a shrug.
	Candidates []ResolveCandidate `json:"candidates"`
	// Similarity is the best match's score, zero when nothing matched.
	Similarity float64 `json:"similarity,omitempty"`
	// MinSimilarity and AmbiguityGap record the thresholds that produced this
	// verdict, so a surprising result can be read against the settings that
	// caused it instead of against a guess.
	MinSimilarity float64 `json:"min_similarity"`
	AmbiguityGap  float64 `json:"ambiguity_gap"`
}

const resolveRestaurantSchema = `{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "The restaurant name the user said, verbatim, e.g. \"Katz's Delicatessen\""
    },
    "address_hint": {
      "type": "string",
      "description": "A street or neighborhood fragment the user mentioned to disambiguate, e.g. 'Houston St'"
    },
    "borough": {
      "type": "string",
      "enum": ["manhattan", "brooklyn", "queens", "bronx", "staten_island"],
      "description": "Restrict the match to one NYC borough"
    },
    "limit": {
      "type": "integer",
      "description": "How many candidates to consider, 1..5"
    }
  },
  "required": ["name"],
  "additionalProperties": false
}`

type resolveRestaurantArgs struct {
	Name        string `json:"name"`
	AddressHint string `json:"address_hint"`
	Borough     string `json:"borough"`
	Limit       int    `json:"limit"`
}

// ResolveRestaurantEntry builds the registry entry.
//
// The tool only turns a name into an id. It never answers a question and never
// skips the evidence step: a resolved id is a scope, not an answer, and a model
// allowed to treat the two as the same would produce ungrounded prose about a
// restaurant it only knows the name of.
func ResolveRestaurantEntry(repo RestaurantResolver, cfg ResolveConfig) toolreg.Entry {
	cfg = NormalizeResolveConfig(cfg)
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name: ResolveRestaurantToolName,
			Description: "Turn a restaurant name the user said into a restaurant_id, " +
				"disambiguating by address or borough. Returns status resolved | ambiguous | " +
				"not_found. An ambiguous result means you must ask the user which one before " +
				"answering; a resolved id still has to be read with get_restaurant_evidence " +
				"before anything can be stated about it.",
			Parameters: json.RawMessage(resolveRestaurantSchema),
			ReadOnly:   true,
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (domaintool.ToolResult, error) {
			var args resolveRestaurantArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return domaintool.ToolResult{}, errs.Wrap(errs.CodeValidationFailed,
					"decode resolve_restaurant arguments", err)
			}
			name := strings.TrimSpace(args.Name)
			if name == "" {
				return domaintool.ToolResult{}, errs.New(errs.CodeInvalidArgument,
					"resolve_restaurant requires a name")
			}
			// The borough is validated here rather than left to filter nothing:
			// an unrecognised value would silently match no rows and read as
			// "no such restaurant". Parenthesised because a composite literal
			// followed by a method call is ambiguous with the if-block.
			if args.Borough != "" {
				if err := (search.RestaurantFilter{Borough: args.Borough}).Validate(); err != nil {
					return domaintool.ToolResult{}, err
				}
			}

			// The model's limit is honoured inside the configured ceiling rather
			// than ignored: the schema advertises the knob, so a value that
			// silently did nothing would be a lie in the tool description. The
			// ceiling itself is the operator's, because it decides how many
			// options a clarification question can carry.
			limit := clampTopK(args.Limit, 1, cfg.Limit, cfg.Limit)
			rows, err := repo.MatchByText(ctx, name, limit)
			if err != nil {
				return domaintool.ToolResult{}, err
			}

			result := resolveMatches(name, rows, args, cfg)
			data, err := json.Marshal(result)
			if err != nil {
				return domaintool.ToolResult{}, errs.Wrap(errs.CodeInternal,
					"encode resolve_restaurant result", err)
			}
			return domaintool.ToolResult{
				Status:  domaintool.ToolStatusOK,
				Content: renderResolve(result),
				Data:    data,
			}, nil
		},
	}
}

// resolveMatches applies the hints, the similarity floor, and the ambiguity
// test, in that order.
//
// The order matters: hints and the floor narrow what counts as a match, and only
// then does "are the top two too close to call" mean anything. Running the
// ambiguity test first would make a clear winner look ambiguous because a
// rejected near-miss was still on the list.
func resolveMatches(
	name string, rows []search.RestaurantCandidate, args resolveRestaurantArgs, cfg ResolveConfig,
) ResolveResult {
	result := ResolveResult{
		Name:          name,
		Status:        ResolveNotFound,
		Candidates:    []ResolveCandidate{},
		MinSimilarity: cfg.MinSimilarity,
		AmbiguityGap:  cfg.AmbiguityGap,
	}

	hinted := make([]ResolveCandidate, 0, len(rows))
	for _, row := range rows {
		if args.Borough != "" && search.CanonicalBorough(row.Borough) != search.CanonicalBorough(args.Borough) {
			continue
		}
		if hint := strings.TrimSpace(args.AddressHint); hint != "" &&
			!strings.Contains(strings.ToLower(row.Address), strings.ToLower(hint)) {
			continue
		}
		if row.Score < cfg.MinSimilarity {
			continue
		}
		hinted = append(hinted, ResolveCandidate{
			ResolvedRestaurant: ResolvedRestaurant{
				RestaurantID: row.RestaurantID,
				Name:         row.Name,
				Address:      row.Address,
				Borough:      row.Borough,
				Cuisines:     row.Cuisines,
				Rating:       row.Rating,
				PriceLevel:   row.PriceLevel,
			},
			Similarity: row.Score,
		})
	}
	result.Candidates = hinted
	if len(hinted) == 0 {
		return result
	}

	// The repository already orders by score, but the tool does not rely on it:
	// the ambiguity decision is "are the two best too close", and reading that
	// off an ordering the tool did not establish would make the verdict depend
	// on an adapter's tie-breaking.
	sortResolveCandidates(hinted)
	result.Similarity = hinted[0].Similarity
	result.Candidates = hinted

	if len(hinted) > 1 && hinted[0].Similarity-hinted[1].Similarity < cfg.AmbiguityGap {
		result.Status = ResolveAmbiguous
		return result
	}
	result.Status = ResolveResolved
	best := hinted[0].ResolvedRestaurant
	result.Restaurant = &best
	return result
}

// sortResolveCandidates orders by score, then id, so two identical scores come
// back in the same order every run.
func sortResolveCandidates(candidates []ResolveCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Similarity != candidates[j].Similarity {
			return candidates[i].Similarity > candidates[j].Similarity
		}
		return candidates[i].RestaurantID < candidates[j].RestaurantID
	})
}

// renderResolve builds the compact model summary.
//
// The three statuses get three different openings because the model's next move
// differs: resolved means go read evidence, ambiguous means stop and ask,
// not_found means do not answer about this restaurant at all.
func renderResolve(result ResolveResult) string {
	var b strings.Builder
	switch result.Status {
	case ResolveResolved:
		fmt.Fprintf(&b, "已唯一确定餐厅（相似度 %.3f）：\n", result.Similarity)
	case ResolveAmbiguous:
		fmt.Fprintf(&b, "名称“%s”匹配到 %d 家，无法确定是哪一家。"+
			"必须先请用户选择，不要猜测，也不要先取证据。\n", result.Name, len(result.Candidates))
	default:
		fmt.Fprintf(&b, "没有找到名称匹配“%s”的餐厅（最低相似度要求 %.2f）。"+
			"这是正常结果：可以改用 search_restaurants 按需求检索，或请用户确认名称。\n",
			result.Name, result.MinSimilarity)
	}
	for i, candidate := range result.Candidates {
		fmt.Fprintf(&b, "%d. id=%d %s", i+1, candidate.RestaurantID, candidate.Name)
		if candidate.Borough != "" {
			fmt.Fprintf(&b, " | %s", candidate.Borough)
		}
		if candidate.Address != "" {
			fmt.Fprintf(&b, " | %s", candidate.Address)
		}
		if len(candidate.Cuisines) > 0 {
			fmt.Fprintf(&b, " | %s", strings.Join(candidate.Cuisines, "/"))
		}
		if candidate.Rating != nil {
			fmt.Fprintf(&b, " | 评分%.1f", *candidate.Rating)
		}
		fmt.Fprintf(&b, " | 相似度%.3f\n", candidate.Similarity)
	}
	if result.Status == ResolveResolved && result.Restaurant != nil {
		fmt.Fprintf(&b, "请用 get_restaurant_evidence 取 restaurant_id=%d 的证据后再回答。\n",
			result.Restaurant.RestaurantID)
	}
	return strings.TrimRight(b.String(), "\n")
}
