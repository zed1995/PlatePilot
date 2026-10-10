package slots

import (
	"encoding/json"

	"github.com/zed1995/platepilot/shared/domain/review"
)

// extractionInstruction is the extraction prompt.
//
// Its whole job is the hard/soft boundary. The model is not asked to decide
// what is answerable — it is asked to put each thing it heard into the field
// that matches how the system will treat it. Stating that division twice, once
// as a rule and once as a worked vocabulary, is deliberate: the vocabulary is
// what the model actually pattern-matches on, and the rule is what a reader
// checks it against.
//
// The query field is copied verbatim rather than rewritten. A model that
// paraphrases loses the user's own words, and the soft channels recall on
// exactly those words; a summary of "想吃点清淡的" is not a query anything can
// match.
const extractionInstruction = `You are PlatePilot's intent and slot extractor. Extract the user's restaurant needs from this turn into structured conditions, and output JSON only.

[Hard conditions] can be filtered deterministically in the database; put them in the matching fields:
- borough: one of manhattan / brooklyn / queens / bronx / staten_island only.
  When the user names a commercial area or neighborhood (Midtown, Downtown, SoHo, Williamsburg, etc.), put it in neighborhood, not borough.
- neighborhood: a commercial area or neighborhood name, copied verbatim.
- cuisines: cuisine or category words in lowercase English (italian, ramen, sushi, pizza, chinese, ...).
- price_levels: price levels 1..4 (1 cheapest, 4 most expensive). "$" is 1, "$$" is 2.
  "under $$" / "no more than $$" states an upper bound, so record [1,2].
- min_rating: the minimum rating from 0..5. "4 stars or above" is 4; "4.5+" is 4.5.
- open_now: true only when the user explicitly asks for "open now" / "still open".

[Soft conditions] have no database column and can only be supported by reviews; put them in soft_conditions:
- quiet, ambience, cozy, romantic, date night, comfortable -> ambience
- queue, wait, no wait, fast seating, fast kitchen -> wait
- service, attitude, attentive, good service -> service
- value, good deal, worth it, good value -> value
- delicious, authentic, signature dish, varied menu -> food
- kids, kid friendly, family friendly -> kid_friendly
- good for groups, gathering, team dinner -> group_friendly
Each soft_conditions entry is {"text": the user's own phrase, "topic": one of the topics above}.
Soft conditions must never go into borough / neighborhood / cuisines / price_levels / min_rating / open_now:
those fields are objective facts, and doing so would treat "mentioned in reviews" as "objectively true of the venue".

[Other fields]
- query: copy the user's sentence verbatim; do not rewrite, translate, or summarize it.
- intent: discover (mostly hard conditions) / recommend (mostly soft conditions) / restaurant_qa (asking about one known restaurant) /
  reservation / chit_chat (pure small talk unrelated to restaurants).
- named_restaurants: restaurant names the user named, verbatim; an empty array if none.
- missing_slots: fill only when necessary conditions are genuinely missing and retrieval cannot run; values are borough / cuisine / price_level /
  date / party_size / restaurant_id.
- need_clarification: true only when the user must provide more information before you can continue.

Do not output any explanatory text.`

// extractionSchemaText is the model-facing JSON Schema.
//
// Every property is listed in "required" and nullability is expressed as a type
// union, because that is what a strict-schema provider accepts: a schema with
// optional properties is rejected outright, and a rejected schema would push
// every extraction onto the rule path without anyone noticing.
//
// The topic enum is generated from the shared domain vocabulary rather than
// written out here, so a topic that exists in one place and not the other is
// impossible instead of merely tested for.
var extractionSchema = buildExtractionSchema()

func buildExtractionSchema() json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent": map[string]any{
				"type":        "string",
				"enum":        []string{"discover", "recommend", "restaurant_qa", "reservation", "chit_chat"},
				"description": "intent of this turn",
			},
			"query": map[string]any{
				"type":        []string{"string", "null"},
				"description": "the user's own words, copied verbatim",
			},
			"borough": map[string]any{
				"type":        []string{"string", "null"},
				"enum":        []any{"manhattan", "brooklyn", "queens", "bronx", "staten_island", nil},
				"description": "borough, one of the five only",
			},
			"neighborhood": map[string]any{
				"type":        []string{"string", "null"},
				"description": "commercial area or neighborhood name",
			},
			"cuisines": map[string]any{
				"type":  []string{"array", "null"},
				"items": map[string]any{"type": "string"},
			},
			"price_levels": map[string]any{
				"type":  []string{"array", "null"},
				"items": map[string]any{"type": "integer", "minimum": 1, "maximum": 4},
			},
			"min_rating": map[string]any{
				"type":    []string{"number", "null"},
				"minimum": 0,
				"maximum": 5,
			},
			"open_now": map[string]any{
				"type": []string{"boolean", "null"},
			},
			"named_restaurants": map[string]any{
				"type":  []string{"array", "null"},
				"items": map[string]any{"type": "string"},
			},
			"soft_conditions": map[string]any{
				"type": []string{"array", "null"},
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text": map[string]any{
							"type":        "string",
							"description": "phrase from the user's own words",
						},
						"topic": map[string]any{
							"type": "string",
							"enum": review.CanonicalTopics(),
						},
					},
					"required":             []string{"text", "topic"},
					"additionalProperties": false,
				},
			},
			"missing_slots": map[string]any{
				"type":  []string{"array", "null"},
				"items": map[string]any{"type": "string"},
			},
			"need_clarification": map[string]any{
				"type": "boolean",
			},
		},
		"required": []string{
			"intent", "query", "borough", "neighborhood", "cuisines", "price_levels",
			"min_rating", "open_now", "named_restaurants", "soft_conditions",
			"missing_slots", "need_clarification",
		},
		"additionalProperties": false,
	}
	data, _ := json.Marshal(schema)
	return data
}

// ExtractionSchema exposes the model-facing schema for the offline tests that
// assert it stays valid and stays in step with the topic vocabulary.
func ExtractionSchema() json.RawMessage {
	out := make([]byte, len(extractionSchema))
	copy(out, extractionSchema)
	return out
}

// Instruction returns the extraction prompt, for a caller that wants to render
// or assert it.
func Instruction() string { return extractionInstruction }
