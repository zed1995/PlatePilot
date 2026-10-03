package audit

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/run"
)

// fakeRunRepo captures audit writes and can force failures.
type fakeRunRepo struct {
	mu       sync.Mutex
	starts   []run.AgentRun
	finishes []run.AgentRun
	calls    []run.ToolCallRecord

	startErr  error
	finishErr error
	callErr   error
}

func (f *fakeRunRepo) Start(_ context.Context, agentRun run.AgentRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, agentRun)
	return f.startErr
}

func (f *fakeRunRepo) Finish(_ context.Context, agentRun run.AgentRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finishes = append(f.finishes, agentRun)
	return f.finishErr
}

func (f *fakeRunRepo) RecordToolCall(_ context.Context, call run.ToolCallRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.callErr
}

// The read path is not what these tests exercise, but the port requires it:
// a fake that only implements the writes would let a change to the read
// contract slip through as long as nothing called it.
func (f *fakeRunRepo) GetRun(_ context.Context, runID string) (run.AgentRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.finishes) - 1; i >= 0; i-- {
		if f.finishes[i].RunID == runID {
			return f.finishes[i], nil
		}
	}
	for i := len(f.starts) - 1; i >= 0; i-- {
		if f.starts[i].RunID == runID {
			return f.starts[i], nil
		}
	}
	return run.AgentRun{}, errs.ErrNotFound
}

func (f *fakeRunRepo) ListRuns(_ context.Context, threadID string, limit int, _ string) ([]run.AgentRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []run.AgentRun
	for _, record := range f.finishes {
		if record.ThreadID != threadID {
			continue
		}
		out = append(out, record)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeRunRepo) ListToolCalls(_ context.Context, runID string) ([]run.ToolCallRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []run.ToolCallRecord
	for _, call := range f.calls {
		if call.RunID == runID {
			out = append(out, call)
		}
	}
	return out, nil
}

func (f *fakeRunRepo) snapshot() ([]run.AgentRun, []run.AgentRun, []run.ToolCallRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]run.AgentRun(nil), f.starts...),
		append([]run.AgentRun(nil), f.finishes...),
		append([]run.ToolCallRecord(nil), f.calls...)
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRedactArguments(t *testing.T) {
	long := strings.Repeat("辣", maxArgumentChars+10)

	tests := []struct {
		name     string
		tool     string
		raw      string
		wantKeys []string
		banned   []string
	}{
		{
			name:     "search keeps business keys only",
			tool:     tools.SearchRestaurantsToolName,
			raw:      `{"query":"ramen","borough":"manhattan","min_rating":4.2,"open_now":true,"top_k":5,"phone":"+15550000000","address":"1 Secret St"}`,
			wantKeys: []string{"query", "borough", "min_rating", "open_now", "top_k"},
			banned:   []string{"phone", "address"},
		},
		{
			name:     "evidence keeps business keys only",
			tool:     tools.RestaurantEvidenceToolName,
			raw:      `{"restaurant_ids":[1,2],"query":"spice","topic":"menu","doc_types":["menu"],"top_k":3,"email":"a@b.com"}`,
			wantKeys: []string{"restaurant_ids", "query", "topic", "doc_types", "top_k"},
			banned:   []string{"email"},
		},
		{
			name: "unknown tool collapses to empty object",
			tool: "send_sms",
			raw:  `{"to":"+15550000000","body":"hi"}`,
		},
		{
			name: "malformed json collapses to empty object",
			tool: tools.SearchRestaurantsToolName,
			raw:  `{not json`,
		},
		{
			name: "empty arguments collapse to empty object",
			tool: tools.SearchRestaurantsToolName,
			raw:  "",
		},
		{
			name:     "long string value truncated",
			tool:     tools.SearchRestaurantsToolName,
			raw:      `{"query":"` + long + `"}`,
			wantKeys: []string{"query"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactArguments(tt.tool, json.RawMessage(tt.raw))
			var args map[string]any
			if err := json.Unmarshal(got, &args); err != nil {
				t.Fatalf("redacted payload must stay valid json: %v (%s)", err, got)
			}
			for _, key := range tt.wantKeys {
				if _, ok := args[key]; !ok {
					t.Errorf("expected whitelisted key %q in %s", key, got)
				}
			}
			for _, key := range tt.banned {
				if _, ok := args[key]; ok {
					t.Errorf("sensitive key %q must be dropped, got %s", key, got)
				}
			}
			if tt.name == "long string value truncated" {
				query, _ := args["query"].(string)
				wantLen := maxArgumentChars + 1 // 256 kept runes plus the single-ellipsis rune
				if len([]rune(query)) != wantLen {
					t.Errorf("query rune length = %d, want %d", len([]rune(query)), wantLen)
				}
				if !strings.HasSuffix(query, "…") {
					t.Errorf("truncated value must end with ellipsis, got %q", query)
				}
			}
		})
	}
}

