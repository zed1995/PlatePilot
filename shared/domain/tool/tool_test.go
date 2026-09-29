package tool

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zed/platepilot/shared/domain/errs"
)

func TestToolResultJSONRoundTrip(t *testing.T) {
	original := ToolResult{
		CallID:  "call-1",
		Name:    "get_restaurant_evidence",
		Status:  ToolStatusError,
		Content: "upstream unavailable",
		Data:    json.RawMessage(`{"retryable":true}`),
		Error:   errs.New(errs.CodeProviderUnavailable, "provider unavailable"),
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ToolResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", decoded, original)
	}
}

func TestToolSpecJSONRoundTrip(t *testing.T) {
	original := ToolSpec{
		Name:        "search_restaurants",
		Description: "Search restaurants",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"top_k":{"type":"integer"}}}`),
		ReadOnly:    true,
		TimeoutMS:   1500,
	}
	var decoded ToolSpec
	data, _ := json.Marshal(original)
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip mismatch: got %+v want %+v", decoded, original)
	}
}
