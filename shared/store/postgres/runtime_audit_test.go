package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/run"
	"github.com/zed1995/platepilot/shared/store/postgres"
)

// TestAgentRuntimeIndexStrategy asserts the operational audit queries are
// index-backed: a thread's recent runs and one run's tool calls must not scan
// the whole audit history.
func TestAgentRuntimeIndexStrategy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := postgres.Connect(ctx, postgres.Config{
		DSN:            dsn(t),
		Database:       "platepilot",
		ConnectTimeout: 10 * time.Second,
		Timeout:        30 * time.Second,
	})
	if err != nil {
		t.Skipf("PostgreSQL is not reachable: %v", err)
	}
	t.Cleanup(func() { _ = client.Close(context.WithoutCancel(ctx)) })
	if err := client.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := client.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	runs := postgres.NewRunRepository(client)
	base := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	// Seed enough runs that the planner prefers the btree over a sequential
	// scan; most rows belong to other threads so equality selectivity matters.
	const seeded = 400
	for i := 0; i < seeded; i++ {
		threadID := fmt.Sprintf("seed-thread-%d", i%50)
		if i%50 == 0 {
			threadID = "indexed-thread" // one thread collects eight runs
		}
		agentRun := run.AgentRun{
			TraceID:   fmt.Sprintf("seed-trace-%d", i),
			ThreadID:  threadID,
			RunID:     fmt.Sprintf("seed-run-%d", i),
			Status:    run.StatusSucceeded,
			StartedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := runs.Start(ctx, agentRun); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		if err := runs.Finish(ctx, agentRun); err != nil {
			t.Fatalf("Finish %d: %v", i, err)
		}
	}
	// Seed calls across many runs; the target run stays a small slice of a
	// table large enough that the planner prices the btree below a sequential
	// scan.
	const seededCalls = 4000
	for i := 0; i < seededCalls; i++ {
		runID := fmt.Sprintf("seed-run-%d", (i%(seeded-2))+2)
		if i < 40 {
			runID = "seed-run-1"
		}
		if err := runs.RecordToolCall(ctx, run.ToolCallRecord{
			CallID:    fmt.Sprintf("seed-call-%d", i),
			RunID:     runID,
			ToolName:  "search_restaurants",
			Status:    "ok",
			CreatedAt: base.Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatalf("RecordToolCall %d: %v", i, err)
		}
	}
	if _, err := client.Pool().Exec(ctx, "ANALYZE agent_runs, tool_calls"); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}

	threadPlan := explain(t, ctx, client, `
		SELECT run_id, status, started_at
		FROM agent_runs
		WHERE thread_id = $1
		ORDER BY started_at DESC
		LIMIT 20`, "indexed-thread")
	assertPlanCoversIndex(t, threadPlan, "agent_runs", "agent_runs_thread_started")

	toolPlan := explain(t, ctx, client, `
		SELECT call_id, tool_name
		FROM tool_calls
		WHERE run_id = $1
		ORDER BY created_at
		LIMIT 50`, "seed-run-1")
	assertPlanCoversIndex(t, toolPlan, "tool_calls", "tool_calls_run_created")
}

// assertPlanCoversIndex accepts both a btree "Index Scan using" and a bitmap
// "Bitmap Index Scan on" plan, while still rejecting a sequential scan.
func assertPlanCoversIndex(t *testing.T, plan, table, indexName string) {
	t.Helper()
	used := planIndexNames(plan)
	for _, line := range strings.Split(plan, "\n") {
		if marker := "Scan on "; strings.Contains(line, marker) {
			rest := strings.TrimSpace(line[strings.Index(line, marker)+len(marker):])
			name, _, _ := strings.Cut(rest, " ")
			name = strings.TrimRight(name, "(),")
			if name == indexName {
				used = append(used, name)
			}
		}
	}
	if !contains(used, indexName) {
		t.Errorf("plan does not cover %s; used %v:\n%s", indexName, used, plan)
	}
	if strings.Contains(plan, "Seq Scan on "+table) {
		t.Errorf("plan fell back to a sequential scan on %s:\n%s", table, plan)
	}
}
