package integration

import (
	"context"
	"log/slog"
)

type RateLimitReporter interface {
	Integration
	RateLimitSummary(ctx context.Context, logger *slog.Logger) (string, error)
}
