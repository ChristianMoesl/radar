package cleanup

import (
	"context"
	"errors"
	"fmt"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/operationlock"
	"radar/internal/phasetiming"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
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
	defer phasetiming.Start(ctx, "cleanup_preview")()
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
	defer phasetiming.Start(ctx, "cleanup_execution")()
	release, lockErr := operationlock.Acquire(false)
	if lockErr != nil {
		return protocol.CleanupResult{}, lockErr
	}
	defer release()
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
	// Acquire the entire set before deleting any resource. Workspace providers
	// include the anchor, so its shared agent lease protects the full bundle.
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	seen := map[string]bool{}
	for _, target := range preview.Targets {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if target.Path != "" && !seen[target.Path] {
			if err := agentservice.CheckWorkspaceRemoval(target.Path); err != nil {
				return result, err
			}
			release, err := workspacelease.Acquire(target.Path, true)
			if err != nil {
				return result, err
			}
			releases = append(releases, release)
			seen[target.Path] = true
		}
	}
	preflightDone := phasetiming.Start(ctx, "cleanup_bundle_preflight")
	for _, provider := range s.providers {
		if err := ctx.Err(); err != nil {
			preflightDone()
			return result, err
		}
		if preflight, ok := provider.(integration.CleanupPreflightProvider); ok {
			if err := preflight.PreflightCleanup(ctx, preview.Targets, options.Mode); err != nil {
				preflightDone()
				return result, err
			}
		}
	}
	preflightDone()
	for _, target := range preview.Targets {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		provider, ok := s.provider(target.Source)
		if !ok {
			return result, fmt.Errorf("source %q cannot clean up local resources", target.Source)
		}
		phase := "cleanup_resource"
		switch target.Kind {
		case "worktree":
			phase = "cleanup_worktree"
		case "sandbox":
			phase = "cleanup_runtime"
		case "workspace":
			phase = "cleanup_anchor"
		}
		done := phasetiming.Start(ctx, phase)
		cleaned, err := provider.Cleanup(ctx, integration.CleanupRequest{Target: target, Mode: options.Mode})
		done()
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
