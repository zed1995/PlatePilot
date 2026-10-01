package memory

import (
	"context"
	"strings"
	"sync"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/run"
)

// RunRepository is an in-memory store.RunRepository.
type RunRepository struct {
	mu        sync.RWMutex
	runs      map[string]run.AgentRun
	toolCalls map[string][]run.ToolCallRecord
}

// NewRunRepository returns an empty in-memory run repository.
func NewRunRepository() *RunRepository {
	return &RunRepository{
		runs:      make(map[string]run.AgentRun),
		toolCalls: make(map[string][]run.ToolCallRecord),
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
func (r *RunRepository) RecordToolCall(_ context.Context, call run.ToolCallRecord) error {
	if strings.TrimSpace(call.RunID) == "" {
		return errs.New(errs.CodeInvalidArgument, "run_id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runs[call.RunID]; !ok {
		return errs.Newf(errs.CodeNotFound, "run %q not found", call.RunID)
	}
	r.toolCalls[call.RunID] = append(r.toolCalls[call.RunID], call)
	return nil
}
