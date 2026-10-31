package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/zed1995/platepilot/chat-service/internal/config"
)

func validChatConfig() config.ChatConfig {
	return config.ChatConfig{
		Provider:              providerOpenAICompatible,
		BaseURL:               "https://openrouter.ai/api/v1",
		APIKey:                "test-key",
		Model:                 "vendor/test-model",
		Timeout:               60 * time.Second,
		MaxRetries:            2,
		SupportsTools:         true,
		SupportsParallelTools: false,
		SupportsJSONSchema:    false,
		ContextTokens:         32768,
	}
}

// capturedChatRequest is the slice of an OpenAI-compatible request the model
// routing assertions need: which model id the job asked for, and what response
// format the extraction model fixed.
type capturedChatRequest struct {
	Model          string `json:"model"`
	Stream         bool   `json:"stream"`
	ResponseFormat *struct {
		Type       string `json:"type"`
		JSONSchema *struct {
			Name string `json:"name"`
		} `json:"json_schema"`
	} `json:"response_format"`
}

func TestBuildChatModelsUsesTheConfiguredModelPerJob(t *testing.T) {
	var (
		mu       sync.Mutex
		captured []capturedChatRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req capturedChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		captured = append(captured, req)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","created":0,`+
			`"model":"test","choices":[{"index":0,"message":{"role":"assistant",`+
			`"content":"{\"intent\":\"discover\",\"query\":\"ok\"}"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	cfg := validChatConfig()
	cfg.BaseURL = server.URL
	cfg.PlanModel = "vendor/planner"
	cfg.AnswerModel = "vendor/writer"
	cfg.ExtractModel = "vendor/reader"

	plan, answer, extract, err := buildChatModels(cfg)
	if err != nil {
		t.Fatalf("buildChatModels: %v", err)
	}

	jobs := []struct {
		name  string
		model model.BaseChatModel
		want  string
	}{
		{"plan", plan, "vendor/planner"},
		{"answer", answer, "vendor/writer"},
		{"extract", extract, "vendor/reader"},
	}
	for _, job := range jobs {
		if _, err := job.model.Generate(context.Background(),
			[]*schema.Message{schema.UserMessage("你好")}); err != nil {
			t.Fatalf("%s model generate: %v", job.name, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != len(jobs) {
		t.Fatalf("captured %d requests, want %d", len(captured), len(jobs))
	}
	for i, job := range jobs {
		if got := captured[i].Model; got != job.want {
			t.Errorf("%s model sent %q, want %q", job.name, got, job.want)
		}
	}

	// The extraction model is the one built with a response format; the other
	// two must not force one, or every planning call would be constrained to
	// JSON.
	if captured[0].ResponseFormat != nil || captured[1].ResponseFormat != nil {
		t.Fatalf("plan/answer must not carry a response format: %+v %+v",
			captured[0].ResponseFormat, captured[1].ResponseFormat)
	}
	if rf := captured[2].ResponseFormat; rf == nil || rf.Type != "json_object" {
		t.Fatalf("extract response format = %+v, want json_object", rf)
	}
}

func TestBuildChatModelsFixesTheExtractionSchemaWhenSupported(t *testing.T) {
	var (
		mu      sync.Mutex
		lastReq capturedChatRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req capturedChatRequest
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		lastReq = req
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","created":0,`+
			`"model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"{}"},`+
			`"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	cfg := validChatConfig()
	cfg.BaseURL = server.URL
	cfg.SupportsJSONSchema = true
	cfg.PlanModel = "vendor/planner"
	cfg.AnswerModel = "vendor/writer"
	cfg.ExtractModel = "vendor/reader"

	_, _, extract, err := buildChatModels(cfg)
	if err != nil {
		t.Fatalf("buildChatModels: %v", err)
	}
	if _, err := extract.Generate(context.Background(),
		[]*schema.Message{schema.UserMessage("你好")}); err != nil {
		t.Fatalf("extract model generate: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	rf := lastReq.ResponseFormat
	if rf == nil || rf.Type != "json_schema" {
		t.Fatalf("extract response format = %+v, want json_schema", rf)
	}
	if rf.JSONSchema == nil || rf.JSONSchema.Name != "slot_extraction" {
		t.Fatalf("json schema = %+v, want the slot extraction schema", rf.JSONSchema)
	}
}

func TestBuildChatModelsRejectsUnknownProvider(t *testing.T) {
	cfg := validChatConfig()
	cfg.Provider = "some-other-vendor"
	_, _, _, err := buildChatModels(cfg)
	if err == nil || !strings.Contains(err.Error(), "CHAT_PROVIDER") {
		t.Fatalf("want CHAT_PROVIDER error, got %v", err)
	}
}
