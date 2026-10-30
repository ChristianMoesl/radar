// Package phasetiming records durations without capturing command arguments,
// output, environment values, or errors that might contain private data.
package phasetiming

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type loggerKey struct{}

func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}

func Start(ctx context.Context, phase string) func() {
	logger, _ := ctx.Value(loggerKey{}).(*slog.Logger)
	if logger == nil {
		return func() {}
	}
	start := time.Now()
	return sync.OnceFunc(func() {
		logger.Info("operation phase completed", "phase", phase, "duration", time.Since(start))
	})
}
