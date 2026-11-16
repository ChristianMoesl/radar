package tmux

import (
	"context"
	"time"

	"radar/internal/command"
	"radar/internal/integration"
	"radar/internal/protocol"
)

var _ integration.RuntimeProbeProvider = Source{}

func (Source) DiagnosticRuntime(ctx context.Context, name string) protocol.DiagnosticRuntimeObservation {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := command.Output(ctx, "tmux", []string{"list-sessions", "-F", sessionFormat}, nil)
	return diagnosticRuntime(ctx, name, string(output), err)
}

func diagnosticRuntime(ctx context.Context, name, output string, problem error) protocol.DiagnosticRuntimeObservation {
	view := protocol.DiagnosticRuntimeObservation{Presence: "unknown", State: "unknown", Problem: "probe_failed"}
	if ctx.Err() != nil {
		view.Problem = "cancelled_or_timed_out"
		return view
	}
	// A failed command, including no-server errors, is not a complete listing.
	if problem != nil {
		return view
	}
	rows, err := parseSessions(output)
	if err != nil {
		return view
	}
	var found *session
	for _, row := range rows {
		if row.Name == "" {
			return view
		}
		if row.Name != name {
			continue
		}
		if found != nil {
			return view
		}
		copy := row
		found = &copy
	}
	view.Problem = ""
	if found == nil {
		view.Presence = "absent"
		return view
	}
	view.Presence, view.ID, view.State = "present", found.sourceRefID(), "running"
	return view
}
