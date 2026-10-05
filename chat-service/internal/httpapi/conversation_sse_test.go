package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"

	"github.com/cloudwego/hertz/pkg/common/ut"
)

// ---------------------------------------------------------------------------
// Scripted ChatService
// ---------------------------------------------------------------------------

type fakeChatService struct {
	mu        sync.Mutex
	convs     map[string]conversation.Conversation
	messages  map[string][]conversation.Message
	memories  map[string][]domainmemory.Memory
	deleted   []string
	listCalls []listCall

	// Conversation deletion keeps its own bookkeeping: the memory list already
	// uses `deleted` for "user/id" strings, and letting the two share one slice
	// would make a handler test that asserts on one of them pass because of the
	// other.
	deletedThreads     []string
	deleteThreadCalls  []deleteThreadCall

	sendEvents      []StreamEvent
	sendErr         error
	blockOnCancel   bool
	lastSend        SendMessageInput
	lastConfirm     ConfirmInput
	confirmErr      error
	confirmCalled   int
	lastUpdate      UpdateMemoryInput
	updateCalled    int
	sendStarted     chan struct{}
	cancelObserved  chan struct{}
	getThreadCalled int

	threadOrder    []string
	listThreadCall listThreadCall
	listThreadErr  error
}

// listThreadCall records the scoping a list request reached the service with,
// so a handler test can tell "this user's threads" from "everybody's".
type listThreadCall struct {
	userID   string
	limit    int
	beforeID string
	called   int
}

// deleteThreadCall is the same idea for the delete route: it records who the
// handler said was asking, so a request that arrived without an identity — or
// with one the handler forgot to forward — is visible as a field rather than as
// a 404 the test has to interpret.
type deleteThreadCall struct {
	userID   string
	threadID string
}

var errListThreads = errs.New(errs.CodeInvalidArgument, "list threads refuses an empty user id")

func (f *fakeChatService) ListThreads(_ context.Context, userID string, limit int, beforeID string) (ThreadPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listThreadCall = listThreadCall{userID: userID, limit: limit, beforeID: beforeID, called: f.listThreadCall.called + 1}
	if strings.TrimSpace(userID) == "" {
		return ThreadPage{}, errListThreads
	}
	return ThreadPage{Conversations: f.convsFor(userID)}, nil
}

// convsFor is the scripted read: one user's threads, newest first. It applies
// the ordering the repository is specified to return, with thread id as the
// tie-breaker, so a handler test can assert on order instead of on insertion
// sequence — which is what makes the "newest first" claim mean anything.
func (f *fakeChatService) convsFor(userID string) []conversation.Conversation {
	out := make([]conversation.Conversation, 0, len(f.threadOrder))
	for _, threadID := range f.threadOrder {
		conv, ok := f.convs[threadID]
		if !ok || conv.UserID != userID {
			continue
		}
		out = append(out, conv)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ThreadID < out[j].ThreadID
	})
	return out
}

func (f *fakeChatService) ListCandidates(_ context.Context, threadID string) (CandidatePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if threadID == "missing-thread" {
		return CandidatePage{}, errs.Newf(errs.CodeNotFound, "thread %q not found", threadID)
	}
	if threadID == "thread-empty-candidates" {
		return CandidatePage{Candidates: []conversation.Candidate{}}, nil
	}
	if threadID == "thread-nil-candidates" {
		return CandidatePage{}, nil
	}
	if threadID == "thread-no-snapshot" {
		// A candidate the store never dated. It is not a special case in the
		// product — only in this fixture, which needs one row without a
		// snapshot to prove the field is left out rather than sent as year 1.
		return CandidatePage{Candidates: []conversation.Candidate{
			{ThreadID: threadID, Position: 1, RestaurantID: 11, Name: "A Ramen", Score: 0.91},
		}}, nil
	}
	return CandidatePage{Candidates: []conversation.Candidate{
		{
			ThreadID: threadID, Position: 1, RestaurantID: 11, Name: "A Ramen", Score: 0.91,
			Reasons:    []string{"评论推断：安静（ambience）"},
			SnapshotAt: candidateSnapshotAt,
		},
		{
			ThreadID: threadID, Position: 2, RestaurantID: 22, Name: "B Ramen", Score: 0.83,
			Reasons:    []string{"硬条件命中：菜系=ramen"},
			SnapshotAt: candidateSnapshotAt,
		},
	}}, nil
}

