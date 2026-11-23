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

// AgentSupervisionDiagnostics reads current-daemon manager memory only. Busy or
// unsupported managers return no evidence, not a healthy/no-retry conclusion.
type AgentSupervisionDiagnostics interface {
	Integration
	DiagnosticAgentSupervision([]string) map[string]protocol.DiagnosticAgentSupervision
}

func (r Registry) DiagnosticAgentSupervision(ids []string) map[string]protocol.DiagnosticAgentSupervision {
	var providers []AgentSupervisionDiagnostics
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(AgentSupervisionDiagnostics); ok {
			providers = append(providers, provider)
		}
	}
	if len(ids) == 0 || len(providers) != 1 {
		return nil
	}
	return providers[0].DiagnosticAgentSupervision(ids)
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

// WorkspacePolicyDiagnostics reads one exact native registration. Scope is a
// host-resolved anchor, never a path to configuration or referenced files.
// Implementations must not probe runtimes, read repository settings or execute.
type WorkspacePolicyDiagnostics interface {
	WorkspaceRegistrationDiagnostics
	DiagnosticWorkspacePolicy(workspaceID, scope string) (protocol.DiagnosticWorkspacePolicy, error)
}

// SandboxRecreationPreviewDiagnostics explicitly invokes the native recreation
// planner for one registered workspace. It can read runtime/secret/policy
// metadata and local recipe accessibility; it must never execute recreation.
type SandboxRecreationPreviewDiagnostics interface {
	WorkspaceRegistrationDiagnostics
	DiagnosticRecreationPreview(context.Context, string, string) (protocol.DiagnosticRecreationPreview, error)
}

// RepositoryPolicyDiagnostics reads only one exact registered member's fixed
// current repository configuration, never referenced files, runtimes or actions.
// It is a separate explicit file-access capability, not WorkspacePolicyDiagnostics.
type RepositoryPolicyDiagnostics interface {
	WorkspaceRegistrationDiagnostics
	DiagnosticRepositoryPolicy(context.Context, string, string, string) (protocol.DiagnosticRepositoryPolicy, error)
}

// WorkspaceRetirementPreview uses only the native registration/path planner for
// one workspace. Unlike full cleanup previews it must not fetch Git refs, probe
// runtimes, acquire leases, refresh or mutate. Targets are proposals, not authority.
type WorkspaceRetirementPreview interface {
	WorkspaceRegistrationDiagnostics
	DiagnosticRetirementTargets(context.Context, protocol.Task, string) ([]protocol.CleanupTarget, error)
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

// DiagnosticIntegrations projects static registration/interfaces only. Never call
// Status, Collect, authentication, rate-limit reporting, setup or tool discovery
// here: those methods can contact providers or read credentials.
func (r Registry) DiagnosticIntegrations() []protocol.DiagnosticIntegration {
	items := make([]protocol.DiagnosticIntegration, 0, len(r.integrations))
	for _, candidate := range r.integrations {
		if candidate == nil {
			continue
		}
		descriptor := candidate.Descriptor()
		_, collection := candidate.(Source)
		_, status := candidate.(StatusReporter)
		_, auth := candidate.(InteractiveAuthenticator)
		_, bindings := candidate.(BoundSourceResolver)
		_, cleanup := candidate.(CleanupProvider)
		_, runtimeProbe := candidate.(RuntimeProbeProvider)
		_, rateLimit := candidate.(RateLimitReporter)
		items = append(items, protocol.DiagnosticIntegration{Name: descriptor.Name, Label: descriptor.Label, Capabilities: &protocol.DiagnosticIntegrationCapabilities{Collection: collection, StatusCheck: status, InteractiveAuthentication: auth, BoundResolution: bindings, Cleanup: cleanup, RuntimeProbe: runtimeProbe, RateLimit: rateLimit}})
	}
	return items
}
