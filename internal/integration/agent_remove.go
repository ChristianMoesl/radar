package integration

import (
	"context"
	"fmt"
	"time"

	"radar/internal/protocol"
)

// AgentRemoveGuard belongs to the trusted typed caller, never a client payload.
// Capture retains it without calling it. Apply must synchronously perform fresh
// scope/publication/consent admission and call execute exactly once while that
// admission is held across schedule pause AND registration unlink. It must not
// re-enter the serialized manager. Uncertainty never grants retry permission.
type AgentRemoveGuard func(context.Context, func() error) error

// AgentRemovePreview is immutable capture facts returned by value, not consent,
// a public transport or independently verified outcomes. Preparation only admits
// stopped registrations. Counts include retained schedules, not their private
// names, payloads, pending events or delivery/execution history.
type AgentRemovePreview struct {
	ID               string
	Schedules        int
	EnabledSchedules int
}

// AgentRemovePlan retains private native evidence and the original final caller
// guard. Copies share one-use admission. Never accept it from or serialize it to
// a client; retain it in the caller's existing bounded maintenance machinery.
type AgentRemovePlan interface {
	Preview() AgentRemovePreview
	// Original native monotonic deadline and conservative native-only budget.
	// The caller must additionally budget its own guard and adapter context.
	ExpiresAt() time.Time
	RetainedBytes() int
	Execute(context.Context) error
}

// AgentRemoveDiagnostics is distinct from first-provider generic agent control
// and stop. Effect-free preparation and native execution never implicitly stop,
// start a daemon, replay a request or clean up retained data/workspace/sandbox.
type AgentRemoveDiagnostics interface {
	Integration
	PrepareDiagnosticAgentRemove(context.Context, string, protocol.AgentWorkspaceBinding, AgentRemoveGuard) (AgentRemovePlan, error)
}

func (r Registry) AgentRemoveDiagnostics() (AgentRemoveDiagnostics, error) {
	var selected AgentRemoveDiagnostics
	count := 0
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(AgentRemoveDiagnostics); ok {
			selected = provider
			count++
		}
	}
	if count != 1 {
		return nil, fmt.Errorf("exact native agent-removal provider unavailable")
	}
	return selected, nil
}
