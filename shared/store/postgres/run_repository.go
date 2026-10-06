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
			error_code     = '',
			error_message  = ''`,
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
			error_code      = $11,
			error_message   = $12
		WHERE run_id = $1`,
		agentRun.RunID, string(agentRun.Status),
		agentRun.ModelProvider, agentRun.ModelName,
		agentRun.FinishedAt, agentRun.LatencyMS,
		agentRun.TokenInput, agentRun.TokenOutput,
		agentRun.RetrievalCount, agentRun.ToolCallCount, agentRun.ErrorCode,
		agentRun.ErrorMessage)
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
	// Position zero is not "unset" here the way it is for a duration: the read
	// path orders by this column, so a row without a position sorts silently
	// into the wrong place. The table's check constraint refuses it too; this
	// is here so the in-memory adapter refuses it with the same code rather
	// than accepting a row PostgreSQL would not store.
	if call.Seq <= 0 {
		return errs.New(errs.CodeInvalidArgument, "seq must be positive")
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
			result_summary, status, seq, latency_ms, started_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		call.CallID, call.RunID, call.ToolName, arguments,
		call.ResultSummary, call.Status, call.Seq, call.LatencyMS, call.Start(), call.CreatedAt)
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

// RecordNode appends one graph-node span to its run.
//
// A span cannot exist without its run, and the foreign key is allowed to say
// so rather than being pre-checked: the same not_found code the in-memory
// implementation returns is mapped from the driver's violation, so both
// adapters satisfy one contract.
func (r *RunRepository) RecordNode(ctx context.Context, node run.RunNode) error {
	if strings.TrimSpace(node.RunID) == "" {
		return errs.New(errs.CodeInvalidArgument, "run_id is required")
	}
	if strings.TrimSpace(node.NodeID) == "" {
		return errs.New(errs.CodeInvalidArgument, "node_id is required")
	}
	// The table's check constraint refuses a span without a position, because
	// the read path orders by it. Refusing it here as well is what keeps the
	// in-memory adapter from accepting a row PostgreSQL would not store.
	if node.Seq <= 0 {
		return errs.New(errs.CodeInvalidArgument, "seq must be positive")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	detail := node.Detail
	if len(detail) == 0 {
		detail = []byte(`{}`)
	}
	_, err := r.client.pool.Exec(ctx, `
		INSERT INTO run_nodes (
			node_id, run_id, trace_id, node, seq, status,
			started_at, latency_ms, detail, error_code
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		node.NodeID, node.RunID, node.TraceID, node.Node, node.Seq,
		string(node.Status), node.StartedAt, node.LatencyMS, detail, node.ErrorCode)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return errs.Newf(errs.CodeNotFound, "run %q not found", node.RunID)
		}
		return operationError("postgres: record run node", err)
	}
	return nil
}

// nodeColumns is the projection every read of run_nodes shares.
const nodeColumns = `
	node_id, run_id, trace_id, node, seq, status,
	started_at, latency_ms, detail, error_code`

// ListNodes returns one run's node spans in execution order. An unknown run
// yields an empty slice, not an error.
func (r *RunRepository) ListNodes(ctx context.Context, runID string) ([]run.RunNode, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "run_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	rows, err := r.client.pool.Query(ctx, `
		SELECT `+nodeColumns+`
		FROM run_nodes
		WHERE run_id = $1
		ORDER BY seq ASC`, runID)
	if err != nil {
		return nil, operationError("postgres: list run nodes", err)
	}
	defer rows.Close()

	out := make([]run.RunNode, 0)
	for rows.Next() {
		var (
			node        run.RunNode
			detailBytes []byte
		)
		if err := rows.Scan(&node.NodeID, &node.RunID, &node.TraceID, &node.Node,
			&node.Seq, &node.Status, &node.StartedAt, &node.LatencyMS,
			&detailBytes, &node.ErrorCode); err != nil {
			return nil, operationError("postgres: scan run node", err)
		}
		if len(detailBytes) > 0 {
			node.Detail = json.RawMessage(detailBytes)
		}
		out = append(out, node)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate run nodes", err)
	}
	return out, nil
}

// DeleteByThread removes every run recorded for a thread.
//
// tool_calls and run_nodes both carry a cascading foreign key onto agent_runs,
// so one statement takes the whole audit of the thread and the database is what
// guarantees nothing is left pointing at a run that no longer exists. A thread
// with no runs is not an error: this is the cleanup half of deleting a
// conversation, and a miss here means there was nothing to clean.
func (r *RunRepository) DeleteByThread(ctx context.Context, threadID string) error {
	if strings.TrimSpace(threadID) == "" {
		return errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	if _, err := r.client.pool.Exec(ctx,
		`DELETE FROM agent_runs WHERE thread_id = $1`, threadID); err != nil {
		return operationError("postgres: delete runs for thread", err)
	}
	return nil
}

// runColumns is the projection every read of agent_runs shares.
const runColumns = `
	run_id, trace_id, thread_id, status, model_provider, model_name,
	started_at, finished_at, latency_ms, token_input, token_output,
	retrieval_count, tool_call_count, error_code, error_message`

// scanRun reads one agent_runs row from a scanner.
func scanRun(scan func(dest ...any) error) (run.AgentRun, error) {
	var agentRun run.AgentRun
	if err := scan(&agentRun.RunID, &agentRun.TraceID, &agentRun.ThreadID, &agentRun.Status,
		&agentRun.ModelProvider, &agentRun.ModelName, &agentRun.StartedAt, &agentRun.FinishedAt,
		&agentRun.LatencyMS, &agentRun.TokenInput, &agentRun.TokenOutput,
		&agentRun.RetrievalCount, &agentRun.ToolCallCount, &agentRun.ErrorCode,
		&agentRun.ErrorMessage); err != nil {
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

// GetRunByTrace returns one run by trace id.
//
// trace_id carries a unique constraint, so this is a single-row read rather
// than a search: a trace names exactly one run, and anything else would mean
// the identifier a client is given to report a problem with is ambiguous.
func (r *RunRepository) GetRunByTrace(ctx context.Context, traceID string) (run.AgentRun, error) {
	if strings.TrimSpace(traceID) == "" {
		return run.AgentRun{}, errs.New(errs.CodeInvalidArgument, "trace_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	agentRun, err := scanRun(r.client.pool.QueryRow(ctx,
		`SELECT `+runColumns+` FROM agent_runs WHERE trace_id = $1`, traceID).Scan)
	if err != nil {
		if pgErrNoRows(err) {
			return run.AgentRun{}, errs.Newf(errs.CodeNotFound, "run for trace %q not found", traceID)
		}
		return run.AgentRun{}, operationError("postgres: get run by trace", err)
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
//
// The order is the caller's request order, which is what seq records. Neither
// timestamp can stand in for it any more: rows arrive in completion order
// because a round may run several read-only tools at once, and while started_at
// does say when each call began, two calls of one round are launched
// microseconds apart and a wall clock stored at microsecond resolution
// routinely cannot separate them. seq is assigned in request order by the
// caller that owns the round, so it needs no tiebreak and no clock.
func (r *RunRepository) ListToolCalls(ctx context.Context, runID string) ([]run.ToolCallRecord, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "run_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	rows, err := r.client.pool.Query(ctx, `
		SELECT call_id, run_id, tool_name, arguments, result_summary,
		       status, seq, latency_ms, started_at, created_at
		FROM tool_calls
		WHERE run_id = $1
		ORDER BY seq ASC`, runID)
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
			&record.ResultSummary, &record.Status, &record.Seq, &record.LatencyMS,
			&record.StartedAt, &record.CreatedAt); err != nil {
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
