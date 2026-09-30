package run

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestAgentRunJSONRoundTrip(t *testing.T) {
	started := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Second)
	original := AgentRun{
		TraceID:        "trace-1",
		ThreadID:       "t1",
		RunID:          "run-1",
		Status:         StatusSucceeded,
		ModelProvider:  "openai_compatible",
		ModelName:      "test-model",
		StartedAt:      started,
		FinishedAt:     &finished,
		LatencyMS:      2000,
		TokenInput:     120,
		TokenOutput:    40,
		RetrievalCount: 2,
		ToolCallCount:  3,
	}
	var decoded AgentRun
	data, _ := json.Marshal(original)
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", decoded, original)
	}
}

func TestToolCallRecordJSONRoundTrip(t *testing.T) {
	original := ToolCallRecord{
		CallID:        "call-1",
		RunID:         "run-1",
		ToolName:      "search_restaurants",
		Arguments:     json.RawMessage(`{"top_k":3}`),
		ResultSummary: "3 candidates",
		Status:        "ok",
		LatencyMS:     42,
		CreatedAt:     time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC),
	}
	var decoded ToolCallRecord
	data, _ := json.Marshal(original)
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip mismatch: got %+v want %+v", decoded, original)
	}
}