// candidateSnapshotAt is the fixed observation date the candidate fixture
// carries, so a test asserting on it does not depend on when it runs.
var candidateSnapshotAt = time.Date(2021, 9, 1, 12, 0, 0, 0, time.UTC)

type listCall struct {
	threadID string
	limit    int
	beforeID string
}

func newFakeChatService() *fakeChatService {
	return &fakeChatService{
		convs:          make(map[string]conversation.Conversation),
		messages:       make(map[string][]conversation.Message),
		memories:       make(map[string][]domainmemory.Memory),
		sendStarted:    make(chan struct{}, 1),
		cancelObserved: make(chan struct{}, 1),
	}
}

func (f *fakeChatService) CreateThread(_ context.Context, userID, title string) (conversation.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	conv := conversation.Conversation{
		ThreadID:     "thread-" + fmt.Sprint(len(f.convs)+1),
		UserID:       userID,
		Title:        title,
		CurrentState: conversation.StateIdle,
		CreatedAt:    now, UpdatedAt: now,
	}
	f.convs[conv.ThreadID] = conv
	return conv, nil
}

func (f *fakeChatService) GetThread(_ context.Context, threadID string) (ThreadDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getThreadCalled++
	conv, ok := f.convs[threadID]
	if !ok {
		return ThreadDetail{}, errs.Newf(errs.CodeNotFound, "thread %q not found", threadID)
	}
	return ThreadDetail{Conversation: conv}, nil
}

func (f *fakeChatService) ListMessages(_ context.Context, threadID string, limit int, beforeID string) (MessagePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls = append(f.listCalls, listCall{threadID, limit, beforeID})
	return MessagePage{Messages: f.messages[threadID]}, nil
}

func (f *fakeChatService) SendMessage(ctx context.Context, in SendMessageInput, emit func(StreamEvent) error) error {
	f.mu.Lock()
	f.lastSend = in
	block := f.blockOnCancel
	events := f.sendEvents
	errToReturn := f.sendErr
	f.mu.Unlock()

	select {
	case f.sendStarted <- struct{}{}:
	default:
	}
	if block {
		<-ctx.Done()
		select {
		case f.cancelObserved <- struct{}{}:
		default:
		}
		return ctx.Err()
	}
	for _, ev := range events {
		if err := emit(ev); err != nil {
			return err
		}
	}
	return errToReturn
}

func (f *fakeChatService) ListMemories(_ context.Context, userID string) (MemoryPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	views := make([]MemoryView, 0, len(f.memories[userID]))
	for _, mem := range f.memories[userID] {
		views = append(views, MemoryView{
			ID: mem.ID, Type: string(mem.Type), Content: mem.Content,
			Source: mem.Source, Confidence: mem.Confidence,
			CreatedAt: mem.CreatedAt, UpdatedAt: mem.UpdatedAt,
		})
	}
	return MemoryPage{Memories: views}, nil
}

// DeleteThread carries the real ownership rule rather than a scripted answer:
// the one thing a handler test cannot check for itself is whether the route
// forwards the caller's identity, and a fake that removed whatever id it was
// given would let a handler that dropped the user id pass.
func (f *fakeChatService) DeleteThread(_ context.Context, userID, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteThreadCalls = append(f.deleteThreadCalls, deleteThreadCall{userID: userID, threadID: threadID})
	conv, ok := f.convs[threadID]
	if !ok || conv.UserID != userID {
		return errs.Newf(errs.CodeNotFound, "thread %q not found", threadID)
	}
	delete(f.convs, threadID)
	f.deletedThreads = append(f.deletedThreads, threadID)
	return nil
}

func (f *fakeChatService) DeleteMemory(_ context.Context, userID, memoryID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if memoryID == "missing" {
		return errs.Newf(errs.CodeNotFound, "memory %q not found", memoryID)
	}
	f.deleted = append(f.deleted, userID+"/"+memoryID)
	return nil
}

// UpdateMemory is scripted: the endpoint's job is to bind a partial body and
// scope it to the caller, and the edit rules themselves are tested where they
// live.
func (f *fakeChatService) UpdateMemory(_ context.Context, in UpdateMemoryInput) (MemoryView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastUpdate = in
	f.updateCalled++
	if in.MemoryID == "missing" || in.MemoryID == "someone-elses" {
		return MemoryView{}, errs.Newf(errs.CodeNotFound, "memory %q not found", in.MemoryID)
	}
	view := MemoryView{ID: in.MemoryID, Type: "preference", Content: "不吃辣", Confidence: 0.9}
	if in.Content != nil {
		view.Content = *in.Content
	}
	if in.Type != nil {
		view.Type = string(*in.Type)
	}
	return view, nil
}

