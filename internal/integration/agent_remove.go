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

// AgentRemoveObservation contains only bounded native postcondition evidence,
// never handler success, consent, payloads or permission to retry. Empty enum
// values and a zero timestamp mean unknown/not observed. Presence means the
// ORIGINAL captured registration is still present, not a replacement owner.
// Paused means the complete decoded native schedule state matches the captured
// state with only Enabled cleared (or the original empty/missing file remains).
// Unchanged means the original checkpoint is preserved, not that removal ran.
// These are point-in-time observations, not an atomic filesystem snapshot or
// evidence about runner/workspace/sandbox absence, other owners or cleanup.
type AgentRemoveObservation struct {
	ObservedAt   time.Time
	Registration AgentRemoveRegistrationState
	Schedules    AgentRemoveScheduleState
}

type AgentRemoveRegistrationState string
type AgentRemoveScheduleState string

const (
	AgentRemoveRegistrationUnknown AgentRemoveRegistrationState = ""
	AgentRemoveRegistrationPresent AgentRemoveRegistrationState = "original_present"
	AgentRemoveRegistrationAbsent  AgentRemoveRegistrationState = "absent"
	AgentRemoveSchedulesUnknown    AgentRemoveScheduleState     = ""
	AgentRemoveSchedulesPaused     AgentRemoveScheduleState     = "paused_or_empty_preserved"
	AgentRemoveSchedulesUnchanged  AgentRemoveScheduleState     = "original_preserved"
)

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
	// Shared immutable result published after the first execution finishes.
	// Before that, or when cancelled/expired/unsafe, evidence remains unknown.
	// Inspecting it never performs I/O, restores admission or retries execution.
	// Execute's error and these independent observations must not be conflated.
	Observation() AgentRemoveObservation
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
