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
	// MaxBatch is how many texts go into one provider request.
	//
	// It is configuration rather than a constant because it is the knob that
	// decides throughput: a local CPU model embeds an array server-side in
	// parallel, so a larger batch is strictly cheaper per document, while a
	// larger batch also raises the chance that one request exceeds its timeout.
	// Which value wins cannot be decided from the code — it has to be measured
	// against the actual model and machine, and a constant makes that
	// impossible without recompiling.
	//
	// Zero means unset; DefaultMaxBatch is then used.
	MaxBatch int
}

// DefaultMaxBatch is the provider batch size when EMBEDDING_MAX_BATCH is unset.
//
// It is a starting point rather than a tuned constant. The embedding stage
// treats it as a default that --batch overrides, so measuring a different value
// does not require changing this.
const DefaultMaxBatch = 32

// BatchSize returns the effective provider batch size.
func (c EmbeddingConfig) BatchSize() int {
	if c.MaxBatch > 0 {
		return c.MaxBatch
	}
	return DefaultMaxBatch
}

// ProviderFake is the deterministic in-process embedding provider. It exists
// so the pipeline can be run and tested without a model server, which is the
// difference between a test that runs on every commit and one that needs a
// 639MB download first.
const ProviderFake = "fake"

// Enabled reports whether an embedding provider has been configured.
func (c EmbeddingConfig) Enabled() bool { return c.Provider != "" }

