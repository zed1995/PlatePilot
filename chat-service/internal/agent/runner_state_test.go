package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"
	"github.com/zed1995/platepilot/shared/store"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
)

func directAnswerProvider(answers ...string) *scriptedProvider {
	p := &scriptedProvider{supportTools: true}
	for _, text := range answers {
		p.completeResps = append(p.completeResps, domainchat.ChatResponse{
			Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: text},
			FinishReason: domainchat.FinishReasonStop,
		})
	}
	return p
}

func newStateRunner(t *testing.T, p *scriptedProvider, deps agent.Deps) *agent.Runner {
	t.Helper()
	deps.Chat = p
	deps.ToolCalling = p
	if deps.Registry == nil {
		deps.Registry = toolreg.New(0)
	}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 3}, deps)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}

func runDirect(t *testing.T, runner *agent.Runner, threadID, userID, input string) *agent.TurnResult {
	t.Helper()
	result, eventsCh := runner.Run(context.Background(), agent.TurnInput{
		ThreadID: threadID, UserID: userID, UserInput: input,
	})
	events := drain(eventsCh)
	if result == nil {
		t.Fatalf("turn %q failed: %+v", input, events)
	}
	return result
}

func TestRunnerPersistsTranscriptCheckpointAndResume(t *testing.T) {
	repo := testkit.NewConversationRepository()
	provider := directAnswerProvider("推荐一兰拉面", "那试试二叶")
	runner := newStateRunner(t, provider, agent.Deps{Conversations: repo})

	runDirect(t, runner, "th-c1", "user-1", "我想吃拉面")

	resumed, err := runner.Resume(context.Background(), "th-c1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.Conversation.ThreadID != "th-c1" || resumed.Conversation.UserID != "user-1" {
		t.Fatalf("conversation = %+v", resumed.Conversation)
	}
	if resumed.Conversation.CurrentState != conversation.StateIdle {
		t.Fatalf("state = %q, want idle", resumed.Conversation.CurrentState)
	}
	if resumed.Checkpoint == nil || resumed.Checkpoint.Version != 1 {
		t.Fatalf("checkpoint = %+v, want version 1", resumed.Checkpoint)
	}
	if len(resumed.Messages) != 2 {
		t.Fatalf("messages = %d, want 2: %+v", len(resumed.Messages), resumed.Messages)
	}
	if resumed.Messages[0].Role != conversation.RoleUser || resumed.Messages[0].Content != "我想吃拉面" {
		t.Fatalf("first message = %+v", resumed.Messages[0])
	}
	if resumed.Messages[1].Role != conversation.RoleAssistant || resumed.Messages[1].Content != "推荐一兰拉面" {
		t.Fatalf("second message = %+v", resumed.Messages[1])
	}
	if resumed.Messages[0].Seq != 1 || resumed.Messages[1].Seq != 2 {
		t.Fatalf("seqs = %d,%d", resumed.Messages[0].Seq, resumed.Messages[1].Seq)
	}

	// Second turn replays history and bumps the checkpoint version.
	runDirect(t, runner, "th-c1", "user-1", "还有别的吗")
	if len(provider.completeReqs) != 2 {
		t.Fatalf("complete requests = %d, want 2", len(provider.completeResps))
	}
	var transcript []string
	for _, msg := range provider.completeReqs[1].Messages {
		transcript = append(transcript, msg.Content)
	}
	joined := strings.Join(transcript, "|")
	if !strings.Contains(joined, "我想吃拉面") || !strings.Contains(joined, "推荐一兰拉面") {
		t.Fatalf("replayed history missing in request transcript: %v", transcript)
	}
	if strings.Contains(joined, "还有别的吗|还有别的吗") {
		t.Fatal("new user message must appear exactly once")
	}
	resumed2, err := runner.Resume(context.Background(), "th-c1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed2.Checkpoint.Version != 2 || len(resumed2.Messages) != 4 {
		t.Fatalf("after second turn: checkpoint=%+v messages=%d", resumed2.Checkpoint, len(resumed2.Messages))
	}
}

