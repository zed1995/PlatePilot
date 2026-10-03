package postgres

import (
	"context"
	"encoding/json"
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

// runColumns is the projection every read of agent_runs shares.
const runColumns = `
	run_id, trace_id, thread_id, status, model_provider, model_name,
	started_at, finished_at, latency_ms, token_input, token_output,
	retrieval_count, tool_call_count, error_code`

// scanRun reads one agent_runs row from a scanner.
func scanRun(scan func(dest ...any) error) (run.AgentRun, error) {
	var agentRun run.AgentRun
	if err := scan(&agentRun.RunID, &agentRun.TraceID, &agentRun.ThreadID, &agentRun.Status,
		&agentRun.ModelProvider, &agentRun.ModelName, &agentRun.StartedAt, &agentRun.FinishedAt,
		&agentRun.LatencyMS, &agentRun.TokenInput, &agentRun.TokenOutput,
		&agentRun.RetrievalCount, &agentRun.ToolCallCount, &agentRun.ErrorCode); err != nil {
		return run.AgentRun{}, err
	}
	return agentRun, nil
}

// GetRun returns one run by id.
func (r *RunRepository) GetRun(ctx context.Context, runID string) (run.AgentRun, error) {
	if strings.TrimSpace(runID) == "" {
		return run.AgentRun{}, errs.New(errs.CodeInvalidArgument, "run_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	agentRun, err := scanRun(r.client.pool.QueryRow(ctx,
		`SELECT `+runColumns+` FROM agent_runs WHERE run_id = $1`, runID).Scan)
	if err != nil {
		if pgErrNoRows(err) {
			return run.AgentRun{}, errs.Newf(errs.CodeNotFound, "run %q not found", runID)
		}
		return run.AgentRun{}, operationError("postgres: get run", err)
	}
	return agentRun, nil
}

// ListRuns returns a thread's runs, newest first, bounded by limit and the
// optional before_id cursor.
func (r *RunRepository) ListRuns(ctx context.Context, threadID string, limit int, beforeID string) ([]run.AgentRun, error) {
	if strings.TrimSpace(threadID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	var limitArg any
	if limit > 0 {
		limitArg = limit
	}
	rows, err := r.client.pool.Query(ctx, `
		SELECT `+runColumns+`
		FROM agent_runs
		WHERE thread_id = $1
		  AND ($2 = '' OR (started_at, run_id) < (
		      SELECT started_at, run_id FROM agent_runs WHERE run_id = $2))
		ORDER BY started_at DESC, run_id DESC
		LIMIT $3`,
		threadID, beforeID, limitArg)
	if err != nil {
		return nil, operationError("postgres: list runs", err)
	}
	defer rows.Close()

	out := make([]run.AgentRun, 0)
	for rows.Next() {
		agentRun, err := scanRun(rows.Scan)
		if err != nil {
			return nil, operationError("postgres: scan run", err)
		}
		out = append(out, agentRun)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate runs", err)
	}
	return out, nil
}

// ListToolCalls returns one run's tool calls in invocation order. An unknown
// run yields an empty slice, not an error.
func (r *RunRepository) ListToolCalls(ctx context.Context, runID string) ([]run.ToolCallRecord, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "run_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	rows, err := r.client.pool.Query(ctx, `
		SELECT call_id, run_id, tool_name, arguments, result_summary,
		       status, latency_ms, created_at
		FROM tool_calls
		WHERE run_id = $1
		ORDER BY created_at ASC, call_id ASC`, runID)
	if err != nil {
		return nil, operationError("postgres: list tool calls", err)
	}
	defer rows.Close()

	out := make([]run.ToolCallRecord, 0)
	for rows.Next() {
		var (
			record    run.ToolCallRecord
			arguments []byte
		)
		if err := rows.Scan(&record.CallID, &record.RunID, &record.ToolName, &arguments,
			&record.ResultSummary, &record.Status, &record.LatencyMS, &record.CreatedAt); err != nil {
			return nil, operationError("postgres: scan tool call", err)
		}
		if len(arguments) > 0 {
			record.Arguments = json.RawMessage(arguments)
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate tool calls", err)
	}
	return out, nil
}
