package integration

import (
	"context"

	"radar/internal/protocol"
)

// CleanupMode separates explicit confirmation from automatic expiry. Expiry may
// discard workspace-local data, but never relaxes ownership or path boundaries.
type CleanupMode uint8

const (
	CleanupSafe CleanupMode = iota
	CleanupConfirmed
	CleanupExpired
)

func (m CleanupMode) DiscardChanges() bool {
	return m == CleanupConfirmed || m == CleanupExpired
}

type CleanupPreviewRequest struct {
	Task protocol.Task
	Mode CleanupMode
}

type CleanupRequest struct {
	Target protocol.CleanupTarget
	Mode   CleanupMode
}

type CleanupProvider interface {
	Integration
	PreviewCleanup(ctx context.Context, req CleanupPreviewRequest) ([]protocol.CleanupTarget, error)
	Cleanup(ctx context.Context, req CleanupRequest) (protocol.CleanupTarget, error)
}

// CleanupPreflightProvider validates protections for a whole bundle after its
// leases are acquired and before any provider has destructive side effects.
// Providers must still revalidate the resource they remove at the last moment.
type CleanupPreflightProvider interface {
	PreflightCleanup(context.Context, []protocol.CleanupTarget, CleanupMode) error
}
