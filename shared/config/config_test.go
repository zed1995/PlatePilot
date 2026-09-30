package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoaderReadsValuesAndDefaults(t *testing.T) {
	t.Setenv("PP_STR", "value")
	t.Setenv("PP_DURATION", "2s")
	t.Setenv("PP_INT", "7")
	t.Setenv("PP_LIST", "a, b ,,c")
	t.Setenv("PP_MAP", `{"X-Title":"PlatePilot"}`)

	l := NewLoader()
	if got := l.String("PP_STR", "def"); got != "value" {
		t.Errorf("String = %q", got)
	}
	if got := l.String("PP_MISSING", "def"); got != "def" {
		t.Errorf("String default = %q", got)
	}
	if got := l.Duration("PP_DURATION", time.Second); got != 2*time.Second {
		t.Errorf("Duration = %s", got)
	}
	if got := l.Int("PP_INT", 1); got != 7 {
		t.Errorf("Int = %d", got)
	}
	if got := l.List("PP_LIST"); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("List = %v", got)
	}
	if got := l.StringMap("PP_MAP"); got["X-Title"] != "PlatePilot" {
		t.Errorf("StringMap = %v", got)
	}
	if err := l.Err(); err != nil {
		t.Fatalf("unexpected problems: %v", err)
	}
}

func TestLoaderCollectsEveryProblem(t *testing.T) {
	t.Setenv("PP_DURATION", "not-a-duration")
	t.Setenv("PP_INT", "not-an-int")
	t.Setenv("PP_MAP", "{not json")

	l := NewLoader()
	_ = l.Duration("PP_DURATION", time.Second)
	_ = l.Int("PP_INT", 1)
	_ = l.StringMap("PP_MAP")

	err := l.Err()
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	if len(verr.Fields) != 3 {
		t.Fatalf("want 3 problems, got %d: %v", len(verr.Fields), verr.Fields)
	}
	for _, key := range []string{"PP_DURATION", "PP_INT", "PP_MAP"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q should name %s", err.Error(), key)
		}
	}
}

func TestLoaderCompositeDefaults(t *testing.T) {
	for _, key := range []string{
		"APP_ENV", "LOG_LEVEL", "POSTGRES_DSN", "POSTGRES_DATABASE",
		"EMBEDDING_PROVIDER", "EMBEDDING_MODEL", "EMBEDDING_DIMENSIONS",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}

	l := NewLoader()
	if got := l.App().Env; got != EnvDev {
		t.Errorf("APP_ENV default = %q", got)
	}
	if got := l.Log().Level; got != "info" {
		t.Errorf("LOG_LEVEL default = %q", got)
	}
	if got := l.Postgres().Database; got != "platepilot" {
		t.Errorf("POSTGRES_DATABASE default = %q", got)
	}
	if got := l.Postgres().DSN; got != "" {
		t.Errorf("POSTGRES_DSN default = %q, want empty", got)
	}
	if got := l.Embedding().Model; got != "qwen3-embedding:0.6b" {
		t.Errorf("EMBEDDING_MODEL default = %q", got)
	}
	if got := l.Embedding().Dimensions; got != 1024 {
		t.Errorf("EMBEDDING_DIMENSIONS default = %d", got)
	}
	if got := l.Timeout().Request; got != 15*time.Second {
		t.Errorf("REQUEST_TIMEOUT default = %s", got)
	}
}

func TestSubConfigValidation(t *testing.T) {
	if problems := (AppConfig{Env: "staging"}).Validate(); len(problems) != 1 {
		t.Errorf("bad APP_ENV should report one problem, got %v", problems)
	}
	if problems := (AppConfig{Env: EnvProd}).Validate(); len(problems) != 0 {
		t.Errorf("valid APP_ENV reported problems: %v", problems)
	}
	if problems := (LogConfig{Level: "loud"}).Validate(); len(problems) != 1 {
		t.Errorf("bad LOG_LEVEL should report one problem, got %v", problems)
	}
	if problems := (PostgresConfig{DSN: "postgres://x"}).Validate(); len(problems) == 0 {
		t.Error("Postgres with empty database should report a problem")
	}
	if problems := (PostgresConfig{}).Validate(); len(problems) != 0 {
		t.Error("disabled Postgres should report no problems")
	}
	if problems := (EmbeddingConfig{Provider: "ollama", Dimensions: 0}).Validate(); len(problems) == 0 {
		t.Error("embedding with zero dimensions should report a problem")
	}
}

