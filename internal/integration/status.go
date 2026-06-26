package integration

import (
	"context"
	"log/slog"

	"radar/internal/protocol"
)

type StatusResult struct {
	Status protocol.SourceStatus
	CanRun bool
}

type StatusReporter interface {
	Status(ctx context.Context, logger *slog.Logger) StatusResult
}
