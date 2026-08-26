package integration

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"radar/internal/protocol"
)

type Integration interface {
	Descriptor() Descriptor
}

type Descriptor struct {
	Name         string
	Label        string
	DisplayOrder int
	CleanupOrder int
}

type Registry struct {
	integrations []Integration
	byName       map[string]Integration
}

func NewRegistry(integrations ...Integration) Registry {
	registry := Registry{
		integrations: append([]Integration(nil), integrations...),
		byName:       make(map[string]Integration, len(integrations)),
	}
	for _, candidate := range integrations {
		if candidate == nil {
			continue
		}
		registry.byName[candidate.Descriptor().Name] = candidate
	}
	return registry
}

func (r Registry) Sources() []Source {
	providers := make([]Source, 0, len(r.integrations))
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(Source); ok {
			providers = append(providers, provider)
		}
	}
	return providers
}

func (r Registry) ActionProviders() []ActionProvider {
	providers := make([]ActionProvider, 0)
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(ActionProvider); ok {
			providers = append(providers, provider)
		}
	}
	return providers
}

func (r Registry) PublishActivity(ctx context.Context, busy bool) error {
	for _, candidate := range r.integrations {
		provider, ok := candidate.(ActivityPublisher)
		if !ok {
			continue
		}
		if err := provider.PublishActivity(ctx, busy); err != nil {
			return err
		}
	}
	return nil
}

func (r Registry) Authenticators() []InteractiveAuthenticator {
	providers := make([]InteractiveAuthenticator, 0)
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(InteractiveAuthenticator); ok {
			providers = append(providers, provider)
		}
	}
	return providers
}

func (r Registry) EnsureAuthentication(ctx context.Context, req AuthenticationRequest) (AuthenticationResult, error) {
	result := AuthenticationResult{}
	for _, provider := range r.Authenticators() {
		providerResult, err := provider.EnsureAuthentication(ctx, req)
		if err != nil {
			return result, err
		}
		result.Changed = result.Changed || providerResult.Changed
	}
	return result, nil
}

func (r Registry) RateLimitReporter() (RateLimitReporter, error) {
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(RateLimitReporter); ok {
			return provider, nil
		}
	}
	return nil, fmt.Errorf("rate-limit integration is not registered")
}

func (r Registry) TaskFilterProviders() []TaskFilterProvider {
	providers := make([]TaskFilterProvider, 0)
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(TaskFilterProvider); ok {
			providers = append(providers, provider)
		}
	}
	return providers
}

func (r Registry) FilterTasks(tasks []protocol.Task, logger *slog.Logger) []protocol.Task {
	for _, provider := range r.TaskFilterProviders() {
		tasks = provider.FilterTasks(tasks, logger)
	}
	return tasks
}

func (r Registry) CleanupProviders() []CleanupProvider {
	providers := make([]CleanupProvider, 0)
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(CleanupProvider); ok {
			providers = append(providers, provider)
		}
	}
	sort.SliceStable(providers, func(i, j int) bool {
		return providers[i].Descriptor().CleanupOrder < providers[j].Descriptor().CleanupOrder
	})
	return providers
}

func (r Registry) RuntimeResourceName(ref protocol.SourceRef) (string, bool) {
	for _, candidate := range r.integrations {
		provider, ok := candidate.(RuntimeProvider)
		if !ok {
			continue
		}
		if name, handled := provider.ResourceName(ref); handled {
			return name, true
		}
	}
	return "", false
}

func (r Registry) WorkspaceSeeder(ref protocol.SourceRef) (WorkspaceSeedProvider, bool) {
	for _, candidate := range r.integrations {
		provider, ok := candidate.(WorkspaceSeedProvider)
		if ok && provider.CanSeedWorkspace(ref) {
			return provider, true
		}
	}
	return nil, false
}

func (r Registry) WorkspaceManager() (WorkspaceManager, error) {
	providers := make([]WorkspaceManager, 0, 1)
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(WorkspaceManager); ok {
			providers = append(providers, provider)
		}
	}
	if len(providers) != 1 {
		return nil, fmt.Errorf("expected exactly one workspace manager, found %d", len(providers))
	}
	return providers[0], nil
}

func (r Registry) Workspace() (WorkspaceProvider, error) {
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(WorkspaceProvider); ok {
			return provider, nil
		}
	}
	return nil, fmt.Errorf("workspace integration is not registered")
}

func (r Registry) Multiplexer() (MultiplexerProvider, error) {
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(MultiplexerProvider); ok {
			return provider, nil
		}
	}
	return nil, fmt.Errorf("multiplexer integration is not registered")
}

func (r Registry) TaskAuthoring() (TaskAuthoringProvider, error) {
	providers := make([]TaskAuthoringProvider, 0, 1)
	for _, candidate := range r.integrations {
		if provider, ok := candidate.(TaskAuthoringProvider); ok {
			providers = append(providers, provider)
		}
	}
	if len(providers) != 1 {
		return nil, fmt.Errorf("exactly one task authoring integration is required; found %d", len(providers))
	}
	return providers[0], nil
}
