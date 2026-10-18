package integration

import (
	"context"
	"fmt"
	"radar/internal/protocol"
)

type WorkspaceExecutor interface {
	AgentWorkspace(context.Context, string) (protocol.AgentWorkspaceBinding, error)
	ExecuteWorkspace(context.Context, string, string, []string) (string, error)
}

func (r Registry) WorkspaceExecutor() (WorkspaceExecutor, error) {
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(WorkspaceExecutor); ok {
			return provider, nil
		}
	}
	return nil, fmt.Errorf("workspace execution is unavailable")
}
