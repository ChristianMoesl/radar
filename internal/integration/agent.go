package integration

import (
	"context"
	"encoding/json"
	"fmt"

	"radar/internal/protocol"
)

// AgentManager is the provider-neutral boundary used by the daemon and CLI.
type AgentManager interface {
	Integration
	ManageAgent(context.Context, protocol.AgentRequest) (json.RawMessage, error)
}

type BackgroundService interface {
	Run(context.Context, func())
}

func (r Registry) ManageAgent(ctx context.Context, request protocol.AgentRequest) (json.RawMessage, error) {
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(AgentManager); ok {
			return provider.ManageAgent(ctx, request)
		}
	}
	return nil, fmt.Errorf("agent integration is not registered")
}

func (r Registry) StartBackground(ctx context.Context, changed func()) {
	for _, candidate := range r.integrations {
		if service, ok := candidate.(BackgroundService); ok {
			go service.Run(ctx, changed)
		}
	}
}
