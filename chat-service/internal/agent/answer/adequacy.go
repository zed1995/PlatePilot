package answer

import (
	"fmt"

	"github.com/zed1995/platepilot/shared/domain/evidence"
)

// adequacyMode separates the three grounding situations a turn can end in.
//
// The measure used to be one number ("how many document kinds does the
// evidence span"), which treated a rating question the search had already
// answered as unsupported. The mode instead asks what kind of question this
// is: objective facts the candidates settle, opinions only reviews can
// settle, or a mixture whose review half has to be checked.
type adequacyMode int

const (
	// adequacyEvidence is a turn that recalled evidence. The classic
	// doc-type-count rule applies: one document's word is not enough to
	// generalise about a restaurant.
	adequacyEvidence adequacyMode = iota
	// adequacyFactsOnly is a turn with candidates but no evidence and no
	// opinion question: structured facts answer it completely, and a
	// "thin evidence" caveat would be a false alarm.
	adequacyFactsOnly
	// adequacyReviewGap is a turn whose user asked an opinion question but
	// which recalled no reviews. Objective facts may still be answered; the
	// opinion half must be declared unsupported.
	adequacyReviewGap
)

// adequacy is what this turn's material can support. It is a measured fact
// about the bundle, computed here rather than asked of the model: a
// generation reading one paragraph cannot know that the corpus held no
// reviews at all, and a caveat the model has to remember is a caveat that
// will sometimes be missing.
type adequacy struct {
	mode adequacyMode
	// Documents is how many citable documents the turn has (evidence mode).
	Documents int
	// DocTypes is how many distinct kinds they span (evidence mode).
	DocTypes int
}

// measureAdequacy decides the mode from the whole input rather than from the
// evidence slice alone: candidates are the fact source and soft conditions
// are what makes a missing review set matter.
func measureAdequacy(in Input) adequacy {
	if len(in.Evidence) > 0 {
		return adequacy{
			mode:      adequacyEvidence,
			Documents: len(in.Evidence),
			DocTypes:  evidence.DocTypesFrom(in.Evidence),
		}
	}
	if len(in.Candidates) == 0 {
		// Neither facts nor reviews: Compose refuses before generating, so
		// the exact mode is irrelevant.
		return adequacy{}
	}
	if len(in.SoftConditions) > 0 {
		return adequacy{mode: adequacyReviewGap}
	}
	return adequacy{mode: adequacyFactsOnly}
}

// sufficient reports whether an unqualified answer may be written.
func (a adequacy) sufficient() bool {
	switch a.mode {
	case adequacyEvidence:
		return a.DocTypes >= evidence.MinAnswerableDocTypes
	case adequacyFactsOnly:
		return true
	default:
		return false
	}
}

// lead is the sentence the answer has to open with when the material is thin.
//
// It is prepended in code rather than requested from the model, for the same
// reason RefusalAnswer is a constant: this is the one line that must not be
// dropped, and a rule that depends on a generation's sentence flow is a rule
// that holds most of the time. It is worded as a statement about this turn's
// material — a count, or an empty review channel — rather than as a general
// disclaimer, because a sentence that reads the same on every answer is a
// sentence readers learn to skip.
func (a adequacy) lead() string {
	switch a.mode {
	case adequacyEvidence:
		if a.sufficient() {
			return ""
		}
		return fmt.Sprintf(
			"资料不足：本次只找到 %d 条可用资料、%d 类文档（至少需要 %d 类），"+
				"以下结论仅基于这些资料，不能代表该餐厅的整体情况。\n\n",
			a.Documents, a.DocTypes, evidence.MinAnswerableDocTypes)
	case adequacyReviewGap:
		return "评论资料不足：本次没有检索到可用评论，口味、氛围、服务等主观感受类条件" +
			"无法确认；以下客观信息依据商户档案作答。\n\n"
	default:
		return ""
	}
}

// contextBlock tells the model what the thin bundle means for how strongly it
// may phrase things.
//
// The count (or the empty channel) is stated so the model can name it, and
// the prohibition is stated separately because "there is little evidence"
// and "so you may not generalise" are different instructions: a model given
// only the first will mention the shortage and then write the generalisation
// anyway.
func (a adequacy) contextBlock() string {
	switch a.mode {
	case adequacyEvidence:
		if a.sufficient() {
			return ""
		}
		return fmt.Sprintf(
			"<evidence_adequacy>\n本次资料只有 %d 条、%d 类文档，未达到 %d 类的最低要求。"+
				"结论必须限于资料直接写明的内容：不得用单条资料描述「这家店服务很好」"+
				"这类整体判断，不得把一条评论的观感写成这家店的属性。\n</evidence_adequacy>",
			a.Documents, a.DocTypes, evidence.MinAnswerableDocTypes)
	case adequacyReviewGap:
		return "<review_gap>\n本次没有检索到任何评论资料。用户提出的主观感受类条件" +
			"（见 <soft_conditions>）评论侧无法确认，必须逐条列入「无法确认」，" +
			"不得用商户档案事实替代；<candidates> 系统商户档案中的客观事实" +
			"仍可直接陈述。\n</review_gap>"
	default:
		return ""
	}
}
