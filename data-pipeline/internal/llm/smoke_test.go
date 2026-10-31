package llm_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/zed1995/platepilot/data-pipeline/internal/llm"
	sharedcfg "github.com/zed1995/platepilot/shared/config"
	"github.com/zed1995/platepilot/shared/domain/chat"
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

// The smoke test hits a real OpenAI-compatible endpoint and stays skipped
// unless explicitly enabled:
//
//	# after filling DIGEST_CHAT_BASE_URL/DIGEST_CHAT_API_KEY/DIGEST_CHAT_MODEL
//	# into the repository-root .env
//	PLATEPILOT_TEST_CHAT=1 go test ./internal/llm/ -run Smoke -count=1 -v
//
// Inline environment variables work too and take precedence:
//
//	PLATEPILOT_TEST_CHAT=1 DIGEST_CHAT_BASE_URL=... DIGEST_CHAT_API_KEY=... \
//	  DIGEST_CHAT_MODEL=... go test -run Smoke -count=1 -v
func TestSmokeComplete(t *testing.T) {
	loadRepoDotEnv(t)
	if os.Getenv("PLATEPILOT_TEST_CHAT") != "1" {
		t.Skip("set PLATEPILOT_TEST_CHAT=1 and DIGEST_CHAT_BASE_URL/DIGEST_CHAT_API_KEY/DIGEST_CHAT_MODEL to run")
	}
	baseURL := os.Getenv("DIGEST_CHAT_BASE_URL")
	apiKey := os.Getenv("DIGEST_CHAT_API_KEY")
	model := os.Getenv("DIGEST_CHAT_MODEL")
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("DIGEST_CHAT_BASE_URL, DIGEST_CHAT_API_KEY and DIGEST_CHAT_MODEL are all required for smoke tests")
	}

	c, err := llm.New(llm.Options{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		Model:      model,
		Timeout:    60 * time.Second,
		MaxRetries: llm.DefaultMaxRetries,
	})
	if err != nil {
		t.Fatalf("llm.New: %v", err)
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