// ConfirmAction is scripted rather than implemented: the decision endpoint's
// job is to bind and validate a body and pass the decision on, and the decision
// itself is tested where it lives. The stub records what reached it so a
// handler test can tell a routable request from one that never arrived.
func (f *fakeChatService) ConfirmAction(_ context.Context, in ConfirmInput) (ConfirmResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastConfirm = in
	f.confirmCalled++
	if f.confirmErr != nil {
		return ConfirmResult{}, f.confirmErr
	}
	return ConfirmResult{
		ThreadID:      in.ThreadID,
		Decision:      in.Decision,
		PendingAction: "request_reservation",
		State:         "completed",
		Output:        json.RawMessage(`{"reservation_id":"res-1","status":"confirmed"}`),
		Summary:       "确认预约：Joe's Pizza\n时间：2026-10-10 19:00\n人数：2 人",
		Message:       "预约已确认。",
	}, nil
}

// ---------------------------------------------------------------------------
// Real-TCP test server
// ---------------------------------------------------------------------------

func startChatServer(t *testing.T, svc ChatService) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	h := NewRouter(Config{Addr: addr, Version: "test", Logger: quietLogger(), Chat: svc})
	go func() { _ = h.Run() }()
	waitForPort(t, addr)
	t.Cleanup(func() {
		// http.DefaultClient keeps idle keep-alive connections in its pool, and
		// Hertz's graceful shutdown counts them as active, so it would block
		// until the deadline below. Close them first so Shutdown returns promptly.
		http.DefaultClient.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
	})
	return "http://" + addr
}

func waitForPort(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never came up on %s", addr)
}

// ---------------------------------------------------------------------------
// SSE frame parser
// ---------------------------------------------------------------------------

type sseFrame struct {
	Event string
	Data  string
}

func readSSEFrames(t *testing.T, body io.Reader) []sseFrame {
	t.Helper()
	var frames []sseFrame
	var current sseFrame
	flush := func() {
		if current.Event != "" || current.Data != "" {
			frames = append(frames, current)
		}
		current = sseFrame{}
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1<<16), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / heartbeat
		}
		if strings.HasPrefix(line, "event: ") {
			current.Event = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			if current.Data != "" {
				current.Data += "\n"
			}
			current.Data += strings.TrimPrefix(line, "data: ")
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan sse body: %v", err)
	}
	flush()
	return frames
}

func decodeFrameData(t *testing.T, f sseFrame, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(f.Data), target); err != nil {
		t.Fatalf("decode %s frame data %q: %v", f.Event, f.Data, err)
	}
}

func chatRequest(t *testing.T, method, url string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, raw
}

// ---------------------------------------------------------------------------
// Thread + history API
// ---------------------------------------------------------------------------

func TestCreateGetThreadAndEmptyHistory(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodPost, base+"/v1/conversations",
		map[string]string{"title": "周五晚餐"},
		map[string]string{"X-User-ID": "user-7"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", resp.StatusCode, raw)
	}
	var created threadResponse
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode created thread: %v", err)
	}
	if created.ThreadID == "" || created.CurrentState != "idle" || created.Title != "周五晚餐" {
		t.Fatalf("created thread = %+v", created)
	}
	if got := resp.Header.Get("X-Request-ID"); got == "" {
		t.Fatal("response must carry a request id")
	}

	resp2, raw2 := chatRequest(t, http.MethodGet, base+"/v1/conversations/"+created.ThreadID,
		nil, map[string]string{"X-User-ID": "user-7"})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, body %s", resp2.StatusCode, raw2)
	}
	var fetched threadResponse
	if err := json.Unmarshal(raw2, &fetched); err != nil {
		t.Fatalf("decode fetched thread: %v", err)
	}
	if fetched.ThreadID != created.ThreadID || fetched.Checkpoint != nil {
		t.Fatalf("fetched thread = %+v", fetched)
	}

	resp3, raw3 := chatRequest(t, http.MethodGet, base+"/v1/conversations/"+created.ThreadID+"/messages",
		nil, nil)
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("messages status = %d, body %s", resp3.StatusCode, raw3)
	}
	if string(raw3) != `{"messages":[]}` {
		t.Fatalf("empty history must be an empty array, got %s", raw3)
	}
}

