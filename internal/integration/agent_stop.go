package integration

import (
	"context"
	"fmt"

	"radar/internal/protocol"
)

// AgentStopGuard belongs to the trusted typed caller, never a client payload.
// Capture must retain it without invoking it. At apply it must synchronously
// perform fresh scope/publication/consent admission and call execute exactly once
// while that admission is held. It must not re-enter the serialized agent manager.
// A refusal, cancellation or uncertain result never grants retry permission.
type AgentStopGuard func(context.Context, func() error) error

// AgentStopPreview contains native identity and point-in-time facts only, never
// private registration/workspace/process fields or verified work/resource outcomes.
// This in-process view is not a public transport contract or execution consent.
type AgentStopPreview struct {
	ID      string
	Desired bool
	Running bool
}

// AgentStopPlan retains native-only evidence and the original caller guard.
// Copies share one-use admission. It must not be serialized or accepted from a
// client; the typed caller must retain it in its existing bounded plan machinery.
type AgentStopPlan interface {
	Preview() AgentStopPreview
	Execute(context.Context) error
}

// AgentStopDiagnostics is separate from first-provider generic AgentManager
// dispatch. Prepare must be effect-free and Execute must reuse native shutdown.
type AgentStopDiagnostics interface {
	Integration
	PrepareDiagnosticAgentStop(context.Context, string, protocol.AgentWorkspaceBinding, AgentStopGuard) (AgentStopPlan, error)
}

func (r Registry) AgentStopDiagnostics() (AgentStopDiagnostics, error) {
	var selected AgentStopDiagnostics
	count := 0
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(AgentStopDiagnostics); ok {
			selected = provider
			count++
		}
	}
	if count != 1 {
		return nil, fmt.Errorf("exact native agent-stop provider unavailable")
	}
	return selected, nil
}
