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
