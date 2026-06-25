package sourceactions

import (
	"context"
	"fmt"
	"os"

	"radar/internal/app"
	"radar/internal/integration"
	"radar/internal/protocol"
)

type Action = integration.Action

type Result = integration.ActionResult

func SourceRefActions(ref protocol.SourceRef, label string) []Action {
	return SourceRefActionsWithProviders(context.Background(), app.DefaultIntegrationSet().ActionProviders, protocol.Task{}, ref, label)
}

func SourceRefActionsWithProviders(ctx context.Context, providers []integration.ActionProvider, task protocol.Task, ref protocol.SourceRef, label string) []Action {
	actions := make([]Action, 0)
	for _, provider := range providers {
		actions = append(actions, provider.Actions(ctx, integration.ActionRequest{Task: task, Ref: ref, Label: label})...)
	}
	return actions
}

func Open(ctx context.Context, task protocol.Task, actionID string, ref protocol.SourceRef) (Result, error) {
	return OpenWithIntegrations(ctx, app.DefaultIntegrationSet(), task, actionID, ref)
}

func OpenWithIntegrations(ctx context.Context, integrations integration.Set, task protocol.Task, actionID string, ref protocol.SourceRef) (Result, error) {
	return open(ctx, integrations.ActionProviders, integrations.Multiplexer, task, actionID, ref)
}

func OpenWithProviders(ctx context.Context, providers []integration.ActionProvider, task protocol.Task, actionID string, ref protocol.SourceRef) (Result, error) {
	return open(ctx, providers, nil, task, actionID, ref)
}

func open(ctx context.Context, providers []integration.ActionProvider, multiplexer integration.MultiplexerProvider, task protocol.Task, actionID string, ref protocol.SourceRef) (Result, error) {
	switchClient := os.Getenv("TMUX") != ""
	for _, provider := range providers {
		actions := provider.Actions(ctx, integration.ActionRequest{Task: task, Ref: ref})
		for _, action := range actions {
			if action.ID != actionID {
				continue
			}
			return provider.RunAction(ctx, integration.RunActionRequest{Task: task, ActionID: actionID, Ref: ref, SwitchClient: switchClient, Multiplexer: multiplexer})
		}
	}
	return Result{}, fmt.Errorf("unknown source action: %s", actionID)
}
