package cleanup

import (
	"context"
	"fmt"

	"radar/internal/integration"
	"radar/internal/protocol"
)

// Service plans and executes cleanup across registered integrations.
type Service struct {
	providers []integration.CleanupProvider
}

type ExecuteOptions struct {
	Force bool
}

func New(providers []integration.CleanupProvider) Service {
	return Service{providers: append([]integration.CleanupProvider(nil), providers...)}
}

func (s Service) Preview(ctx context.Context, task protocol.Task) (protocol.CleanupPreview, error) {
	targets := make([]protocol.CleanupTarget, 0)
	for _, provider := range s.providers {
		providerTargets, err := provider.PreviewCleanup(ctx, integration.CleanupPreviewRequest{Task: task})
		if err != nil {
			return protocol.CleanupPreview{}, err
		}
		targets = append(targets, providerTargets...)
	}
	if len(targets) == 0 {
		return protocol.CleanupPreview{}, fmt.Errorf("selected task has no local resources to clean up")
	}
	return protocol.CleanupPreview{TaskID: task.ID, TaskTitle: task.Title, Targets: targets}, nil
}

func (s Service) Execute(ctx context.Context, preview protocol.CleanupPreview, options ExecuteOptions) (protocol.CleanupResult, error) {
	if len(preview.Targets) == 0 {
		return protocol.CleanupResult{}, fmt.Errorf("cleanup targets are required")
	}
	result := protocol.CleanupResult{TaskID: preview.TaskID, Targets: make([]protocol.CleanupTarget, 0, len(preview.Targets))}
	for _, target := range preview.Targets {
		provider, ok := s.provider(target.Source)
		if !ok {
			return result, fmt.Errorf("source %q cannot clean up local resources", target.Source)
		}
		cleaned, err := provider.Cleanup(ctx, integration.CleanupRequest{Target: target, Force: options.Force})
		if err != nil {
			return result, fmt.Errorf("cleanup stopped after %d of %d resources: %w", len(result.Targets), len(preview.Targets), err)
		}
		result.Targets = append(result.Targets, cleaned)
	}
	return result, nil
}

func (s Service) provider(sourceName string) (integration.CleanupProvider, bool) {
	for _, provider := range s.providers {
		source, ok := provider.(integration.Source)
		if ok && source.Name() == sourceName {
			return provider, true
		}
	}
	return nil, false
}
