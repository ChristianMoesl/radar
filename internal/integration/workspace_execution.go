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
}

type AgentWorkspaceManager interface {
	AgentWorkspace(context.Context, string) (protocol.AgentWorkspaceBinding, error)
	PrepareAgentWorkspace(context.Context, AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, WorkspaceReconcilePlan, error)
}
