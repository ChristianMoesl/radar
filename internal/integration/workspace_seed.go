package integration

import (
	"context"

	"radar/internal/protocol"
)

type WorkspaceSeed struct {
	Repo       string
	Name       string
	Branch     string
	BranchMode WorkspaceBranchMode
	Warning    string
	NotePath   string
}

type WorkspaceSeedProvider interface {
	Integration
	CanSeedWorkspace(ref protocol.SourceRef) bool
	PrepareWorkspaceSeed(ctx context.Context, ref protocol.SourceRef) (WorkspaceSeed, error)
}