func TestThreadEndpoints404(t *testing.T) {
	base := startChatServer(t, newFakeChatService())

	resp, raw := chatRequest(t, http.MethodGet, base+"/v1/conversations/nope", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := decodeError(t, raw).Error.Code; got != "not_found" {
		t.Fatalf("code = %q, want not_found", got)
	}

	// Posting into a missing thread is a pre-stream JSON 404, not an SSE
	// error frame.
	resp2, raw2 := chatRequest(t, http.MethodPost, base+"/v1/conversations/nope/messages",
		map[string]string{"content": "hi"}, nil)
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("post missing thread status = %d, body %s", resp2.StatusCode, raw2)
	}
	if ct := resp2.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("pre-stream error must be JSON, got %q", ct)
	}
}

func TestHistoryPaginationLimitClamped(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)
	chatRequest(t, http.MethodPost, base+"/v1/conversations", nil, nil)
	threadID := fake.mustThreadID(t)

	cases := []struct {
		query string
		want  int
	}{
		{"", defaultHistoryPageSize},
		{"?limit=5", 5},
		{"?limit=9999", maxHistoryPageSize},
	}
	for _, tc := range cases {
		chatRequest(t, http.MethodGet, base+"/v1/conversations/"+threadID+"/messages"+tc.query, nil, nil)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.listCalls) != len(cases) {
		t.Fatalf("list calls = %d", len(fake.listCalls))
	}
	for i, tc := range cases {
		if fake.listCalls[i].limit != tc.want {
			t.Fatalf("case %q: limit = %d, want %d", tc.query, fake.listCalls[i].limit, tc.want)
		}
	}
}

