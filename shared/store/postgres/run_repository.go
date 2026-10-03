package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/run"
	"github.com/zed1995/platepilot/shared/store"
)

var _ store.RunRepository = (*RunRepository)(nil)

// RunRepository is the PostgreSQL store.RunRepository.
//
// Start is an idempotent upsert so a retry after a lost acknowledgement cannot
// duplicate a run; Finish and RecordToolCall require an existing run, matching
// the in-memory implementation's not_found contract.
type RunRepository struct {
	client *Client
}

// NewRunRepository builds a run repository on an existing client.
func NewRunRepository(client *Client) *RunRepository {
	return &RunRepository{client: client}
}

// Start upserts a run in its initial state.
func (r *RunRepository) Start(ctx context.Context, agentRun run.AgentRun) error {
	if strings.TrimSpace(agentRun.RunID) == "" {
		return errs.New(errs.CodeInvalidArgument, "run_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	_, err := r.client.pool.Exec(ctx, `
		INSERT INTO agent_runs (
			run_id, trace_id, thread_id, status,
			model_provider, model_name, started_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (run_id) DO UPDATE SET
			trace_id       = EXCLUDED.trace_id,
			thread_id      = EXCLUDED.thread_id,
			status         = EXCLUDED.status,
			model_provider = EXCLUDED.model_provider,
			model_name     = EXCLUDED.model_name,
			started_at     = EXCLUDED.started_at,
			finished_at    = NULL,
			latency_ms     = 0,
			token_input    = 0,
			token_output   = 0,
			retrieval_count = 0,
			tool_call_count = 0,
			error_code     = ''`,
		agentRun.RunID, agentRun.TraceID, agentRun.ThreadID, string(agentRun.Status),
		agentRun.ModelProvider, agentRun.ModelName, agentRun.StartedAt)
	if err != nil {
		return operationError("postgres: start agent run", err)
	}
	return nil
}

// Finish writes the terminal state of a previously started run.
func (r *RunRepository) Finish(ctx context.Context, agentRun run.AgentRun) error {
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	tag, err := r.client.pool.Exec(ctx, `
		UPDATE agent_runs SET
			status          = $2,
			model_provider  = $3,
			model_name      = $4,
			finished_at     = $5,
			latency_ms      = $6,
			token_input     = $7,
			token_output    = $8,
			retrieval_count = $9,
			tool_call_count = $10,
			error_code      = $11
		WHERE run_id = $1`,
		agentRun.RunID, string(agentRun.Status),
		agentRun.ModelProvider, agentRun.ModelName,
		agentRun.FinishedAt, agentRun.LatencyMS,
		agentRun.TokenInput, agentRun.TokenOutput,
		agentRun.RetrievalCount, agentRun.ToolCallCount, agentRun.ErrorCode)
	if err != nil {
		return operationError("postgres: finish agent run", err)
	}
	if tag.RowsAffected() == 0 {
		return errs.Newf(errs.CodeNotFound, "run %q not found", agentRun.RunID)
	}
	return nil
}

// RecordToolCall appends one tool invocation audit row. The arguments must
// already be redacted by the caller.
func (r *RunRepository) RecordToolCall(ctx context.Context, call run.ToolCallRecord) error {
	if strings.TrimSpace(call.RunID) == "" {
		return errs.New(errs.CodeInvalidArgument, "run_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	arguments := call.Arguments
	if len(arguments) == 0 {
		arguments = []byte(`{}`)
	}
	_, err := r.client.pool.Exec(ctx, `
		INSERT INTO tool_calls (
			call_id, run_id, tool_name, arguments,
			result_summary, status, latency_ms, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		call.CallID, call.RunID, call.ToolName, arguments,
		call.ResultSummary, call.Status, call.LatencyMS, call.CreatedAt)
	if err != nil {
		// A tool call cannot exist without its run. The driver reports this
		// as a foreign-key violation; map it onto the same not_found code the
		// in-memory implementation returns so both satisfy one contract.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return errs.Newf(errs.CodeNotFound, "run %q not found", call.RunID)
		}
		return operationError("postgres: record tool call", err)
	}
	return nil
}
