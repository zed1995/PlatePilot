// Package config provides configuration primitives shared by the data pipeline
// and the chat service: environment loading, validation errors, reusable
// sub-configs, and secret redaction.
//
// Each service composes these primitives into its own Config so that a service
// only validates what it actually needs.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Recognised application environments.
const (
	EnvDev  = "dev"
	EnvTest = "test"
	EnvProd = "prod"
)

// AppConfig holds process-level settings shared by every service.
type AppConfig struct {
	Env string
}

// Validate reports problems with the application environment.
func (c AppConfig) Validate() []string {
	switch c.Env {
	case EnvDev, EnvTest, EnvProd:
		return nil
	default:
		return []string{fmt.Sprintf("APP_ENV: must be one of %s|%s|%s (got %q)", EnvDev, EnvTest, EnvProd, c.Env)}
	}
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level string
}

// Validate reports problems with the log level.
func (c LogConfig) Validate() []string {
	switch c.Level {
	case "debug", "info", "warn", "error":
		return nil
	default:
		return []string{fmt.Sprintf("LOG_LEVEL: must be one of debug|info|warn|error (got %q)", c.Level)}
	}
}

// PostgresConfig holds PostgreSQL connection settings.
type PostgresConfig struct {
	// DSN is a libpq-style connection string, for example
	// "postgres://platepilot:platepilot@localhost:55432/platepilot?sslmode=disable".
	// Empty disables the store.
	DSN string
	// Database is the logical database name, used for reporting and to build
	// the default DSN.
	Database string
	// Timeout bounds a single statement.
	Timeout time.Duration
	// ConnectTimeout bounds the initial handshake.
	ConnectTimeout time.Duration
	// MaxPoolSize and MinPoolSize bound the connection pool. Zero means the
	// driver default.
	MaxPoolSize int32
	MinPoolSize int32
}

// Enabled reports whether PostgreSQL has been configured.
func (c PostgresConfig) Enabled() bool { return c.DSN != "" }

// Validate reports problems with the PostgreSQL settings.
func (c PostgresConfig) Validate() []string {
	if !c.Enabled() {
		return nil
	}
	var problems []string
	if strings.TrimSpace(c.Database) == "" {
		problems = append(problems, "POSTGRES_DATABASE: required when POSTGRES_DSN is set")
	}
	if c.Timeout <= 0 {
		problems = append(problems, fmt.Sprintf("POSTGRES_TIMEOUT: must be > 0 (got %s)", c.Timeout))
	}
	if c.ConnectTimeout <= 0 {
		problems = append(problems, fmt.Sprintf("POSTGRES_CONNECT_TIMEOUT: must be > 0 (got %s)", c.ConnectTimeout))
	}
	if c.MinPoolSize > 0 && c.MaxPoolSize > 0 && c.MinPoolSize > c.MaxPoolSize {
		problems = append(problems, fmt.Sprintf("POSTGRES_MIN_POOL_SIZE: must not exceed POSTGRES_MAX_POOL_SIZE (got %d > %d)", c.MinPoolSize, c.MaxPoolSize))
	}
	return problems
}

// EmbeddingConfig holds the local Ollama embedding settings.
type EmbeddingConfig struct {
	Provider   string
	BaseURL    string
	Model      string
	Dimensions int
}

// Enabled reports whether an embedding provider has been configured.
func (c EmbeddingConfig) Enabled() bool { return c.Provider != "" }

// Validate reports problems with the embedding settings.
func (c EmbeddingConfig) Validate() []string {
	if !c.Enabled() {
		return nil
	}
	var problems []string
	if strings.TrimSpace(c.BaseURL) == "" {
		problems = append(problems, "OLLAMA_BASE_URL: required when EMBEDDING_PROVIDER is set")
	}
	if strings.TrimSpace(c.Model) == "" {
		problems = append(problems, "EMBEDDING_MODEL: required when EMBEDDING_PROVIDER is set")
	}
	if c.Dimensions <= 0 {
		problems = append(problems, fmt.Sprintf("EMBEDDING_DIMENSIONS: must be > 0 (got %d)", c.Dimensions))
	}
	return problems
}

// TimeoutConfig holds shared outbound request timeouts.
type TimeoutConfig struct {
	Request time.Duration
}

// ValidationError aggregates every configuration problem found at startup so an
// operator can fix them in one pass instead of one restart at a time.
type ValidationError struct {
	Fields []string
}

func (e *ValidationError) Error() string {
	return "invalid configuration: " + strings.Join(e.Fields, "; ")
}

// Combine collects the non-empty problem lists into a single error, or nil.
func Combine(problemGroups ...[]string) error {
	var fields []string
	for _, group := range problemGroups {
		fields = append(fields, group...)
	}
	if len(fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: fields}
}

// Loader reads environment variables and accumulates parse failures instead of
// stopping at the first bad value.
type Loader struct {
	problems []string
}

// NewLoader returns an empty Loader.
func NewLoader() *Loader { return &Loader{} }

// String returns the environment value or def when unset/empty.
func (l *Loader) String(key, def string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return def
}

// Duration parses a Go duration, recording a problem and returning def on error.
func (l *Loader) Duration(key string, def time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s: invalid duration %q", key, raw))
		return def
	}
	return value
}