func TestHistoryBadLimitRejected(t *testing.T) {
	base := startChatServer(t, newFakeChatService())
	resp, _ := chatRequest(t, http.MethodGet,
		base+"/v1/conversations/t1/messages?limit=abc", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func (f *fakeChatService) mustThreadID(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.convs) != 1 {
		t.Fatalf("expected one seeded thread, got %d", len(f.convs))
	}
	for id := range f.convs {
		return id
	}
	return ""
}

// ---------------------------------------------------------------------------
// SSE turn endpoint
// ---------------------------------------------------------------------------

func TestSendMessageSSEEventSequence(t *testing.T) {
	fake := newFakeChatService()
	fake.sendEvents = []StreamEvent{
		{Type: StreamStart, RunID: "run-1", ThreadID: "th-1"},
		{Type: StreamToolStart, CallID: "call-1", Tool: "search_restaurants"},
		{Type: StreamToolFinish, CallID: "call-1", ToolStatus: "succeeded", LatencyMS: 83},
		{Type: StreamDelta, Delta: "为你推荐一兰拉面"},
		{Type: StreamCitation, EvidenceIDs: []int64{101, 104}},
		{
			Type: StreamEnd, FinishReason: "stop",
			Usage:    &chat.TokenUsage{InputTokens: 120, OutputTokens: 40, TotalTokens: 160},
			Warnings: []string{"低置信度结果已降级"},
		},
	}
	base := startChatServer(t, fake)
	threadID := seedThread(t, base)

	req, _ := http.NewRequest(http.MethodPost, base+"/v1/conversations/"+threadID+"/messages",
		bytes.NewReader([]byte(`{"content":"  曼哈顿中城日料  "}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-User-ID", "user-1")
	req.Header.Set("X-Trace-ID", "trace-fixed-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post message: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	frames := readSSEFrames(t, resp.Body)
	wantTypes := []string{
		"message.start", "tool.start", "tool.finish",
		"message.delta", "citation", "message.end",
	}
	if len(frames) != len(wantTypes) {
		t.Fatalf("frames = %d, want %d: %+v", len(frames), len(wantTypes), frames)
	}
	for i, want := range wantTypes {
		if frames[i].Event != want {
			t.Fatalf("frame %d = %q, want %q (all: %+v)", i, frames[i].Event, want, frames)
		}
	}
	var start map[string]string
	decodeFrameData(t, frames[0], &start)
	if start["run_id"] != "run-1" || start["thread_id"] != "th-1" {
		t.Fatalf("start data = %s", frames[0].Data)
	}
	var finish map[string]any
	decodeFrameData(t, frames[2], &finish)
	if finish["status"] != "succeeded" || finish["latency_ms"].(float64) != 83 {
		t.Fatalf("tool.finish data = %s", frames[2].Data)
	}
	var citation map[string][]int64
	decodeFrameData(t, frames[4], &citation)
	if len(citation["evidence_ids"]) != 2 || citation["evidence_ids"][0] != 101 {
		t.Fatalf("citation data = %s", frames[4].Data)
	}
	var end struct {
		FinishReason string `json:"finish_reason"`
		Usage        struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	decodeFrameData(t, frames[5], &end)
	if end.FinishReason != "stop" || end.Usage.InputTokens != 120 || end.Usage.TotalTokens != 160 {
		t.Fatalf("end data = %s", frames[5].Data)
	}

	fake.mu.Lock()
	last := fake.lastSend
	fake.mu.Unlock()
	if last.ThreadID != threadID || last.UserID != "user-1" ||
		last.TraceID != "trace-fixed-1" || last.Content != "曼哈顿中城日料" {
		t.Fatalf("service input = %+v", last)
	}
}

func TestSendMessageRejectsInvalidContent(t *testing.T) {
	base := startChatServer(t, newFakeChatService())
	threadID := seedThread(t, base)

	for _, body := range []string{`{"content":""}`, `{"content":"   "}`, `{"content":"` + strings.Repeat("长", maxMessageRunes+1) + `"}`} {
		req, _ := http.NewRequest(http.MethodPost, base+"/v1/conversations/"+threadID+"/messages",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", body, resp.StatusCode)
		}
		if got := decodeError(t, raw).Error.Code; got != "invalid_argument" {
			t.Fatalf("code = %q", got)
		}
	}
}

func TestSendMessageRuntimeErrorEvent(t *testing.T) {
	fake := newFakeChatService()
	fake.sendEvents = []StreamEvent{{
		Type: StreamError, Code: string(errs.CodeProviderTimeout), Message: "upstream timed out",
	}}
	base := startChatServer(t, fake)
	threadID := seedThread(t, base)

	resp, raw := postSSE(t, base+"/v1/conversations/"+threadID+"/messages", `{"content":"hi"}`)
	frames := readSSEFrames(t, bytes.NewReader(raw))
	if resp.StatusCode != http.StatusOK || len(frames) != 1 || frames[0].Event != "error" {
		t.Fatalf("status=%d frames=%+v", resp.StatusCode, frames)
	}
	var data map[string]string
	decodeFrameData(t, frames[0], &data)
	if data["code"] != "provider_timeout" || data["message"] != "upstream timed out" {
		t.Fatalf("error frame = %s", frames[0].Data)
	}
}

func TestSendMessageFallbackErrorFrame(t *testing.T) {
	fake := newFakeChatService()
	fake.sendErr = errs.New(errs.CodeProviderUnavailable, "model down")
	base := startChatServer(t, fake)
	threadID := seedThread(t, base)

	resp, raw := postSSE(t, base+"/v1/conversations/"+threadID+"/messages", `{"content":"hi"}`)
	frames := readSSEFrames(t, bytes.NewReader(raw))
	if resp.StatusCode != http.StatusOK || len(frames) != 1 {
		t.Fatalf("status=%d frames=%+v", resp.StatusCode, frames)
	}
	var data map[string]string
	decodeFrameData(t, frames[0], &data)
	// The in-stream frame mirrors the canonical JSON envelope, whose message
	// renders as "code: prose".
	if data["code"] != "provider_unavailable" || data["message"] != "provider_unavailable: model down" {
		t.Fatalf("fallback error frame = %s", frames[0].Data)
	}
}

func TestSendMessageInternalErrorFrameHidesDetails(t *testing.T) {
	fake := newFakeChatService()
	fake.sendErr = errs.New(errs.CodeInternal, "db password hunter2 exploded")
	base := startChatServer(t, fake)
	threadID := seedThread(t, base)

	resp, raw := postSSE(t, base+"/v1/conversations/"+threadID+"/messages", `{"content":"hi"}`)
	frames := readSSEFrames(t, bytes.NewReader(raw))
	if resp.StatusCode != http.StatusOK || len(frames) != 1 {
		t.Fatalf("status=%d frames=%+v", resp.StatusCode, frames)
	}
	var data map[string]string
	decodeFrameData(t, frames[0], &data)
	if data["code"] != "internal" || data["message"] != "internal server error" {
		t.Fatalf("internal error frame = %s", frames[0].Data)
	}
	if strings.Contains(frames[0].Data, "hunter2") {
		t.Fatal("internal error details leaked into the SSE frame")
	}
}

func TestClientDisconnectCancelsRun(t *testing.T) {
	fake := newFakeChatService()
	fake.blockOnCancel = true
	base := startChatServer(t, fake)
	threadID := seedThread(t, base)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/v1/conversations/"+threadID+"/messages", strings.NewReader(`{"content":"hi"}`))
	req.Header.Set("Content-Type", "application/json")

	done := make(chan struct{})
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		close(done)
	}()

	select {
	case <-fake.sendStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("service was never invoked")
	}
	cancel()
	select {
	case <-fake.cancelObserved:
	case <-time.After(3 * time.Second):
		t.Fatal("runner context was not canceled when the client disconnected")
	}
	<-done
}

// ---------------------------------------------------------------------------
// Memories API
// ---------------------------------------------------------------------------

func TestMemoriesListAndDelete(t *testing.T) {
	fake := newFakeChatService()
	fake.memories["user-1"] = []domainmemory.Memory{
		{ID: "m-1", Type: domainmemory.MemoryTypeConstraint, Content: "不吃香菜", Confidence: 0.9},
		{ID: "m-2", Type: domainmemory.MemoryTypePreference, Content: "偏好日料", Confidence: 0.6},
	}
	base := startChatServer(t, fake)

	// Identity is mandatory.
	resp, _ := chatRequest(t, http.MethodGet, base+"/v1/memories", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("anonymous list status = %d, want 400", resp.StatusCode)
	}

	resp2, raw2 := chatRequest(t, http.MethodGet, base+"/v1/memories", nil,
		map[string]string{"X-User-ID": "user-1"})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, body %s", resp2.StatusCode, raw2)
	}
	var listed struct {
		Memories []MemoryView `json:"memories"`
	}
	if err := json.Unmarshal(raw2, &listed); err != nil {
		t.Fatalf("decode memories: %v", err)
	}
	if len(listed.Memories) != 2 || listed.Memories[0].Content != "不吃香菜" {
		t.Fatalf("memories = %+v", listed.Memories)
	}
	if strings.Contains(string(raw2), "embedding") {
		t.Fatal("memory list must not expose embeddings")
	}

	resp3, _ := chatRequest(t, http.MethodDelete, base+"/v1/memories/m-1", nil,
		map[string]string{"X-User-ID": "user-1"})
	if resp3.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", resp3.StatusCode)
	}

	resp4, _ := chatRequest(t, http.MethodDelete, base+"/v1/memories/missing", nil,
		map[string]string{"X-User-ID": "user-1"})
	if resp4.StatusCode != http.StatusNotFound {
		t.Fatalf("delete missing status = %d, want 404", resp4.StatusCode)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.deleted) != 1 || fake.deleted[0] != "user-1/m-1" {
		t.Fatalf("deleted calls = %v", fake.deleted)
	}
}

func TestCORSPreflightOnSSERouteAllowsUserHeader(t *testing.T) {
	h := NewRouter(Config{
		Addr:             ":0",
		Version:          "test",
		Logger:           quietLogger(),
		CORSAllowOrigins: []string{"http://allowed.example"},
		Chat:             newFakeChatService(),
	})
	w := ut.PerformRequest(h.Engine, http.MethodOptions, "/v1/conversations/t1/messages", nil,
		ut.Header{Key: "Origin", Value: "http://allowed.example"},
		ut.Header{Key: "Access-Control-Request-Method", Value: "POST"},
		ut.Header{Key: "Access-Control-Request-Headers", Value: "x-user-id, content-type"},
	)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(got), "x-user-id") {
		t.Fatalf("preflight must allow X-User-ID, got %q", got)
	}
}

func TestChatRoutesAbsentWithoutService(t *testing.T) {
	// Without a Chat service the conversation surface must not be registered
	// at all; the engine can be exercised in-process.
	h := NewRouter(Config{Addr: "127.0.0.1:0", Version: "test", Logger: quietLogger()})
	w := ut.PerformRequest(h.Engine, http.MethodGet, "/v1/conversations/t1", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (chat routes must be unregistered)", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func seedThread(t *testing.T, base string) string {
	t.Helper()
	resp, raw := chatRequest(t, http.MethodPost, base+"/v1/conversations", nil, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed thread status = %d, body %s", resp.StatusCode, raw)
	}
	var created threadResponse
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ThreadID
}

func postSSE(t *testing.T, url, body string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post sse: %v", err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read sse: %v", err)
	}
	return resp, raw
}
