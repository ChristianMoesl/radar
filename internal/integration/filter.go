package integration

import (
	"log/slog"

	"radar/internal/protocol"
)

// TaskFilterProvider applies one integration's user-owned visibility and
// priority rules to projected tasks. Filters run in integration registration
// order and must leave tasks from unrelated integrations unchanged.
type TaskFilterProvider interface {
	Integration
	FilterTasks(tasks []protocol.Task, logger *slog.Logger) []protocol.Task
}
