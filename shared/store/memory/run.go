package memory

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/run"
)

// RunRepository is an in-memory store.RunRepository.
type RunRepository struct {
	mu        sync.RWMutex
	runs      map[string]run.AgentRun
	toolCalls map[string][]run.ToolCallRecord
	nodes     map[string][]run.RunNode
}

// NewRunRepository returns an empty in-memory run repository.
func NewRunRepository() *RunRepository {
	return &RunRepository{
		runs:      make(map[string]run.AgentRun),
		toolCalls: make(map[string][]run.ToolCallRecord),
		nodes:     make(map[string][]run.RunNode),
	}
}

// Start records a newly started run.
func (r *RunRepository) Start(_ context.Context, agentRun run.AgentRun) error {
	if strings.TrimSpace(agentRun.RunID) == "" {
		return errs.New(errs.CodeInvalidArgument, "run_id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[agentRun.RunID] = agentRun
	return nil
}

// Finish updates a stored run with terminal state.
func (r *RunRepository) Finish(_ context.Context, agentRun run.AgentRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runs[agentRun.RunID]; !ok {
		return errs.Newf(errs.CodeNotFound, "run %q not found", agentRun.RunID)
	}
	r.runs[agentRun.RunID] = agentRun
	return nil
}

// RecordToolCall appends a tool call audit record to its run.
//
// The start is normalised on the way in for the same reason the other stores
// stamp their own timestamps: the column is required, and a caller that did not
// observe the start should read back the same derived value it would get from
// PostgreSQL rather than a zero time that no other adapter can store.
func (r *RunRepository) RecordToolCall(_ context.Context, call run.ToolCallRecord) error {
	if strings.TrimSpace(call.RunID) == "" {
		return errs.New(errs.CodeInvalidArgument, "run_id is required")
	}
	// The read path orders by this column, so a row without a position sorts
	// silently into the wrong place. PostgreSQL refuses it through the table's
	// check constraint; refusing it here is what keeps the two adapters
	// interchangeable rather than merely close.
	if call.Seq <= 0 {
		return errs.New(errs.CodeInvalidArgument, "seq must be positive")
	}
	call.StartedAt = call.Start()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runs[call.RunID]; !ok {
		return errs.Newf(errs.CodeNotFound, "run %q not found", call.RunID)
	}
	r.toolCalls[call.RunID] = append(r.toolCalls[call.RunID], call)
	return nil
}

// RecordNode appends one graph-node span to its run.
func (r *RunRepository) RecordNode(_ context.Context, node run.RunNode) error {
	if strings.TrimSpace(node.RunID) == "" {
		return errs.New(errs.CodeInvalidArgument, "run_id is required")
	}
	if strings.TrimSpace(node.NodeID) == "" {
		return errs.New(errs.CodeInvalidArgument, "node_id is required")
	}
	// Same rule as RecordToolCall, and for the same reason: the read path
	// orders by this column, and PostgreSQL refuses a span without a position
	// through the table's check constraint.
	if node.Seq <= 0 {
		return errs.New(errs.CodeInvalidArgument, "seq must be positive")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runs[node.RunID]; !ok {
		return errs.Newf(errs.CodeNotFound, "run %q not found", node.RunID)
	}
	r.nodes[node.RunID] = append(r.nodes[node.RunID], node)
	return nil
}

// DeleteByThread removes every run of a thread along with its tool calls and
// node spans.
//
// The three maps are keyed by run id, so the thread has to be established from
// the run row before its children can be found. Nothing is returned for a
// thread that has no runs: this is the cleanup half of deleting a conversation,
// not a lookup whose miss means something.
func (r *RunRepository) DeleteByThread(_ context.Context, threadID string) error {
	if strings.TrimSpace(threadID) == "" {
		return errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for runID, agentRun := range r.runs {
		if agentRun.ThreadID != threadID {
			continue
		}
		delete(r.runs, runID)
		delete(r.toolCalls, runID)
		delete(r.nodes, runID)
	}
	return nil
}

// GetRun returns one run or a not_found error.
func (r *RunRepository) GetRun(_ context.Context, runID string) (run.AgentRun, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agentRun, ok := r.runs[runID]
	if !ok {
		return run.AgentRun{}, errs.Newf(errs.CodeNotFound, "run %q not found", runID)
	}
	return agentRun, nil
}

// GetRunByTrace returns one run by trace id or a not_found error.
func (r *RunRepository) GetRunByTrace(_ context.Context, traceID string) (run.AgentRun, error) {
	if strings.TrimSpace(traceID) == "" {
		return run.AgentRun{}, errs.New(errs.CodeInvalidArgument, "trace_id is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, agentRun := range r.runs {
		if agentRun.TraceID == traceID {
			return agentRun, nil
		}
	}
	return run.AgentRun{}, errs.Newf(errs.CodeNotFound, "run for trace %q not found", traceID)
}

// ListRuns returns a thread's runs, newest first, bounded by limit and the
// optional before_id cursor.
//
// The order is (started_at, run_id) descending. Two runs of the same thread can
// share a started_at — a retry within the same millisecond — and a tie broken
// by map iteration would make paging return a different set each call, so the
// run id is the deterministic tiebreaker.
func (r *RunRepository) ListRuns(_ context.Context, threadID string, limit int, beforeID string) ([]run.AgentRun, error) {
	if strings.TrimSpace(threadID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]run.AgentRun, 0, len(r.runs))
	for _, agentRun := range r.runs {
		if agentRun.ThreadID == threadID {
			all = append(all, agentRun)
		}
	}
	sort.Slice(all, func(i, j int) bool { return agentRunNewer(all[i], all[j]) })

	if beforeID != "" {
		cut := -1
		for i, agentRun := range all {
			if agentRun.RunID == beforeID {
				cut = i
				break
			}
		}
		if cut < 0 {
			// An unknown cursor pages from nothing rather than from the head; a
			// stale id must not resurrect runs the caller already saw.
			return []run.AgentRun{}, nil
		}
		all = all[cut+1:]
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// ListToolCalls returns one run's tool calls in invocation order. An unknown
// run yields an empty slice, not an error.
//
// Insert order is completion order — a parallel round writes each row when its
// call returns — so the sort is what makes the read answer the question the
// caller asked, which is what the model asked for in the first place. seq is
// the key rather than started_at for the reason the column exists: the calls of
// one round start microseconds apart, and a sort on a wall clock would leave
// the order to the tiebreak.
func (r *RunRepository) ListToolCalls(_ context.Context, runID string) ([]run.ToolCallRecord, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "run_id is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	calls := r.toolCalls[runID]
	out := make([]run.ToolCallRecord, len(calls))
	copy(out, calls)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// ListNodes returns one run's node spans in execution order. An unknown run
// yields an empty slice, not an error.
func (r *RunRepository) ListNodes(_ context.Context, runID string) ([]run.RunNode, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "run_id is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	nodes := r.nodes[runID]
	out := make([]run.RunNode, len(nodes))
	copy(out, nodes)
	// Ordered by seq rather than by arrival: the span carries its position
	// because two nodes of a fast run start in the same millisecond, and an
	// order derived from insertion happens to be right only until it is not.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// agentRunNewer reports whether a should sort before b (both newest-first).
func agentRunNewer(a, b run.AgentRun) bool {
	if !a.StartedAt.Equal(b.StartedAt) {
		return a.StartedAt.After(b.StartedAt)
	}
	return a.RunID > b.RunID
}
