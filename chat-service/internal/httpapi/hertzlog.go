package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/common/hlog"

	"github.com/zed1995/platepilot/shared/observability/logging"
)

// hertzLogger adapts Hertz's logging interface onto log/slog so that framework
// logs share the application's JSON format instead of printing plain text.
//
// Trace and Debug both map to slog's debug level; Notice maps to info; Fatal is
// recorded as an error and deliberately does not call os.Exit, because process
// lifecycle is owned by cmd/platepilot.
type hertzLogger struct {
	logger *slog.Logger
}

func newHertzLogger(logger *slog.Logger) hertzLogger {
	if logger == nil {
		logger = slog.Default()
	}
	return hertzLogger{logger: logger.With(slog.String(logging.FieldComponent, "hertz"))}
}

func (l hertzLogger) write(ctx context.Context, level slog.Level, msg string) {
	if ctx == nil {
		ctx = context.Background()
	}
	l.logger.Log(ctx, level, msg)
}

func (l hertzLogger) plain(level slog.Level, v ...any) {
	l.write(context.Background(), level, fmt.Sprint(v...))
}

func (l hertzLogger) formatted(ctx context.Context, level slog.Level, format string, v ...any) {
	l.write(ctx, level, fmt.Sprintf(format, v...))
}

func (l hertzLogger) Trace(v ...any)  { l.plain(slog.LevelDebug, v...) }
func (l hertzLogger) Debug(v ...any)  { l.plain(slog.LevelDebug, v...) }
func (l hertzLogger) Info(v ...any)   { l.plain(slog.LevelInfo, v...) }
func (l hertzLogger) Notice(v ...any) { l.plain(slog.LevelInfo, v...) }
func (l hertzLogger) Warn(v ...any)   { l.plain(slog.LevelWarn, v...) }
func (l hertzLogger) Error(v ...any)  { l.plain(slog.LevelError, v...) }
func (l hertzLogger) Fatal(v ...any)  { l.plain(slog.LevelError, v...) }

func (l hertzLogger) Tracef(format string, v ...any) {
	l.formatted(context.Background(), slog.LevelDebug, format, v...)
}
func (l hertzLogger) Debugf(format string, v ...any) {
	l.formatted(context.Background(), slog.LevelDebug, format, v...)
}
func (l hertzLogger) Infof(format string, v ...any) {
	l.formatted(context.Background(), slog.LevelInfo, format, v...)
}
func (l hertzLogger) Noticef(format string, v ...any) {
	l.formatted(context.Background(), slog.LevelInfo, format, v...)
}
func (l hertzLogger) Warnf(format string, v ...any) {
	l.formatted(context.Background(), slog.LevelWarn, format, v...)
}
func (l hertzLogger) Errorf(format string, v ...any) {
	l.formatted(context.Background(), slog.LevelError, format, v...)
}
func (l hertzLogger) Fatalf(format string, v ...any) {
	l.formatted(context.Background(), slog.LevelError, format, v...)
}

func (l hertzLogger) CtxTracef(ctx context.Context, format string, v ...any) {
	l.formatted(ctx, slog.LevelDebug, format, v...)
}
func (l hertzLogger) CtxDebugf(ctx context.Context, format string, v ...any) {
	l.formatted(ctx, slog.LevelDebug, format, v...)
}
func (l hertzLogger) CtxInfof(ctx context.Context, format string, v ...any) {
	l.formatted(ctx, slog.LevelInfo, format, v...)
}
func (l hertzLogger) CtxNoticef(ctx context.Context, format string, v ...any) {
	l.formatted(ctx, slog.LevelInfo, format, v...)
}
func (l hertzLogger) CtxWarnf(ctx context.Context, format string, v ...any) {
	l.formatted(ctx, slog.LevelWarn, format, v...)
}
func (l hertzLogger) CtxErrorf(ctx context.Context, format string, v ...any) {
	l.formatted(ctx, slog.LevelError, format, v...)
}
func (l hertzLogger) CtxFatalf(ctx context.Context, format string, v ...any) {
	l.formatted(ctx, slog.LevelError, format, v...)
}

// SetLevel is intentionally a no-op: verbosity is controlled by the slog handler
// level configured in logging.New, not by Hertz's own level.
func (l hertzLogger) SetLevel(hlog.Level) {}

// SetOutput is intentionally a no-op: output is owned by the slog handler.
func (l hertzLogger) SetOutput(io.Writer) {}
