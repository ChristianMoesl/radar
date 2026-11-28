package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"radar/internal/protocol"
)

type WorkspaceExecutor interface {
	AgentWorkspace(context.Context, string) (protocol.AgentWorkspaceBinding, error)
	ExecuteWorkspace(context.Context, string, string, []string, *protocol.AgentWorkspaceBinding) (string, error)
}

func (r Registry) WorkspaceExecutor() (WorkspaceExecutor, error) {
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(WorkspaceExecutor); ok {
			return provider, nil
		}
	}
	return nil, fmt.Errorf("workspace execution is unavailable")
}

// Agent workspace provisioning uses the ordinary workspace planner without an
// interactive session. Expected pins recovery to its original registration.
type AgentWorkspaceRequest struct {
	ID       string
	DataDir  string
	Path     string
	Preview  bool
	PlanID   string
	Note     json.RawMessage
	Expected *protocol.AgentWorkspaceBinding
	// Complete runs synchronously once after successful preparation of the
	// expected binding, never on preview or initial add. Managed workspaces keep
	// native admission through this trusted in-process continuation. Explicit
	// bindings remain validation-only. This is not a transport field or consent.
	Complete func(context.Context, protocol.AgentWorkspaceBinding) error `json:"-"`
}

type AgentWorkspaceManager interface {
	AgentWorkspace(context.Context, string) (protocol.AgentWorkspaceBinding, error)
	PrepareAgentWorkspace(context.Context, AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, WorkspaceReconcilePlan, error)
}
