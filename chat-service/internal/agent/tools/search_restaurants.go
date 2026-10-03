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

	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
)

// Searcher is the search slice of the retrieval service.
type Searcher interface {
	Search(ctx context.Context, req domainretrieval.Request) (domainretrieval.SearchResult, error)
}

// SearchRestaurantsToolName is the tool name the model sees.
const SearchRestaurantsToolName = "search_restaurants"

const searchRestaurantsTopK = 10

// searchRestaurantsSchema is the model-facing argument schema.
const searchRestaurantsSchema = `{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Natural-language restaurant need, e.g. 'quiet Italian place for a date near the East Village'"
    },
    "borough": {
      "type": "string",
      "enum": ["manhattan", "brooklyn", "queens", "bronx", "staten_island"],
      "description": "Restrict results to one NYC borough"
    },
    "cuisine": {
      "type": "string",
      "description": "Cuisine or food type, e.g. 'italian', 'ramen'"
    },
    "min_rating": {
      "type": "number",
      "description": "Minimum knowledge-base average rating, 0..5"
    },
    "open_now": {
      "type": "boolean",
      "description": "When true, only restaurants currently open"
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
	Query     string   `json:"query"`
	Borough   string   `json:"borough"`
	Cuisine   string   `json:"cuisine"`
	MinRating *float64 `json:"min_rating"`
	OpenNow   *bool    `json:"open_now"`
	TopK      int      `json:"top_k"`
}

// SearchRestaurantsEntry builds the registry entry.
func SearchRestaurantsEntry(svc Searcher) toolreg.Entry {
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name:        SearchRestaurantsToolName,
			Description: "Search NYC restaurants by a natural-language need with optional borough, cuisine, rating, and open-now filters. Returns ranked candidates with ids used by get_restaurant_evidence.",
			Parameters:  json.RawMessage(searchRestaurantsSchema),
			ReadOnly:    true,
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (domaintool.ToolResult, error) {
			var args searchRestaurantsArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return domaintool.ToolResult{}, errs.Wrap(errs.CodeValidationFailed, "decode search_restaurants arguments", err)
			}
			if strings.TrimSpace(args.Query) == "" && args.Borough == "" && args.Cuisine == "" &&
				args.MinRating == nil && args.OpenNow == nil {
				return domaintool.ToolResult{}, errs.New(errs.CodeInvalidArgument,
					"search_restaurants requires at least one of query/borough/cuisine/min_rating/open_now")
			}

			req := domainretrieval.Request{
				Query: args.Query,
				TopK:  clampTopK(args.TopK, 1, searchRestaurantsTopK, searchRestaurantsTopK),
				Filter: search.RestaurantFilter{
					Borough:   args.Borough,
					MinRating: args.MinRating,
					OpenNow:   args.OpenNow,
				},
			}
			if args.Cuisine != "" {
				req.Filter.Cuisines = []string{args.Cuisine}
			}

			result, err := svc.Search(ctx, req)
			if err != nil {
				return domaintool.ToolResult{}, err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return domaintool.ToolResult{}, errs.Wrap(errs.CodeInternal, "encode search result", err)
			}
			return domaintool.ToolResult{
				Status:  domaintool.ToolStatusOK,
				Content: renderCandidates(result),
				Data:    data,
			}, nil
		},
	}
}

// renderCandidates builds the compact, token-cheap model summary.
func renderCandidates(result domainretrieval.SearchResult) string {
	if len(result.Candidates) == 0 {
		text := "未找到符合条件的餐厅。"
		if result.Trace != nil && len(result.Trace.Warnings) > 0 {
			text += " 降级提示：" + strings.Join(result.Trace.Warnings, "；")
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
	}
	if result.Trace != nil && len(result.Trace.Warnings) > 0 {
		b.WriteString("降级提示：" + strings.Join(result.Trace.Warnings, "；"))
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
