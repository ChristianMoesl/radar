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
type observerKey struct{}

// WithObserver receives completed native intervals, even without a logger.
// Observers must return promptly. The caller supplies correlation; phase strings
// are not inherently safe and must be allowlisted before diagnostic projection.
func WithObserver(ctx context.Context, observer func(string, time.Duration)) context.Context {
	return context.WithValue(ctx, observerKey{}, observer)
}

func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}

func Start(ctx context.Context, phase string) func() {
	logger, _ := ctx.Value(loggerKey{}).(*slog.Logger)
	observer, _ := ctx.Value(observerKey{}).(func(string, time.Duration))
	if logger == nil && observer == nil {
		return func() {}
	}
	start := time.Now()
	return sync.OnceFunc(func() {
		elapsed := time.Since(start)
		if observer != nil {
			observer(phase, elapsed)
		}
		if logger != nil {
			logger.Info("operation phase completed", "phase", phase, "duration", elapsed)
		}
	})
}
