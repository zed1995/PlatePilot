package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/chat/openai"
	sharedcfg "github.com/zed/platepilot/shared/config"
	"github.com/zed/platepilot/shared/domain/chat"
	"github.com/zed/platepilot/shared/domain/tool"
)

// loadRepoDotEnv makes `go test` behave like the application: it loads the
// repository-root .env (the one next to go.mod). The test working directory is
// this package's directory, not the repo root, so the file is located relative
// to the source file instead. Real environment variables always win, so the
// documented invocation with inline vars still works.
func loadRepoDotEnv(t *testing.T) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if err := sharedcfg.LoadDotEnv(filepath.Join(dir, ".env")); err != nil {
				t.Logf("load %s: %v", filepath.Join(dir, ".env"), err)
			}
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// Smoke tests hit a real OpenAI-compatible endpoint and stay skipped unless
// explicitly enabled:
//
//	# after filling CHAT_BASE_URL/CHAT_API_KEY/CHAT_MODEL into the repo-root .env
//	PLATEPILOT_TEST_CHAT=1 go test ./shared/chat/openai/ -run Smoke -count=1 -v
//
// Inline environment variables work too and take precedence:
//
//	PLATEPILOT_TEST_CHAT=1 CHAT_BASE_URL=... CHAT_API_KEY=... CHAT_MODEL=... \
//	  go test -run Smoke -count=1 -v
func smokeClient(t *testing.T) (*openai.Client, bool) {
	t.Helper()
	loadRepoDotEnv(t)
	if os.Getenv("PLATEPILOT_TEST_CHAT") != "1" {
		t.Skip("set PLATEPILOT_TEST_CHAT=1 and CHAT_BASE_URL/CHAT_API_KEY/CHAT_MODEL (in .env or inline) to run")
	}
	baseURL := os.Getenv("CHAT_BASE_URL")
	apiKey := os.Getenv("CHAT_API_KEY")
	model := os.Getenv("CHAT_MODEL")
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("CHAT_BASE_URL, CHAT_API_KEY and CHAT_MODEL are all required for smoke tests")
	}

	caps := openai.DefaultCapabilities()
	// Optional capability overrides so the smoke run matches the model actually
	// configured in .env, without editing code.
	if v := os.Getenv("CHAT_SUPPORTS_PARALLEL_TOOLS"); v == "true" {
		caps.ParallelTools = true
	}
	if v := os.Getenv("CHAT_SUPPORTS_JSON_SCHEMA"); v == "true" {
		caps.JSONSchema = true
	}
	if v := os.Getenv("CHAT_SUPPORTS_TOOLS"); v == "false" {
		caps.Tools = false
	}

	c, err := openai.New(openai.Options{
		BaseURL:      baseURL,
		APIKey:       apiKey,
		Model:        model,
		Capabilities: caps,
		Timeout:      60 * time.Second,
		MaxRetries:   openai.DefaultMaxRetries,
	})
	if err != nil {
		t.Fatalf("openai.New: %v", err)
	}
	return c, true
}

func TestSmokeComplete(t *testing.T) {
	c, ok := smokeClient(t)
	if !ok {
		return
	}
	resp, err := c.Complete(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{
			{Role: chat.RoleSystem, Content: "Reply with exactly: pong"},
			{Role: chat.RoleUser, Content: "ping"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Message.Content == "" {
		t.Errorf("empty content: %+v", resp)
	}
	t.Logf("complete response: %q usage=%+v", resp.Message.Content, resp.Usage)
}

func TestSmokeToolCalls(t *testing.T) {
	c, ok := smokeClient(t)
	if !ok {
		return
	}
	params := json.RawMessage(`{
	  "type": "object",
	  "properties": {"city": {"type": "string"}},
	  "required": ["city"]
	}`)
	resp, err := c.ChatWithTools(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{
			{Role: chat.RoleSystem, Content: "Use get_weather for weather questions."},
			{Role: chat.RoleUser, Content: "What is the weather in Tokyo?"},
		},
	}, []tool.ToolSpec{{
		Name:        "get_weather",
		Description: "Get current weather for a city",
		Parameters:  params,
	}})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if len(resp.Message.ToolCalls) == 0 {
		t.Skipf("model did not emit tool calls (answer was %q); model may not support tools", resp.Message.Content)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.Name != "get_weather" || len(tc.Arguments) == 0 {
		t.Fatalf("unexpected tool call: %+v", tc)
	}
	if !json.Valid(tc.Arguments) {
		t.Fatalf("arguments not valid JSON: %s", tc.Arguments)
	}
	t.Logf("tool call: %s(%s)", tc.Name, tc.Arguments)
}

func TestSmokeStream(t *testing.T) {
	c, ok := smokeClient(t)
	if !ok {
		return
	}
	stream, err := c.Stream(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{
			{Role: chat.RoleSystem, Content: "Reply with exactly: pong"},
			{Role: chat.RoleUser, Content: "ping"},
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var text string
	chunks := 0
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		chunks++
		text += chunk.Delta
		if chunk.FinishReason != "" {
			t.Logf("finish reason: %s usage=%+v", chunk.FinishReason, chunk.Usage)
		}
	}
	if chunks == 0 || text == "" {
		t.Fatalf("no stream chunks received (chunks=%d text=%q)", chunks, text)
	}
	t.Logf("streamed %d chunks: %q", chunks, text)
}
