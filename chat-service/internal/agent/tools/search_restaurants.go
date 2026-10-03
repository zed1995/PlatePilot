// Package tools adapts the retrieval read path onto agent tool entries.
//
// Each file owns one tool: its JSON schema, its argument mapping, and its
// compact result rendering. The handlers return the domain retrieval DTOs in
// ToolResult.Data so the graph can update TurnState without re-deriving the
// results, and a compact ToolResult.Content so the model gets a token-cheap
// summary plus any degradation warnings.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zed1995/platepilot/shared/domain/errs"
	domainretrieval "github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/search"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"

	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
)

// Searcher is the search slice of the retrieval service.
type Searcher interface {
	Search(ctx context.Context, req domainretrieval.Request) (domainretrieval.SearchResult, error)
}

// searchRestaurantsSchema is the model-facing argument schema.
//
// It separates deterministic conditions (borough, cuisine, price, rating,
// open-now) from soft ones (soft_conditions), because the two take different
// paths through retrieval and the model has to be told which is which before it
// can file them correctly. Every deterministic field maps onto a column; the
// soft list maps onto a review topic and never onto a filter.
const searchRestaurantsSchema = `{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Natural-language restaurant need, e.g. 'quiet Italian place for a date near the East Village'"
    },
    "name": {
      "type": "string",
      "description": "A restaurant name or address fragment to match literally, e.g. \"Katz's Delicatessen\""
    },
    "borough": {
      "type": "string",
      "enum": ["manhattan", "brooklyn", "queens", "bronx", "staten_island"],
      "description": "Restrict results to one NYC borough"
    },
    "neighborhood": {
      "type": "string",
      "description": "A neighborhood or street name to match inside the address, e.g. 'Midtown', 'Williamsburg'"
    },
    "cuisine": {
      "type": "string",
      "description": "Cuisine or food type, e.g. 'italian', 'ramen'"
    },
    "cuisines": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Several acceptable cuisines at once, e.g. ['japanese','sushi'] for 日料. A restaurant matching any of them qualifies"
    },
    "price_levels": {
      "type": "array",
      "items": {"type": "integer", "minimum": 1, "maximum": 4},
      "description": "Price bands to accept, 1..4 (1 cheapest). 'under $$' is [1,2]"
    },
    "min_rating": {
      "type": "number",
      "description": "Minimum knowledge-base average rating, 0..5"
    },
    "open_now": {
      "type": "boolean",
      "description": "When true, only restaurants currently open"
    },
    "soft_conditions": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Requirements only reviews can support, e.g. ['安静', '适合约会']. They influence recall and ranking but never exclude a restaurant"
    },
    "top_k": {
      "type": "integer",
      "description": "Maximum candidates to return, 1..10"
    }
  },
  "additionalProperties": false
}`

// searchRestaurantsArgs is the decoded argument object.
type searchRestaurantsArgs struct {
	Query          string   `json:"query"`
	Name           string   `json:"name"`
	Borough        string   `json:"borough"`
	Neighborhood   string   `json:"neighborhood"`
	Cuisine        string   `json:"cuisine"`
	Cuisines       []string `json:"cuisines"`
	PriceLevels    []int    `json:"price_levels"`
	MinRating      *float64 `json:"min_rating"`
	OpenNow        *bool    `json:"open_now"`
	SoftConditions []string `json:"soft_conditions"`
	TopK           int      `json:"top_k"`
}

// filterCuisines merges the singular and plural cuisine arguments.
//
// Both exist because the model's phrasing varies: one cuisine is a word, two is
// a list, and forcing either shape produces a worse extraction than accepting
// both. The merged list keeps first-seen order and drops duplicates, so it is
// stable for a caller that compares requests.
func (a searchRestaurantsArgs) filterCuisines() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range append([]string{a.Cuisine}, a.Cuisines...) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// SearchRestaurantsToolName is the tool name the model sees.
const SearchRestaurantsToolName = "search_restaurants"

const searchRestaurantsTopK = 10

