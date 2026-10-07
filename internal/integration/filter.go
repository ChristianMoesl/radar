package integration

import (
	"log/slog"

	"radar/internal/protocol"
)

// TaskFilterProvider applies one integration's user-owned visibility and
// priority rules to projected tasks. Filters run in integration registration
// order and must preserve source facts and unrelated source contributions.
// Providers may reproject the preserved refs with source-local contribution
// policy; they must not discard linked work owned by other integrations.
type TaskFilterProvider interface {
	Integration
	FilterTasks(tasks []protocol.Task, logger *slog.Logger) []protocol.Task
}
