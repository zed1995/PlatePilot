package answer

import (
	"fmt"

	"github.com/zed1995/platepilot/shared/domain/evidence"
)

// adequacy is what an evidence set can support. It is a measured fact about the
// bundle, computed here rather than asked of the model: a generation reading one
// paragraph cannot know that the corpus held no second kind to check it
// against, and a caveat the model has to remember is a caveat that will
// sometimes be missing.
type adequacy struct {
	// Documents is how many citable documents the turn has.
	Documents int
	// DocTypes is how many distinct kinds they span.
	DocTypes int
}

// measureAdequacy counts what the evidence set is made of.
func measureAdequacy(items []evidence.Evidence) adequacy {
	return adequacy{
		Documents: len(items),
		DocTypes:  evidence.DocTypesFrom(items),
	}
}

// sufficient reports whether an unqualified answer may be written from this
// evidence.
func (a adequacy) sufficient() bool {
	return a.DocTypes >= evidence.MinAnswerableDocTypes
}

// lead is the sentence the answer has to open with when the evidence is thin.
//
// It is prepended in code rather than requested from the model, for the same
// reason RefusalAnswer is a constant: this is the one line that must not be
// dropped, and a rule that depends on a generation's sentence flow is a rule
// that holds most of the time. It is worded as a statement about this turn's
// evidence — a count — rather than as a general disclaimer, because a sentence
// that reads the same on every answer is a sentence readers learn to skip.
func (a adequacy) lead() string {
	if a.sufficient() {
		return ""
	}
	return fmt.Sprintf(
		"资料不足：本次只找到 %d 条可用资料、%d 类文档（至少需要 %d 类），"+
			"以下结论仅基于这些资料，不能代表该餐厅的整体情况。\n\n",
		a.Documents, a.DocTypes, evidence.MinAnswerableDocTypes)
}

// contextBlock tells the model what the thin bundle means for how strongly it
// may phrase things.
//
// The count is stated so the model can name it, and the prohibition is stated
// separately because "there is little evidence" and "so you may not generalise"
// are different instructions: a model given only the first will mention the
// shortage and then write the generalisation anyway.
func (a adequacy) contextBlock() string {
	if a.sufficient() {
		return ""
	}
	return fmt.Sprintf(
		"<evidence_adequacy>\n本次资料只有 %d 条、%d 类文档，未达到 %d 类的最低要求。"+
			"结论必须限于资料直接写明的内容：不得用单条资料描述「这家店服务很好」"+
			"这类整体判断，不得把一条评论的观感写成这家店的属性。\n</evidence_adequacy>",
		a.Documents, a.DocTypes, evidence.MinAnswerableDocTypes)
}