// SearchRestaurantsEntry builds the registry entry.
func SearchRestaurantsEntry(svc Searcher) toolreg.Entry {
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name: SearchRestaurantsToolName,
			Description: "Search NYC restaurants by a natural-language need with optional name, borough, " +
				"neighborhood, cuisine, price-band, rating, and open-now filters. Soft conditions " +
				"(quiet, good for a date) rank results without excluding any. Returns ranked candidates " +
				"with ids used by get_restaurant_evidence.",
			Parameters: json.RawMessage(searchRestaurantsSchema),
			ReadOnly:   true,
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (domaintool.ToolResult, error) {
			var args searchRestaurantsArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return domaintool.ToolResult{}, errs.Wrap(errs.CodeValidationFailed, "decode search_restaurants arguments", err)
			}
			// Whatever the model left out is filled from the turn's own
			// interpretation of the sentence. This is what makes "every returned
			// candidate satisfies every stated hard condition" a property of the
			// system rather than of the model's diligence: a condition the user
			// stated cannot disappear because the model did not repeat it in the
			// tool call.
			plan, hasPlan := slots.PlanFromContext(ctx)
			var notes []string
			if hasPlan {
				notes = applyPlanDefaults(&args, plan)
			}
			if !args.hasCondition() {
				return domaintool.ToolResult{}, errs.New(errs.CodeInvalidArgument,
					"search_restaurants requires at least one of query/name/borough/neighborhood/"+
						"cuisine/cuisines/price_levels/min_rating/open_now/soft_conditions")
			}

			req := domainretrieval.Request{
				Query: args.Query,
				// The name fragment goes to the keyword channel through Text; the
				// whole question stays in Query for the semantic channel. Keeping
				// them apart is what stops a name from being embedded as a
				// sentence and a question from being matched as a name.
				Text: args.Name,
				TopK: clampTopK(args.TopK, 1, searchRestaurantsTopK, searchRestaurantsTopK),
				Filter: search.RestaurantFilter{
					Cuisines:     args.filterCuisines(),
					PriceLevels:  args.PriceLevels,
					Neighborhood: args.Neighborhood,
					Borough:      args.Borough,
					MinRating:    args.MinRating,
					OpenNow:      args.OpenNow,
				},
				// Soft conditions are carried beside the filter, never inside it:
				// they may rank a result but must never exclude one, because the
				// corpus cannot evaluate them and an exclusion would be
				// indistinguishable from "no reviews mention this".
				SoftConditions: args.softConditionList(plan),
			}
			// The extracted filter is validated here rather than left to the
			// retrieval layer so an illegal borough surfaces as a request
			// problem naming the field, not as an empty candidate list.
			if err := req.Filter.Validate(); err != nil {
				return domaintool.ToolResult{}, err
			}

			result, err := svc.Search(ctx, req)
			if err != nil {
				return domaintool.ToolResult{}, err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return domaintool.ToolResult{}, errs.Wrap(errs.CodeInternal, "encode search result", err)
			}
			content := renderCandidates(result)
			if len(notes) > 0 {
				// The note is not decoration: the model has to know what was
				// actually searched in order for its answer to describe the
				// result it was given. It also lands in the run's tool-call
				// summary, which is where the plan's contribution becomes
				// auditable after the fact.
				content = "本轮检索条件（理解层已补齐模型未给出的部分）：" +
					strings.Join(notes, "，") + "\n" + content
			}
			return domaintool.ToolResult{
				Status:  domaintool.ToolStatusOK,
				Content: content,
				Data:    data,
			}, nil
		},
	}
}

// applyPlanDefaults fills the search arguments the model left out with what the
// turn's plan already established, and returns one note per applied value.
//
// The direction is deliberate: the plan fills omissions, it does not override.
// A model that supplied a value read the same sentence and may be deliberately
// widening a search that came back empty ("没有 4 星的，那就看看 3 星"), and
// overriding it would make that retry impossible. What the plan guarantees is
// the other half — a condition the user stated is never absent from the search
// merely because the model did not carry it into the call.
func applyPlanDefaults(args *searchRestaurantsArgs, plan slots.Plan) []string {
	var notes []string
	filter := plan.HardFilters

	if strings.TrimSpace(args.Query) == "" && plan.Query != "" {
		args.Query = plan.Query
	}
	if args.Borough == "" && filter.Borough != "" {
		args.Borough = filter.Borough
		notes = append(notes, "borough="+filter.Borough)
	}
	if args.Neighborhood == "" && filter.Neighborhood != "" {
		args.Neighborhood = filter.Neighborhood
		notes = append(notes, "neighborhood="+filter.Neighborhood)
	}
	if len(args.filterCuisines()) == 0 && len(filter.Cuisines) > 0 {
		args.Cuisines = append([]string(nil), filter.Cuisines...)
		notes = append(notes, "cuisines="+strings.Join(filter.Cuisines, "/"))
	}
	if len(args.PriceLevels) == 0 && len(filter.PriceLevels) > 0 {
		args.PriceLevels = append([]int(nil), filter.PriceLevels...)
		notes = append(notes, fmt.Sprintf("price_levels=%v", filter.PriceLevels))
	}
	if args.MinRating == nil && filter.MinRating != nil {
		value := *filter.MinRating
		args.MinRating = &value
		notes = append(notes, fmt.Sprintf("min_rating=%g", value))
	}
	if args.OpenNow == nil && filter.OpenNow != nil {
		value := *filter.OpenNow
		args.OpenNow = &value
		notes = append(notes, fmt.Sprintf("open_now=%t", value))
	}
	if texts := mergeSoftConditions(args.SoftConditions, plan.SoftTexts()); len(texts) > 0 {
		args.SoftConditions = texts
		notes = append(notes, "soft_conditions="+strings.Join(texts, "/"))
	}
	return notes
}

