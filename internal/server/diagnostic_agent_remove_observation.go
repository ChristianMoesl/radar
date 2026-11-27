package server

import (
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

// Project only finite, fresh native evidence from this consumed apply. Never
// derive postconditions from counts/handler error or disclose provider strings.
// Invalid native evidence becomes unknown, without another read or execution.
func projectAgentRemoveObservation(o integration.AgentRemoveObservation, started, expires time.Time) protocol.DiagnosticAgentRemoveObservation {
	unknown := protocol.UnknownDiagnosticAgentRemoveObservation()
	if o.ObservedAt.IsZero() || o.ObservedAt.Before(started) || o.ObservedAt.After(expires) || o.ObservedAt.After(time.Now()) || o.ObservedAt.Year() < 1 || o.ObservedAt.Year() > 9999 {
		return unknown
	}
	v := protocol.DiagnosticAgentRemoveObservation{Evidence: "bounded_native_checkpoint_reads", Registration: "unknown", Schedules: "unknown", ObservedAt: o.ObservedAt.UTC().Format(time.RFC3339Nano)}
	switch o.Registration {
	case integration.AgentRemoveRegistrationUnknown:
	case integration.AgentRemoveRegistrationPresent:
		v.Registration = "original_present"
	case integration.AgentRemoveRegistrationAbsent:
		v.Registration = "absent"
	default:
		return unknown
	}
	switch o.Schedules {
	case integration.AgentRemoveSchedulesUnknown:
	case integration.AgentRemoveSchedulesPaused:
		v.Schedules = "paused_or_empty_preserved"
	case integration.AgentRemoveSchedulesUnchanged:
		v.Schedules = "original_preserved"
	default:
		return unknown
	}
	return v
}
