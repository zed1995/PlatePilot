package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zed1995/platepilot/chat-service/internal/retrieval"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"

	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
)

// EvidenceReader is the evidence slice of the retrieval service.
type EvidenceReader interface {
	Evidence(ctx context.Context, req retrieval.EvidenceRequest) (retrieval.EvidenceResult, error)
}

// RestaurantEvidenceToolName is the tool name the model sees.
const RestaurantEvidenceToolName = "get_restaurant_evidence"

const (
	// maxEvidenceRestaurants bounds one evidence call's scope.
	maxEvidenceRestaurants = 5
	// evidenceTopKDefault matches the retrieval service default.
	evidenceTopKDefault = 20
	evidenceTopKMax     = 100
)

const restaurantEvidenceSchema = `{
  "type": "object",
  "properties": {
    "restaurant_ids": {
      "type": "array",
      "items": {"type": "integer"},
      "description": "One to five restaurant ids from search_restaurants or resolve_restaurant. May be omitted on a follow-up turn, when the thread has already pinned a restaurant."
    },
    "query": {
      "type": "string",
      "description": "The specific question the evidence must answer"
    },
    "topic": {
      "type": "string",
      "description": "Optional review topic, e.g. 'service' or 'wait time'"
    },
    "doc_types": {
      "type": "array",
      "items": {
        "type": "string",
        "enum": ["restaurant_profile", "restaurant_attributes", "restaurant_hours", "restaurant_review_summary", "restaurant_representative_reviews"]
      },
      "description": "Restrict to these document kinds"
    },
    "top_k": {
      "type": "integer",
      "description": "Maximum evidence documents to recall, 1..100"
    }
  },
  "required": ["query"],
  "additionalProperties": false
}`

type restaurantEvidenceArgs struct {
	RestaurantIDs []int64  `json:"restaurant_ids"`
	Query         string   `json:"query"`
	Topic         string   `json:"topic"`
	DocTypes      []string `json:"doc_types"`
	TopK          int      `json:"top_k"`
}

// RestaurantEvidenceEntry builds the registry entry.
func RestaurantEvidenceEntry(svc EvidenceReader) toolreg.Entry {
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name:        RestaurantEvidenceToolName,
			Description: "Gather citable evidence documents for one to five restaurants to answer a specific factual question (hours, attributes, review summaries, representative reviews). Every answer statement must cite the evidence_id this tool returns.",
			Parameters:  json.RawMessage(restaurantEvidenceSchema),
			ReadOnly:    true,
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (domaintool.ToolResult, error) {
			var args restaurantEvidenceArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return domaintool.ToolResult{}, errs.Wrap(errs.CodeValidationFailed, "decode get_restaurant_evidence arguments", err)
			}
			if strings.TrimSpace(args.Query) == "" {
				return domaintool.ToolResult{}, errs.New(errs.CodeInvalidArgument,
					"get_restaurant_evidence requires a query")
			}
			// A follow-up turn is about the restaurant the thread pinned, and
			// the plan is where that decision lives — "第二家" was resolved into
			// an id before the model ever saw the message. Filling it in is the
			// same fill-omissions-never-override rule the search tool follows:
			// ids the model named are used verbatim, and the plan is consulted
			// only when it named none.
			restaurantIDs := args.RestaurantIDs
			if len(restaurantIDs) == 0 {
				if plan, ok := slots.PlanFromContext(ctx); ok && plan.SelectedRestaurantID != 0 {
					restaurantIDs = []int64{plan.SelectedRestaurantID}
				}
			}
			if len(restaurantIDs) == 0 {
				return domaintool.ToolResult{}, errs.New(errs.CodeInvalidArgument,
					"get_restaurant_evidence requires at least one restaurant id")
			}
			if len(restaurantIDs) > maxEvidenceRestaurants {
				return domaintool.ToolResult{}, errs.Newf(errs.CodeInvalidArgument,
					"get_restaurant_evidence accepts at most %d restaurant ids (got %d)",
					maxEvidenceRestaurants, len(restaurantIDs))
			}
			docTypes, err := mapDocTypes(args.DocTypes)
			if err != nil {
				return domaintool.ToolResult{}, err
			}

			result, err := svc.Evidence(ctx, retrieval.EvidenceRequest{
				RestaurantIDs: restaurantIDs,
				Query:         args.Query,
				Topic:         args.Topic,
				DocTypes:      docTypes,
				TopK:          clampTopK(args.TopK, 1, evidenceTopKMax, evidenceTopKDefault),
			})
			if err != nil {
				return domaintool.ToolResult{}, err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return domaintool.ToolResult{}, errs.Wrap(errs.CodeInternal, "encode evidence result", err)
			}
			return domaintool.ToolResult{
				Status:  domaintool.ToolStatusOK,
				Content: renderEvidence(result),
				Data:    data,
			}, nil
		},
	}
}

// mapDocTypes validates the doc type enum and maps it onto domain values.
func mapDocTypes(raw []string) ([]evidence.DocType, error) {
	known := map[evidence.DocType]struct{}{
		evidence.DocTypeRestaurantProfile:               {},
		evidence.DocTypeRestaurantAttributes:            {},
		evidence.DocTypeRestaurantHours:                 {},
		evidence.DocTypeRestaurantReviewSummary:         {},
		evidence.DocTypeRestaurantRepresentativeReviews: {},
	}
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]evidence.DocType, 0, len(raw))
	for _, name := range raw {
		docType := evidence.DocType(name)
		if _, ok := known[docType]; !ok {
			return nil, errs.Newf(errs.CodeValidationFailed,
				"unknown doc_type %q", name)
		}
		out = append(out, docType)
	}
	return out, nil
}

// renderEvidence builds the compact model summary. Every document keeps its
// EvidenceID because that ID is the only legal citation key.
func renderEvidence(result retrieval.EvidenceResult) string {
	if len(result.Evidence) == 0 {
		text := "未找到相关证据资料。"
		if result.Trace != nil && len(result.Trace.Warnings) > 0 {
			text += " 降级提示：" + strings.Join(result.Trace.Warnings, "；")
		}
		return text
	}
	var b strings.Builder
	fmt.Fprintf(&b, "召回 %d 条证据（引用时使用 evidence_id）：\n", len(result.Evidence))
	for _, item := range result.Evidence {
		fmt.Fprintf(&b, "[证据 %d | restaurant_id=%d | %s",
			item.EvidenceID, item.RestaurantID, string(item.DocType))
		if item.RestaurantName != "" {
			fmt.Fprintf(&b, " | %s", item.RestaurantName)
		}
		if item.Title != "" {
			fmt.Fprintf(&b, " | %s", item.Title)
		}
		fmt.Fprintf(&b, " | tokens≈%d]\n", retrieval.EstimateTokens(item.Content))
		b.WriteString(strings.TrimSpace(item.Content))
		b.WriteString("\n\n")
	}
	if result.Trace != nil && len(result.Trace.Warnings) > 0 {
		b.WriteString("降级提示：" + strings.Join(result.Trace.Warnings, "；") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
