package server

import (
	"context"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func stopObservationInputsLive(ctx context.Context, expires time.Time) bool {
	if ctx.Err() != nil || !time.Now().Before(expires) {
		return false
	}
	deadline, ok := ctx.Deadline()
	return !ok || time.Now().Before(deadline)
}

// Project only finite native facts captured during this consumed execution.
// Invalid evidence becomes unknown, never a provider string, extra read or retry.
func projectAgentStopObservation(o integration.AgentStopObservation, started, expires time.Time) protocol.DiagnosticAgentStopObservation {
	unknown := protocol.UnknownDiagnosticAgentStopObservation()
	if o.ObservedAt.IsZero() || o.ObservedAt.Before(started) || o.ObservedAt.After(expires) || o.ObservedAt.After(time.Now()) || o.ObservedAt.Year() < 1 || o.ObservedAt.Year() > 9999 {
		return unknown
	}
	v := protocol.DiagnosticAgentStopObservation{Evidence: "bounded_native_stop_reads", Registration: "unknown", Runner: "unknown", ObservedAt: o.ObservedAt.UTC().Format(time.RFC3339Nano)}
	switch o.Registration {
	case integration.AgentStopRegistrationUnknown:
	case integration.AgentStopRegistrationStopped:
		v.Registration = "matching_stopped_definition"
	case integration.AgentStopRegistrationOriginal:
		v.Registration = "original_preserved"
	default:
		return unknown
	}
	switch o.Runner {
	case integration.AgentStopRunnerUnknown:
	case integration.AgentStopRunnerPresent:
		v.Runner = "matching_listener_present"
	case integration.AgentStopRunnerAbsent:
		v.Runner = "no_listener_and_no_live_observed_owner"
	default:
		return unknown
	}
	return v
}
