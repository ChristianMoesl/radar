package integration

import (
	"context"
	"fmt"
	"time"

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

// AgentStopObservation is independently read native postcondition evidence, not
// the executor's return, attribution to this attempt or permission to retry.
// Empty fields/zero time mean unknown/not observed. A stopped definition matches
// the captured native definition with Desired=false; unknown private extensions
// are not definition fields. Original-preserved binds the original raw file.
// Runner absence requires no listener, no live recorded owner and (if captured)
// no live original snapshot PID. These point-in-time checks attest neither PID
// incarnation nor completed work, released leases or workspace/sandbox absence.
type AgentStopObservation struct {
	ObservedAt   time.Time
	Registration AgentStopRegistrationState
	Runner       AgentStopRunnerState
}

type AgentStopRegistrationState string
type AgentStopRunnerState string

const (
	AgentStopRegistrationUnknown  AgentStopRegistrationState = ""
	AgentStopRegistrationStopped  AgentStopRegistrationState = "matching_stopped_definition"
	AgentStopRegistrationOriginal AgentStopRegistrationState = "original_preserved"
	AgentStopRunnerUnknown        AgentStopRunnerState       = ""
	AgentStopRunnerPresent        AgentStopRunnerState       = "matching_listener_present"
	AgentStopRunnerAbsent         AgentStopRunnerState       = "no_listener_and_no_live_observed_owner"
)

// AgentStopPlan retains native-only evidence and the original caller guard.
// Copies share one-use admission. It must not be serialized or accepted from a
// client; the typed caller must retain it in its existing bounded plan machinery.
type AgentStopPlan interface {
	Preview() AgentStopPreview
	// Retention metadata is private admission evidence, never client input.
	// ExpiresAt preserves the native monotonic deadline. RetainedBytes is a
	// conservative native-only budget; the caller must also budget its guard
	// closure and adapter context. Shared manager state is not a per-plan copy.
	ExpiresAt() time.Time
	RetainedBytes() int
	Execute(context.Context) error
	// Shared immutable observation published after the first attempted native
	// execution finishes. Inspection performs no I/O and cannot restore admission.
	// Cancelled/expired/pre-effect-refused attempts retain unknown evidence.
	Observation() AgentStopObservation
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
