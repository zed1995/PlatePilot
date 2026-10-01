// Package logging configures structured JSON logging and request-scoped loggers.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/zed/platepilot/shared/requestctx"
)

// Canonical log field names shared across the codebase.
const (
	FieldRequestID = "request_id"
	FieldTraceID   = "trace_id"
	FieldThreadID  = "thread_id"
	FieldRunID     = "run_id"
	FieldComponent = "component"
)

type loggerKey struct{}

// New builds a JSON logger writing to stdout at the given level.
func New(level string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: ParseLevel(level)}))
}

// ParseLevel maps a configuration string onto a slog level, defaulting to Info.
func ParseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithLogger stores logger in ctx for downstream handlers.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey{}, logger)
}

// FromContext returns the request-scoped logger, falling back to slog.Default.
func FromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return slog.Default()
	}
	if logger, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}

// WithRequest decorates logger with request identity fields.
func WithRequest(logger *slog.Logger, rc requestctx.RequestContext) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	attrs := make([]any, 0, 6)
	if rc.RequestID != "" {
		attrs = append(attrs, slog.String(FieldRequestID, rc.RequestID))
	}
	if rc.TraceID != "" {
		attrs = append(attrs, slog.String(FieldTraceID, rc.TraceID))
	}
	if rc.UserID != "" {
		attrs = append(attrs, slog.String("user_id", rc.UserID))
	}
	if len(attrs) == 0 {
		return logger
	}
	return logger.With(attrs...)
}
