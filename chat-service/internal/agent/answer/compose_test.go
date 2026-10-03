package answer

import (
	"context"
	"strings"
	"testing"

	chatport "github.com/zed/platepilot/shared/chat"
	domainchat "github.com/zed/platepilot/shared/domain/chat"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
)

// scriptedChat answers Complete from a queued script; it records every request
// so tests can assert on corrective messages and call counts.
type scriptedChat struct {
	contents []string
	err      error
	requests []domainchat.ChatRequest
}

func (s *scriptedChat) Complete(_ context.Context, req domainchat.ChatRequest) (domainchat.ChatResponse, error) {
	s.requests = append(s.requests, req)
	if s.err != nil {
		return domainchat.ChatResponse{}, s.err
	}
	content := s.contents[0]
	s.contents = s.contents[1:]
	return domainchat.ChatResponse{
		Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: content},
		FinishReason: domainchat.FinishReasonStop,
	}, nil
}

func (s *scriptedChat) Stream(context.Context, domainchat.ChatRequest) (chatport.ChatStream, error) {
	return nil, nil
}

func ev(id int64) evidence.Evidence {
	return evidence.Evidence{EvidenceID: id, RestaurantID: id + 1000, Content: "资料正文", DocType: evidence.DocTypeRestaurantProfile}
}

func TestComposeRequiresQuestion(t *testing.T) {
	c := NewComposer(Deps{Chat: &scriptedChat{}})
	_, err := c.Compose(context.Background(), Input{Evidence: []evidence.Evidence{ev(1)}})
	if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

func TestComposeWithoutEvidenceReturnsFixedRefusalWithoutModel(t *testing.T) {
	chat := &scriptedChat{}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	ans, err := c.Compose(context.Background(), Input{Question: "哪家好？"})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != RefusalAnswer {
		t.Fatalf("text = %q, want fixed refusal", ans.Text)
	}
	if len(chat.requests) != 0 {
		t.Fatalf("Complete called %d times, want 0", len(chat.requests))
	}
}

func TestComposeValidAnswerParsesCitationsAndFollowUps(t *testing.T) {
	chat := &scriptedChat{contents: []string{
		"结论：A 更好[^10][^20]。细节如此。\nFOLLOWUPS: [\"能订位吗?\",\"人均多少?\"]",
	}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	ans, err := c.Compose(context.Background(), Input{
		Question: "哪家好？",
		Evidence: []evidence.Evidence{ev(10), ev(20)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "结论：A 更好[^10][^20]。细节如此。"; ans.Text != want {
		t.Fatalf("text = %q, want %q", ans.Text, want)
	}
	if got := citationIDs(ans); !equalInt64(got, []int64{10, 20}) {
		t.Fatalf("citations = %v, want [10 20]", got)
	}
	if len(ans.FollowUps) != 2 || ans.FollowUps[0] != "能订位吗?" {
		t.Fatalf("followUps = %v", ans.FollowUps)
	}
	if len(chat.requests) != 1 {
		t.Fatalf("Complete called %d times, want 1", len(chat.requests))
	}
	// Evidence must be carried into the grounding context.
	userContent := chat.requests[0].Messages[1].Content
	if !strings.Contains(userContent, "<evidence id=\"10\"") {
		t.Fatalf("evidence block missing from prompt:\n%s", userContent)
	}
}

func TestComposeDeduplicatesAndSortsCitations(t *testing.T) {
	chat := &scriptedChat{contents: []string{"正文[^20][^10][^20]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat})
	ans, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(10), ev(20)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := citationIDs(ans); !equalInt64(got, []int64{10, 20}) {
		t.Fatalf("citations = %v, want [10 20]", got)
	}
}

func TestComposeRetriesOnceAfterOutOfRangeCitation(t *testing.T) {
	chat := &scriptedChat{contents: []string{
		"第一次回答[^99]\nFOLLOWUPS: []",
		"修正回答[^10]\nFOLLOWUPS: []",
	}}
	c := NewComposer(Deps{Chat: chat})
	ans, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(10)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "修正回答[^10]" {
		t.Fatalf("text = %q", ans.Text)
	}
	if len(chat.requests) != 2 {
		t.Fatalf("Complete called %d times, want 2", len(chat.requests))
	}
	last := chat.requests[1].Messages[len(chat.requests[1].Messages)-1]
	if last.Role != domainchat.RoleUser {
		t.Fatalf("correction role = %s", last.Role)
	}
	if !strings.Contains(last.Content, "99") || !strings.Contains(last.Content, "10") {
		t.Fatalf("correction message must name invalid and allowed IDs:\n%s", last.Content)
	}
}

func TestComposeFailsAfterSecondViolation(t *testing.T) {
	chat := &scriptedChat{contents: []string{
		"第一次[^99]\nFOLLOWUPS: []",
		"第二次还是[^99]\nFOLLOWUPS: []",
	}}
	c := NewComposer(Deps{Chat: chat})
	_, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(10)},
	})
	if errs.CodeOf(err) != errs.CodeAgentCitationViolation {
		t.Fatalf("err = %v, want agent_citation_violation", err)
	}
	if len(chat.requests) != 2 {
		t.Fatalf("Complete called %d times, want 2", len(chat.requests))
	}
}

func TestComposePropagatesModelError(t *testing.T) {
	modelErr := errs.New(errs.CodeProviderUnavailable, "down")
	c := NewComposer(Deps{Chat: &scriptedChat{err: modelErr}})
	if _, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(1)},
	}); errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("err = %v, want provider_unavailable", err)
	}
}

func TestFollowUpsClampedToThree(t *testing.T) {
	chat := &scriptedChat{contents: []string{
		"正文[^10]\nFOLLOWUPS: [\"a\",\"b\",\"c\",\"d\",\"e\"]",
	}}
	c := NewComposer(Deps{Chat: chat})
	ans, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(10)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.FollowUps) != 3 {
		t.Fatalf("followUps = %v, want at most 3", ans.FollowUps)
	}
}

func TestFollowUpsMalformedJSONTolerated(t *testing.T) {
	chat := &scriptedChat{contents: []string{"正文[^10]\nFOLLOWUPS: 不是数组"}}
	c := NewComposer(Deps{Chat: chat})
	ans, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(10)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ans.FollowUps != nil {
		t.Fatalf("followUps = %v, want nil", ans.FollowUps)
	}
	if ans.Text != "正文[^10]" {
		t.Fatalf("text = %q", ans.Text)
	}
}

func TestNoFollowUpsLine(t *testing.T) {
	chat := &scriptedChat{contents: []string{"只有正文[^10]"}}
	c := NewComposer(Deps{Chat: chat})
	ans, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(10)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "只有正文[^10]" || ans.FollowUps != nil {
		t.Fatalf("ans = %+v", ans)
	}
}

func citationIDs(a domainchat.Answer) []int64 { return a.Citations }

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