// Int parses an integer, recording a problem and returning def on error.
func (l *Loader) Int(key string, def int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s: invalid integer %q", key, raw))
		return def
	}
	return value
}

// Bool parses a boolean, recording a problem and returning def on error.
func (l *Loader) Bool(key string, def bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s: invalid boolean %q", key, raw))
		return def
	}
	return value
}

// List reads a comma-separated list, trimming blanks.
func (l *Loader) List(key string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// StringMap parses a JSON object of string values.
func (l *Loader) StringMap(key string) map[string]string {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s: invalid JSON object %q", key, raw))
		return nil
	}
	return out
}

// Err returns the accumulated parse problems, or nil.
func (l *Loader) Err() error {
	if len(l.problems) == 0 {
		return nil
	}
	return &ValidationError{Fields: l.problems}
}

// App loads the shared application config.
func (l *Loader) App() AppConfig {
	return AppConfig{Env: l.String("APP_ENV", EnvDev)}
}

// Log loads the shared logging config.
func (l *Loader) Log() LogConfig {
	return LogConfig{Level: l.String("LOG_LEVEL", "info")}
}

// Postgres loads the shared PostgreSQL config.
func (l *Loader) Postgres() PostgresConfig {
	return PostgresConfig{
		DSN:            l.String("POSTGRES_DSN", ""),
		Database:       l.String("POSTGRES_DATABASE", "platepilot"),
		Timeout:        l.Duration("POSTGRES_TIMEOUT", 30*time.Second),
		ConnectTimeout: l.Duration("POSTGRES_CONNECT_TIMEOUT", 10*time.Second),
		MaxPoolSize:    int32(max(l.Int("POSTGRES_MAX_POOL_SIZE", 0), 0)),
		MinPoolSize:    int32(max(l.Int("POSTGRES_MIN_POOL_SIZE", 0), 0)),
	}
}

// Embedding loads the shared Ollama embedding config.
func (l *Loader) Embedding() EmbeddingConfig {
	return EmbeddingConfig{
		Provider:   l.String("EMBEDDING_PROVIDER", ""),
		BaseURL:    l.String("OLLAMA_BASE_URL", "http://localhost:11434"),
		Model:      l.String("EMBEDDING_MODEL", "qwen3-embedding:0.6b"),
		Dimensions: l.Int("EMBEDDING_DIMENSIONS", 1024),
	}
}

// Timeout loads the shared outbound timeout config.
func (l *Loader) Timeout() TimeoutConfig {
	return TimeoutConfig{Request: l.Duration("REQUEST_TIMEOUT", 15*time.Second)}
}

// LoadDotEnv reads KEY=VALUE pairs from path and sets any variable that is not
// already present in the environment. A missing file is not an error, and an
// existing environment variable always wins.
func LoadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

// Redact replaces a non-empty secret with a placeholder.
func Redact(secret string) string {
	if secret == "" {
		return ""
	}
	return "***"
}

// looksLikeKeywordDSN reports whether s is a libpq keyword/value connection
// string rather than an arbitrary unparseable value.
func looksLikeKeywordDSN(s string) bool {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return false
	}
	// Every field must be a known libpq keyword=value pair, or a bare value
	// that continues the previous keyword (libpq allows quoted values with
	// spaces). Requiring at least one recognised keyword keeps prose out.
	known := map[string]bool{
		"host": true, "hostaddr": true, "port": true, "dbname": true,
		"user": true, "password": true, "passfile": true, "sslmode": true,
		"sslrootcert": true, "sslcert": true, "sslkey": true,
		"connect_timeout": true, "application_name": true, "search_path": true,
		"options": true, "target_session_attrs": true, "service": true,
		"servicefile": true, "sslcrl": true, "gssencmode": true,
		"channel_binding": true, "sslnegotiation": true, "sslcompression": true,
		"ssl_min_protocol_version": true, "ssl_max_protocol_version": true,
		"krbsrvname": true, "gsslib": true,
	}
	sawKnown := false
	for _, field := range fields {
		key, _, found := strings.Cut(field, "=")
		if !found {
			// A continuation of a previous quoted value; tolerate it.
			continue
		}
		if known[strings.ToLower(strings.TrimSpace(key))] {
			sawKnown = true
			continue
		}
		// An unrecognised key means this is not a DSN.
		return false
	}
	return sawKnown
}

// RedactURI strips embedded credentials from a connection string. It accepts
// both libpq URLs and keyword/value DSNs.
func RedactURI(uri string) string {
	if uri == "" {
		return ""
	}
	// libpq also accepts a keyword/value DSN ("host=... password=..."). It is
	// recognised by looking like key=value pairs rather than by assuming every
	// non-URL string is a DSN, so an arbitrary unparseable value is still
	// redacted wholesale.
	if looksLikeKeywordDSN(uri) {
		fields := strings.Fields(uri)
		for i, field := range fields {
			key, _, found := strings.Cut(field, "=")
			if found && strings.EqualFold(strings.TrimSpace(key), "password") {
				fields[i] = key + "=***"
			}
		}
		return strings.Join(fields, " ")
	}
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Host == "" {
		return "***"
	}
	parsed.User = nil
	return parsed.String()
}
