package integration

import (
	"context"
	"fmt"
	"log/slog"

	"radar/internal/protocol"
)

type RateLimitReporter interface {
	Integration
	RateLimitSummary(ctx context.Context, logger *slog.Logger) (string, error)
}

// WorkspaceRegistrationDiagnostics is observation-only. Implementations project
// their registration allowlist without probing runtimes or collecting sources.
// Identity fields retain full values for scope matching and stable child IDs;
// child lists are complete. Callers must bound their final diagnostic output.
// Ownership and lifecycle fields are populated separately by their owning
// services, not by this capability.
type WorkspaceRegistrationDiagnostics interface {
	Integration
	DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error)
}

// RuntimeProbeProvider runs only bounded observation, never collection, login,
// startup or maintenance. The caller must resolve exact registered scope first.
type RuntimeProbeProvider interface {
	Integration
	DiagnosticRuntime(context.Context, string) protocol.DiagnosticRuntimeObservation
}

func (r Registry) RuntimeProbe(source string) (RuntimeProbeProvider, error) {
	var selected []RuntimeProbeProvider
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(RuntimeProbeProvider); ok && provider.Descriptor().Name == source {
			selected = append(selected, provider)
		}
	}
	if len(selected) != 1 {
		return nil, fmt.Errorf("runtime probe unsupported")
	}
	return selected[0], nil
}

func (r Registry) WorkspaceDiagnostics() (WorkspaceRegistrationDiagnostics, error) {
	var providers []WorkspaceRegistrationDiagnostics
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(WorkspaceRegistrationDiagnostics); ok {
			providers = append(providers, provider)
		}
	}
	if len(providers) != 1 {
		return nil, fmt.Errorf("workspace registration diagnostics unavailable: expected one provider, found %d", len(providers))
	}
	return providers[0], nil
}
