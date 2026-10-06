package cleanup

import (
	"context"
	"errors"
	"fmt"

	"radar/internal/integration"
	"radar/internal/protocol"
)

var ErrNoResources = errors.New("selected task has no local resources to clean up")

// Service plans and executes cleanup across registered integrations.
type Service struct {
	providers []integration.CleanupProvider
}

type ExecuteOptions struct {
	Mode integration.CleanupMode
}

func New(providers []integration.CleanupProvider) Service {
	return Service{providers: append([]integration.CleanupProvider(nil), providers...)}
}

func (s Service) Preview(ctx context.Context, task protocol.Task, mode integration.CleanupMode) (protocol.CleanupPreview, error) {
	targets := make([]protocol.CleanupTarget, 0)
	for _, provider := range s.providers {
		providerTargets, err := provider.PreviewCleanup(ctx, integration.CleanupPreviewRequest{Task: task, Mode: mode})
		if err != nil {
			return protocol.CleanupPreview{}, err
		}
		targets = append(targets, providerTargets...)
	}
	if len(targets) == 0 {
		return protocol.CleanupPreview{}, ErrNoResources
	}
	return protocol.CleanupPreview{TaskID: task.ID, TaskTitle: task.Title, Targets: targets}, nil
}

func (s Service) Execute(ctx context.Context, preview protocol.CleanupPreview, options ExecuteOptions) (protocol.CleanupResult, error) {
	if options.Mode != integration.CleanupSafe && options.Mode != integration.CleanupConfirmed && options.Mode != integration.CleanupExpired {
		return protocol.CleanupResult{}, fmt.Errorf("invalid cleanup mode")
	}
	if options.Mode != integration.CleanupConfirmed {
		if messages := AutomaticBlockingMessages(preview.Targets, options.Mode == integration.CleanupExpired); len(messages) > 0 {
			return protocol.CleanupResult{}, errors.New(messages[0])
		}
	}
	if len(preview.Targets) == 0 {
		return protocol.CleanupResult{}, fmt.Errorf("cleanup targets are required")
	}
	result := protocol.CleanupResult{TaskID: preview.TaskID, Targets: make([]protocol.CleanupTarget, 0, len(preview.Targets))}
	for _, target := range preview.Targets {
		provider, ok := s.provider(target.Source)
		if !ok {
			return result, fmt.Errorf("source %q cannot clean up local resources", target.Source)
		}
		cleaned, err := provider.Cleanup(ctx, integration.CleanupRequest{Target: target, Mode: options.Mode})
		if err != nil {
			return result, fmt.Errorf("cleanup stopped after %d of %d resources: %w", len(result.Targets), len(preview.Targets), err)
		}
		result.Targets = append(result.Targets, cleaned)
	}
	return result, nil
}

func (s Service) provider(sourceName string) (integration.CleanupProvider, bool) {
	for _, provider := range s.providers {
		if provider.Descriptor().Name == sourceName {
			return provider, true
		}
	}
	return nil, false
}