// mergeSoftConditions unions the model's soft conditions with the plan's,
// keeping first-seen order.
//
// Unlike the hard values, these are additive: a soft condition can only widen
// what the ranking considers, so dropping either source would lose a signal
// without protecting anything.
func mergeSoftConditions(model, plan []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range append(append([]string(nil), model...), plan...) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

// softConditionList pairs every soft condition with the review topic it maps
// onto.
//
// The plan's mapping is preferred over a fresh lookup because the plan derived
// it from the same sentence with more context than a bare phrase carries: the
// model may hand the tool "quiet" while the user wrote "安静", and only the
// plan knows those are one condition. A condition the plan never saw — a model
// inventing one — is still mapped, and one that maps onto nothing keeps an
// empty topic rather than being dropped: the words alone still improve the
// embedding query, and discarding them would silently narrow the search.
func (a searchRestaurantsArgs) softConditionList(plan slots.Plan) []domainretrieval.SoftCondition {
	fromPlan := make(map[string]string, len(plan.SoftConditions))
	for _, condition := range plan.SoftConditions {
		key := strings.ToLower(strings.TrimSpace(condition.Text))
		if key == "" {
			continue
		}
		if _, ok := fromPlan[key]; !ok {
			fromPlan[key] = condition.Topic
		}
	}

	seen := map[string]struct{}{}
	var out []domainretrieval.SoftCondition
	for _, raw := range a.SoftConditions {
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		key := strings.ToLower(text)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		topic := fromPlan[key]
		if topic == "" {
			if derived, ok := slots.TopicFor(text); ok {
				topic = derived
			}
		}
		out = append(out, domainretrieval.SoftCondition{Text: text, Topic: topic})
	}
	return out
}

// hasCondition reports whether the model actually asked for something. A call
// with no condition at all is a listing, not a search, and answering it would
// present the corpus in prior order as though the question had found it.
func (a searchRestaurantsArgs) hasCondition() bool {
	return strings.TrimSpace(a.Query) != "" || strings.TrimSpace(a.Name) != "" ||
		a.Borough != "" || a.Neighborhood != "" || len(a.filterCuisines()) > 0 ||
		len(a.PriceLevels) > 0 || a.MinRating != nil || a.OpenNow != nil ||
		hasNonEmpty(a.SoftConditions)
}

func hasNonEmpty(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// renderCandidates builds the compact, token-cheap model summary.
//
// The match reasons and the data time travel with each candidate because a
// recommendation the model cannot explain is one it will explain anyway, from
// nothing. With the reasons in front of it the model can quote what actually
// matched — including which channel found the restaurant — instead of
// reconstructing a plausible-sounding justification.
//
// The trailing notes are labelled "检索说明" rather than "降级提示": the list
// now carries both degradations and the soft-condition explanation, and calling
// a channel that ran successfully a degradation would teach the model to
// apologise for a ranking that was in fact complete.
func renderCandidates(result domainretrieval.SearchResult) string {
	if len(result.Candidates) == 0 {
		text := "未找到符合条件的餐厅。"
		if result.Trace != nil && len(result.Trace.Warnings) > 0 {
			text += " 检索说明：" + strings.Join(result.Trace.Warnings, "；")
		}
		return text
	}
	var b strings.Builder
	fmt.Fprintf(&b, "找到 %d 家候选餐厅（按相关度排序）：\n", len(result.Candidates))
	for i, candidate := range result.Candidates {
		fmt.Fprintf(&b, "%d. id=%d %s", i+1, candidate.RestaurantID, candidate.Name)
		if candidate.Borough != "" {
			fmt.Fprintf(&b, " | %s", candidate.Borough)
		}
		if len(candidate.Cuisines) > 0 {
			fmt.Fprintf(&b, " | %s", strings.Join(candidate.Cuisines, "/"))
		}
		if candidate.Rating != nil {
			fmt.Fprintf(&b, " | 评分%.1f", *candidate.Rating)
		}
		if candidate.PriceLevel != nil {
			fmt.Fprintf(&b, " | 价格%d", *candidate.PriceLevel)
		}
		if candidate.Address != "" {
			fmt.Fprintf(&b, " | %s", candidate.Address)
		}
		b.WriteString("\n")
		if len(candidate.Reasons) > 0 {
			fmt.Fprintf(&b, "   匹配原因：%s\n", strings.Join(candidate.Reasons, "；"))
		}
		if !candidate.SnapshotAt.IsZero() {
			fmt.Fprintf(&b, "   数据时间：%s\n", candidate.SnapshotAt.UTC().Format("2006-01-02"))
		}
	}
	if result.Trace != nil && len(result.Trace.Warnings) > 0 {
		b.WriteString("检索说明：" + strings.Join(result.Trace.Warnings, "；"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// clampTopK bounds a top_k argument; zero means the caller named no value.
func clampTopK(value, min, max, fallback int) int {
	if value <= 0 {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