func TestRunnerResumeUnknownThreadNotFound(t *testing.T) {
	runner := newStateRunner(t, directAnswerProvider(), agent.Deps{
		Conversations: testkit.NewConversationRepository(),
	})
	if _, err := runner.Resume(context.Background(), "missing"); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("err code = %q, want not_found", errs.CodeOf(err))
	}
}

func TestRunnerConversationIsolationAcrossThreads(t *testing.T) {
	repo := testkit.NewConversationRepository()
	provider := directAnswerProvider("A 线程回答", "B 线程回答", "A 第二轮")
	runner := newStateRunner(t, provider, agent.Deps{Conversations: repo})

	runDirect(t, runner, "th-A", "user-1", "A 的问题")
	runDirect(t, runner, "th-B", "user-1", "B 的问题")
	runDirect(t, runner, "th-A", "user-1", "A 继续")

	// The third request belongs to th-A and must only see A's history.
	var transcript []string
	for _, msg := range provider.completeReqs[2].Messages {
		transcript = append(transcript, msg.Content)
	}
	joined := strings.Join(transcript, "|")
	if !strings.Contains(joined, "A 的问题") || !strings.Contains(joined, "A 线程回答") {
		t.Fatalf("th-A history missing: %v", transcript)
	}
	if strings.Contains(joined, "B 的问题") || strings.Contains(joined, "B 线程回答") {
		t.Fatalf("th-B history leaked into th-A: %v", transcript)
	}
}

// failingConversationRepo wraps the memory repo and fails one method group,
// proving persistence errors degrade with warnings, never the answer.
type failingConversationRepo struct {
	store.ConversationRepository
	failUpsert bool
}

func (f *failingConversationRepo) Upsert(ctx context.Context, conv conversation.Conversation) error {
	if f.failUpsert {
		return errs.ErrInternal
	}
	return f.ConversationRepository.Upsert(ctx, conv)
}

func TestRunnerConversationStoreFailureIsNonFatal(t *testing.T) {
	repo := &failingConversationRepo{
		ConversationRepository: testkit.NewConversationRepository(),
		failUpsert:             true,
	}
	runner := newStateRunner(t, directAnswerProvider("照常回答"), agent.Deps{Conversations: repo})

	result := runDirect(t, runner, "th-broken", "user-1", "你好")
	if result.Answer.Text != "照常回答" {
		t.Fatalf("answer = %+v", result.Answer)
	}
	found := false
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "会话状态保存失败") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected persistence warning, warnings = %v", result.Warnings)
	}
}

func TestRunnerInjectsMemoriesIntoSystemMessages(t *testing.T) {
	memRepo := testkit.NewMemoryRepository()
	ctx := context.Background()
	memories := []domainmemory.Memory{
		{UserID: "u1", Type: domainmemory.MemoryTypeConstraint, Content: "不吃香菜", Confidence: 0.95},
		{UserID: "u1", Type: domainmemory.MemoryTypePreference, Content: "偏好日料", Confidence: 0.8},
		{UserID: "u1", Type: domainmemory.MemoryTypeFact, Content: "住在曼哈顿", Confidence: 0.6},
	}
	for _, mem := range memories {
		if err := memRepo.Upsert(ctx, mem); err != nil {
			t.Fatalf("upsert memory: %v", err)
		}
	}
	provider := directAnswerProvider("收到")
	runner := newStateRunner(t, provider, agent.Deps{Memories: memRepo})

	runDirect(t, runner, "th-mem", "u1", "推荐餐厅")

	if len(provider.completeReqs) != 1 {
		t.Fatalf("complete requests = %d", len(provider.completeReqs))
	}
	msgs := provider.completeReqs[0].Messages
	var injected string
	systemCount := 0
	for _, msg := range msgs {
		if msg.Role == domainchat.RoleSystem {
			systemCount++
			if strings.Contains(msg.Content, "不吃香菜") {
				injected = msg.Content
			}
		}
	}
	if systemCount != 2 {
		t.Fatalf("system messages = %d, want plan prompt + memory segment", systemCount)
	}
	if injected == "" {
		t.Fatalf("memory segment not injected, messages = %+v", msgs)
	}
	for _, want := range []string{"不吃香菜", "偏好日料", "住在曼哈顿", "【约束】", "【偏好】", "【事实】"} {
		if !strings.Contains(injected, want) {
			t.Fatalf("memory segment missing %q:\n%s", want, injected)
		}
	}
}