func TestRunLifecycleCounters(t *testing.T) {
	repo := &fakeRunRepo{}
	hooks := New(repo, silentLogger(), Options{ModelProvider: "openai-compatible", ModelName: "gpt-test"})
	ctx := context.Background()
	startedAt := time.Now().Add(-2 * time.Second)
	meta := Meta{RunID: "run-1", TraceID: "trace-1", ThreadID: "thread-1", StartedAt: startedAt}

	hooks.RunStart(ctx, meta)
	hooks.ToolCall(ctx, ToolEvent{
		RunID: "run-1", CallID: "call-1", Name: tools.SearchRestaurantsToolName,
		Arguments: json.RawMessage(`{"query":"ramen","phone":"x"}`),
		Status:    "ok", LatencyMS: 12,
	})
	hooks.ToolCall(ctx, ToolEvent{
		RunID: "run-1", CallID: "call-2", Name: tools.RestaurantEvidenceToolName,
		Status: "error", LatencyMS: 30,
	})
	hooks.ToolCall(ctx, ToolEvent{
		RunID: "run-1", CallID: "call-3", Name: "some_other_tool",
		Status: "ok", LatencyMS: 5,
	})
	finishedAt := time.Now()
	hooks.RunFinish(ctx, run.AgentRun{
		RunID: "run-1", Status: run.StatusSucceeded,
		StartedAt: startedAt, FinishedAt: &finishedAt,
		TokenInput: 100, TokenOutput: 50,
	})

	starts, finishes, calls := repo.snapshot()
	if len(starts) != 1 || starts[0].Status != run.StatusRunning {
		t.Fatalf("expected one running start row, got %+v", starts)
	}
	if starts[0].ModelProvider != "openai-compatible" || starts[0].ModelName != "gpt-test" {
		t.Errorf("model identity not propagated: %+v", starts[0])
	}
	if len(calls) != 3 {
		t.Fatalf("expected 3 tool call records, got %d", len(calls))
	}
	if string(calls[0].Arguments) == `{}` || strings.Contains(string(calls[0].Arguments), "phone") {
		t.Errorf("arguments must be redacted but keep query, got %s", calls[0].Arguments)
	}
	if len(finishes) != 1 {
		t.Fatalf("expected one finish row, got %d", len(finishes))
	}
	fin := finishes[0]
	if fin.Status != run.StatusSucceeded || fin.ToolCallCount != 3 || fin.RetrievalCount != 2 {
		t.Errorf("finish row mismatch: %+v", fin)
	}
	if fin.ModelProvider != "openai-compatible" {
		t.Errorf("finish row lost model identity: %+v", fin)
	}

	// Counters are consumed once: a repeated finish (e.g. retried caller)
	// must not double-count.
	hooks.RunFinish(ctx, run.AgentRun{RunID: "run-1", Status: run.StatusSucceeded})
	_, finishes2, _ := repo.snapshot()
	if finishes2[1].ToolCallCount != 0 || finishes2[1].RetrievalCount != 0 {
		t.Errorf("counters must be consumed by the first finish, got %+v", finishes2[1])
	}
}

func TestFailRecordsErrorCode(t *testing.T) {
	repo := &fakeRunRepo{}
	hooks := New(repo, silentLogger(), Options{})
	startedAt := time.Now().Add(-time.Second)
	hooks.RunStart(context.Background(), Meta{RunID: "run-x", StartedAt: startedAt})
	hooks.Fail(context.Background(), Meta{RunID: "run-x", StartedAt: startedAt},
		errs.New(errs.CodeProviderTimeout, "upstream timeout"))

	_, finishes, _ := repo.snapshot()
	if len(finishes) != 1 {
		t.Fatalf("expected one finish row, got %d", len(finishes))
	}
	fin := finishes[0]
	if fin.Status != run.StatusFailed {
		t.Errorf("status = %q, want failed", fin.Status)
	}
	if fin.ErrorCode != string(errs.CodeProviderTimeout) {
		t.Errorf("error code = %q, want provider_timeout", fin.ErrorCode)
	}
	if fin.FinishedAt == nil || fin.LatencyMS < 0 {
		t.Errorf("finished timestamp/latency missing: %+v", fin)
	}
}

func TestRepoErrorsNeverEscape(t *testing.T) {
	boom := errs.New(errs.CodeInternal, "store down")
	repo := &fakeRunRepo{startErr: boom, finishErr: boom, callErr: boom}
	hooks := New(repo, silentLogger(), Options{})

	// A canceled parent context must still be attempted: audit writes
	// detach from the request lifecycle.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	hooks.RunStart(canceled, Meta{RunID: "run-err"})
	hooks.ToolCall(canceled, ToolEvent{RunID: "run-err", CallID: "c", Name: tools.SearchRestaurantsToolName})
	hooks.RunFinish(canceled, run.AgentRun{RunID: "run-err", Status: run.StatusSucceeded})
	hooks.Fail(canceled, Meta{RunID: "run-err"}, boom)

	starts, finishes, calls := repo.snapshot()
	if len(starts) != 1 || len(finishes) != 2 || len(calls) != 1 {
		t.Fatalf("all writes must be attempted despite errors/cancellation: starts=%d finishes=%d calls=%d",
			len(starts), len(finishes), len(calls))
	}
}

func TestNilHooksAndNilRepoSafe(t *testing.T) {
	var nilHooks *Hooks
	ctx := context.Background()
	nilHooks.RunStart(ctx, Meta{})
	nilHooks.ToolCall(ctx, ToolEvent{})
	nilHooks.RunFinish(ctx, run.AgentRun{})
	nilHooks.Fail(ctx, Meta{}, errs.ErrInternal)

	hooks := New(nil, silentLogger(), Options{})
	hooks.RunStart(ctx, Meta{RunID: "r"})
	hooks.ToolCall(ctx, ToolEvent{RunID: "r"})
	hooks.RunFinish(ctx, run.AgentRun{RunID: "r"})
}
