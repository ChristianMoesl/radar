package integration

import (
	"context"

	"radar/internal/protocol"
)

// TaskLifecycleProvider persists remote completion and activity-driven handoffs.
// Work items are authoritative contributors with confirmed signals. A provider
// may reopen on changed active work or preserve explicit Done/Reopen baselines.
// Empty WorkItems allow only activity-driven reopening/retries, never remote completion.
// A nil observation means no source mutation was needed.
type TaskLifecycleProvider interface {
	Integration
	ReconcileLifecycle(ctx context.Context, ref protocol.SourceRef, request TaskLifecycleRequest) (*Observation, error)
}

// RequireAgentReview retries a handoff whose note write previously failed.
// WorkItems is empty when only local activity, not remote completion, is known.
type TaskLifecycleRequest struct {
	Task               protocol.Task
	WorkItems          []protocol.SourceRef
	Activity           protocol.Activity
	RequireAgentReview bool
}

// AgentReviewPersistenceError preserves the pending intent in the rebuildable
// cache until the canonical note can be written. It is never a successful source
// observation; callers still report the persistence error.
type AgentReviewPersistenceError struct{ Err error }

func (e *AgentReviewPersistenceError) Error() string { return e.Err.Error() }
func (e *AgentReviewPersistenceError) Unwrap() error { return e.Err }