func TestRunnerMemoryWindowTrimsByConfidence(t *testing.T) {
	memRepo := testkit.NewMemoryRepository()
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		mem := domainmemory.Memory{
			UserID: "u2", Type: domainmemory.MemoryTypePreference,
			Content: "高置信记忆" + string(rune('A'+i)), Confidence: 0.9,
		}
		if err := memRepo.Upsert(ctx, mem); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		time.Sleep(time.Millisecond) // distinct updated_at within the window
	}
	for _, low := range []string{"低置信记忆X", "低置信记忆Y"} {
		if err := memRepo.Upsert(ctx, domainmemory.Memory{
			UserID: "u2", Type: domainmemory.MemoryTypePreference,
			Content: low, Confidence: 0.1,
		}); err != nil {
			t.Fatalf("upsert low: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	provider := directAnswerProvider("ok")
	runner := newStateRunner(t, provider, agent.Deps{Memories: memRepo})

	result := runDirect(t, runner, "th-window", "u2", "推荐")

	var injected string
	for _, msg := range provider.completeReqs[0].Messages {
		if msg.Role == domainchat.RoleSystem && strings.Contains(msg.Content, "长期记忆") {
			injected = msg.Content
		}
	}
	if injected == "" {
		t.Fatal("memory segment missing")
	}
	if strings.Contains(injected, "低置信记忆X") || strings.Contains(injected, "低置信记忆Y") {
		t.Fatalf("low-confidence memories must be trimmed:\n%s", injected)
	}
	if !strings.Contains(injected, "高置信记忆A") || !strings.Contains(injected, "高置信记忆J") {
		t.Fatalf("all ten high-confidence memories expected:\n%s", injected)
	}
	found := false
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "按置信度截断") && strings.Contains(warning, "2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected trim warning for 2 dropped, warnings = %v", result.Warnings)
	}
}

func TestRunnerDeletedAndAnonymousMemoriesNotInjected(t *testing.T) {
	memRepo := testkit.NewMemoryRepository()
	ctx := context.Background()
	mem := domainmemory.Memory{
		ID: "mem-del", UserID: "u3",
		Type: domainmemory.MemoryTypeConstraint, Content: "已删除的忌口", Confidence: 0.9,
	}
	if err := memRepo.Upsert(ctx, mem); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := memRepo.Delete(ctx, "u3", "mem-del"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	provider := directAnswerProvider("ok", "ok-anon")
	runner := newStateRunner(t, provider, agent.Deps{Memories: memRepo})

	runDirect(t, runner, "th-del", "u3", "推荐")
	for _, msg := range provider.completeReqs[0].Messages {
		if strings.Contains(msg.Content, "已删除的忌口") {
			t.Fatalf("soft-deleted memory must not be injected: %+v", msg)
		}
	}

	// Anonymous turns simply skip memory injection.
	runDirect(t, runner, "th-anon", "", "你好")
	systemCount := 0
	for _, msg := range provider.completeReqs[1].Messages {
		if msg.Role == domainchat.RoleSystem {
			systemCount++
		}
	}
	if systemCount != 1 {
		t.Fatalf("anonymous turn must carry only the plan prompt, got %d system messages", systemCount)
	}
}
