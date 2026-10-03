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
const extractionInstruction = `你是 PlatePilot 的意图与槽位抽取器。把用户这一轮的餐厅需求抽取成结构化条件，只输出 JSON。

【硬条件】可以在数据库中确定性过滤，放进对应字段：
- borough：只能是 manhattan / brooklyn / queens / bronx / staten_island 之一。
  用户说商圈（中城、下城、SoHo、Williamsburg 等）时，放进 neighborhood，不要放进 borough。
- neighborhood：商圈或街区名，原样填写。
- cuisines：菜系或品类，用英文小写单词（italian、ramen、sushi、pizza、chinese…）。
- price_levels：价格档，取值 1..4（1 最便宜，4 最贵）。"$" 记 1，"$$" 记 2。
  用户说 "under $$" / "不超过 $$" 时表示上限，记 [1,2]。
- min_rating：0..5 的最低评分。"4 星以上" 记 4；"4.5+" 记 4.5。
- open_now：仅当用户明确要求"现在营业 / 还开着"时为 true。

【软条件】数据库里没有对应字段，只能由评论支撑，放进 soft_conditions：
- 安静、quiet、氛围、cozy、浪漫、romantic、适合约会、date night、有情调、舒适 → ambience
- 排队、等位、no wait、上菜快、出餐快 → wait
- 服务、态度、good service、贴心 → service
- 性价比、划算、物有所值、good value → value
- 好吃、delicious、正宗、招牌菜、菜品丰富 → food
- 带孩子、亲子、kid friendly、family friendly → kid_friendly
- 适合聚会、聚餐、团建、good for groups → group_friendly
soft_conditions 的每一项都是 {"text": 用户原话片段, "topic": 上面列出的主题之一}。
软条件绝对不能写进 borough / neighborhood / cuisines / price_levels / min_rating / open_now：
这些字段是客观事实，写了就会把"评论里提到"当成"店里确定如此"。

【其他字段】
- query：把用户这一句原话原样复制，不要改写、不要翻译、不要概括。
- intent：discover（硬条件为主）/ recommend（软条件为主）/ restaurant_qa（问某家已知餐厅）/
  reservation（预约）/ chit_chat（纯寒暄，和餐厅无关）。
- named_restaurants：用户点名的餐厅名称，原样填写；没有就留空数组。
- missing_slots：确实缺少必要条件、无法检索时才填，取值 borough / cuisine / price_level /
  date / party_size / restaurant_id。
- need_clarification：只有在必须让用户补充信息才能继续时才为 true。

不要输出任何解释文字。`

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
				"description": "本轮的意图",
			},
			"query": map[string]any{
				"type":        []string{"string", "null"},
				"description": "用户原话，原样复制",
			},
			"borough": map[string]any{
				"type":        []string{"string", "null"},
				"enum":        []any{"manhattan", "brooklyn", "queens", "bronx", "staten_island", nil},
				"description": "行政区，只能是五个之一",
			},
			"neighborhood": map[string]any{
				"type":        []string{"string", "null"},
				"description": "商圈或街区名",
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
							"description": "用户原话片段",
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
