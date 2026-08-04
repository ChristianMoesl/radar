package integration

import (
	"fmt"
	"sort"
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
