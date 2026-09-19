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

// OptionalStatus distinguishes an absent prerequisite in automatic mode from a
// broken explicit request. It does not probe tools or credentials itself.
func OptionalStatus(name string, enabled *bool, missing string) StatusResult {
	status := protocol.SourceStatus{Name: name, Status: "ok"}
	if enabled != nil && !*enabled {
		status.Status, status.Detail = "disabled", "disabled by config"
	} else if missing != "" {
		status.Status, status.Detail = "disabled", missing
		if enabled != nil && *enabled {
			status.Status = "error"
		}
	}
	return StatusResult{Status: status, CanRun: status.Status == "ok"}
}