func TestCombine(t *testing.T) {
	if err := Combine(nil, nil); err != nil {
		t.Fatalf("no problems should yield nil, got %v", err)
	}
	err := Combine([]string{"a"}, nil, []string{"b", "c"})
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	if len(verr.Fields) != 3 {
		t.Fatalf("want 3 fields, got %v", verr.Fields)
	}
}

func TestLoadDotEnvPrefersEnvironmentAndParsesExport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "PP_TEST_KEY=fromfile\nexport PP_TEST_EXPORTED=exported\n# comment\n\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PP_TEST_KEY", "fromenv")
	os.Unsetenv("PP_TEST_EXPORTED")
	t.Cleanup(func() { os.Unsetenv("PP_TEST_EXPORTED") })

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("PP_TEST_KEY"); got != "fromenv" {
		t.Errorf("existing env must win, got %q", got)
	}
	if got := os.Getenv("PP_TEST_EXPORTED"); got != "exported" {
		t.Errorf("export lines should parse, got %q", got)
	}
}

func TestLoadDotEnvMissingFileIsNotAnError(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "absent.env")); err != nil {
		t.Fatalf("missing .env should be ignored, got %v", err)
	}
}

func TestRedaction(t *testing.T) {
	if got := Redact(""); got != "" {
		t.Errorf("empty secret should stay empty, got %q", got)
	}
	if got := Redact("super-secret"); got != "***" {
		t.Errorf("secret should be redacted, got %q", got)
	}
	uri := "postgres://platepilot:hunter2@cluster.example/platepilot"
	redacted := RedactURI(uri)
	if strings.Contains(redacted, "hunter2") || strings.Contains(redacted, "platepilot:") {
		t.Errorf("credentials not removed: %q", redacted)
	}
	if !strings.Contains(redacted, "cluster.example") {
		t.Errorf("host should be preserved: %q", redacted)
	}
	if got := RedactURI("not a uri"); got != "***" {
		t.Errorf("unparseable uri should be fully redacted, got %q", got)
	}
}

func TestRedactKeywordDSN(t *testing.T) {
	// A libpq keyword/value DSN must lose the password but keep the host, so a
	// log line stays useful for debugging.
	got := RedactURI("host=localhost port=55432 dbname=platepilot user=platepilot password=hunter2 sslmode=disable")
	if strings.Contains(got, "hunter2") {
		t.Errorf("password not redacted: %q", got)
	}
	if !strings.Contains(got, "host=localhost") {
		t.Errorf("host should be preserved: %q", got)
	}
	if !strings.Contains(got, "dbname=platepilot") {
		t.Errorf("dbname should be preserved: %q", got)
	}

	// Arbitrary text must still be redacted wholesale rather than passed through.
	for _, opaque := range []string{"not a uri", "some free text", "a=b"} {
		if got := RedactURI(opaque); got != "***" {
			t.Errorf("RedactURI(%q) = %q, want ***", opaque, got)
		}
	}
}

func TestRedactPostgresURL(t *testing.T) {
	got := RedactURI("postgres://platepilot:hunter2@localhost:55432/platepilot?sslmode=disable")
	if strings.Contains(got, "hunter2") || strings.Contains(got, "platepilot:") {
		t.Errorf("credentials not removed: %q", got)
	}
	if !strings.Contains(got, "localhost:55432") {
		t.Errorf("host should be preserved: %q", got)
	}
}