// Validate reports problems with the embedding settings.
func (c EmbeddingConfig) Validate() []string {
	if !c.Enabled() {
		return nil
	}
	// Zero is the "unset" sentinel BatchSize resolves, so only values outside
	// the accepted range are problems. They are reported rather than clamped:
	// a silently clamped batch would surface much later as an unexplained
	// throughput number, which is far harder to trace back than a refusal.
	problems := c.validateMaxBatch()
	// The fake provider needs no server, so the endpoint and model checks that
	// guard a real provider do not apply to it. A missing model name is still a
	// problem: the model is recorded on every document, and an empty one would
	// leave the audit trail unable to say what produced a vector.
	if c.Provider == ProviderFake {
		if strings.TrimSpace(c.Model) == "" {
			problems = append(problems, "EMBEDDING_MODEL: required when EMBEDDING_PROVIDER is set")
		}
		if c.Dimensions <= 0 {
			problems = append(problems,
				fmt.Sprintf("EMBEDDING_DIMENSIONS: must be > 0 (got %d)", c.Dimensions))
		}
		return problems
	}
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

// maxEmbedBatch caps EMBEDDING_MAX_BATCH.
//
// The batch is one JSON request body, so the size also caps how much text the
// provider must accept in a single read. Past this the request is refused by
// the server rather than made slower, and a refused run looks like an outage.
const maxEmbedBatch = 2048

// validateMaxBatch reports EMBEDDING_MAX_BATCH values that are out of range.
func (c EmbeddingConfig) validateMaxBatch() []string {
	var problems []string
	if c.MaxBatch < 0 {
		problems = append(problems, fmt.Sprintf("EMBEDDING_MAX_BATCH: must be > 0 (got %d)", c.MaxBatch))
	}
	if c.MaxBatch > maxEmbedBatch {
		problems = append(problems, fmt.Sprintf("EMBEDDING_MAX_BATCH: must be <= %d (got %d)", maxEmbedBatch, c.MaxBatch))
	}
	return problems
}

// RetrievalConfig holds the read path's ranking knobs.
//
// The weights live in configuration rather than in code because they encode a
// product decision — how far a soft match may reorder a hard-filtered list — and
// that decision is only answerable once an evaluation set can measure it. A
// constant would make the answer require a recompile.
type RetrievalConfig struct {
	Weights RetrievalWeights
	// Oversample is how much deeper than TopK each channel reads, so a channel
	// that fills its own page cannot hide a restaurant another channel ranked
	// first.
	Oversample int
	TopK       int
	// EnableKeyword, EnableStructured and EnableVector switch individual
	// channels off. A disabled channel is recorded in the trace as not run, so
	// a weakened ranking is visible rather than silent.
	EnableKeyword    bool
	EnableStructured bool
	EnableVector     bool
	// EmbeddingTimeout bounds the on-demand query embedding.
	//
	// It is separate from the pipeline's REQUEST_TIMEOUT because that default
	// is sized for embedding a whole batch of long documents on a CPU model —
	// three minutes. An online recall embeds one short string, and a caller
	// waiting three minutes for a search has already been given its answer by
	// giving up.
	EmbeddingTimeout time.Duration
}

// RetrievalWeights scales each channel's contribution to a fused score.
type RetrievalWeights struct {
	Structured float64
	Keyword    float64
	Vector     float64
	// Quality scales the restaurant's own prior. It is kept smallest so a
	// ranking answers the question rather than sorting by reputation.
	Quality float64
}

// DefaultRetrievalWeights keeps the structured channel dominant: a restaurant
// satisfying every stated condition must not be displaced by one that reads
// well.
var DefaultRetrievalWeights = RetrievalWeights{
	Structured: 1.0,
	Keyword:    0.5,
	Vector:     1.0,
	Quality:    0.2,
}

// Validate reports problems with the retrieval settings.
func (c RetrievalConfig) Validate() []string {
	var problems []string
	weights := map[string]float64{
		"RETRIEVAL_WEIGHT_STRUCTURED": c.Weights.Structured,
		"RETRIEVAL_WEIGHT_KEYWORD":    c.Weights.Keyword,
		"RETRIEVAL_WEIGHT_VECTOR":     c.Weights.Vector,
		"RETRIEVAL_WEIGHT_QUALITY":    c.Weights.Quality,
	}
	// Names are visited in a fixed order so two runs over the same bad
	// environment report the problems in the same sequence.
	for _, name := range []string{
		"RETRIEVAL_WEIGHT_STRUCTURED",
		"RETRIEVAL_WEIGHT_KEYWORD",
		"RETRIEVAL_WEIGHT_VECTOR",
		"RETRIEVAL_WEIGHT_QUALITY",
	} {
		if weights[name] < 0 {
			problems = append(problems, fmt.Sprintf("%s: must be >= 0 (got %g)", name, weights[name]))
		}
	}
	if c.Oversample <= 0 {
		problems = append(problems, fmt.Sprintf("RETRIEVAL_OVERSAMPLE: must be > 0 (got %d)", c.Oversample))
	}
	if c.TopK <= 0 {
		problems = append(problems, fmt.Sprintf("RETRIEVAL_TOP_K: must be > 0 (got %d)", c.TopK))
	}
	if c.EmbeddingTimeout <= 0 {
		problems = append(problems,
			fmt.Sprintf("RETRIEVAL_EMBEDDING_TIMEOUT: must be > 0 (got %s)", c.EmbeddingTimeout))
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

// Float parses a floating-point value, recording a problem and returning def on
// error.
func (l *Loader) Float(key string, def float64) float64 {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s: invalid number %q", key, raw))
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
		MaxBatch:   l.Int("EMBEDDING_MAX_BATCH", DefaultMaxBatch),
	}
}

// Retrieval loads the read path's ranking configuration.
func (l *Loader) Retrieval() RetrievalConfig {
	defaults := DefaultRetrievalWeights
	return RetrievalConfig{
		Weights: RetrievalWeights{
			Structured: l.Float("RETRIEVAL_WEIGHT_STRUCTURED", defaults.Structured),
			Keyword:    l.Float("RETRIEVAL_WEIGHT_KEYWORD", defaults.Keyword),
			Vector:     l.Float("RETRIEVAL_WEIGHT_VECTOR", defaults.Vector),
			Quality:    l.Float("RETRIEVAL_WEIGHT_QUALITY", defaults.Quality),
		},
		Oversample:       l.Int("RETRIEVAL_OVERSAMPLE", 2),
		TopK:             l.Int("RETRIEVAL_TOP_K", 5),
		EnableKeyword:    l.Bool("RETRIEVAL_ENABLE_KEYWORD", true),
		EnableStructured: l.Bool("RETRIEVAL_ENABLE_STRUCTURED", true),
		EnableVector:     l.Bool("RETRIEVAL_ENABLE_VECTOR", true),
		EmbeddingTimeout: l.Duration("RETRIEVAL_EMBEDDING_TIMEOUT", 5*time.Second),
	}
}

// DefaultRequestTimeout bounds one outbound request.
//
// It is three minutes rather than the more usual fifteen seconds because the
// heaviest document this pipeline embeds -- a representative-review chunk --
// averages 2,878 characters and reaches 8,615. Measured against the local
// Qwen3 model on CPU, a batch of the sixteen longest of those takes 28s and a
// batch of thirty-two takes 52s, so a 15s default fails on real data while
// passing on the short profile documents it was sized for. See E.32.
const DefaultRequestTimeout = 3 * time.Minute

// Timeout loads the shared outbound timeout config.
func (l *Loader) Timeout() TimeoutConfig {
	return TimeoutConfig{Request: l.Duration("REQUEST_TIMEOUT", DefaultRequestTimeout)}
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
