package cleanup

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

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
			var resources []string
			for _, ref := range task.SourceRefs {
				if ref.Source == provider.Descriptor().Name {
					resources = append(resources, resourceLabel(protocol.CleanupTarget{Source: ref.Source, Kind: ref.Kind, Path: ref.Path, ResourceID: ref.ID}))
				}
			}
			if len(resources) == 0 {
				resources = append(resources, provider.Descriptor().Name)
			}
			return protocol.CleanupPreview{}, fmt.Errorf("resource check failed for %s: %w; refresh Radar and retry cleanup", strings.Join(resources, ", "), err)
		}
		// Own each provider result before calling the next provider. Later planner
		// or lifecycle callbacks must not rewrite earlier operations or warnings
		// through borrowed maps/slices, including before daemon plan freezing.
		for _, target := range providerTargets {
			target.Operation = maps.Clone(target.Operation)
			target.Safety = slices.Clone(target.Safety)
			targets = append(targets, target)
		}
	}
	if len(targets) == 0 {
		return protocol.CleanupPreview{}, ErrNoResources
	}
	return protocol.CleanupPreview{TaskID: task.ID, TaskTitle: task.Title, Targets: targets}, nil
}

// CheckBundle runs the same mode, capability, ownership, lease, native admission
// and whole-bundle preflight checks as Execute, but never calls Cleanup. It
// releases all admission before returning; success is neither a reservation nor
// deletion authority. Providers still perform last-moment checks during Execute.
// This is not a read-only probe: native checks may create lock files, inspect
// runtimes or update metadata. The caller supplies a fresh native preview and is
// responsible for its scope and any required permission to run these checks.
func (s Service) CheckBundle(ctx context.Context, preview protocol.CleanupPreview, options ExecuteOptions) error {
	_, err := s.runBundle(ctx, preview, options, false)
	return err
}

func (s Service) Execute(ctx context.Context, preview protocol.CleanupPreview, options ExecuteOptions) (protocol.CleanupResult, error) {
	return s.runBundle(ctx, preview, options, true)
}

// Keep a single guard path. In particular, checking must not call Execute as a
// dry run or maintain a second approximation of production admission policy.
func (s Service) runBundle(ctx context.Context, preview protocol.CleanupPreview, options ExecuteOptions, dispatch bool) (result protocol.CleanupResult, err error) {
	observation := protocol.OperationCleanupExecution{Stage: "admission"}
	defer func() { result.ExecutionObservation = &observation }()
	if dispatch {
		defer phasetiming.Start(ctx, "cleanup_execution")()
	}
	release, lockErr := operationlock.Acquire(false)
	if lockErr != nil {
		return protocol.CleanupResult{}, lockErr
	}
	defer release()
	if options.Mode != integration.CleanupSafe && options.Mode != integration.CleanupConfirmed && options.Mode != integration.CleanupExpired && options.Mode != integration.CleanupRetire {
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
	result = protocol.CleanupResult{TaskID: preview.TaskID, Targets: make([]protocol.CleanupTarget, 0, len(preview.Targets))}
	// Resolve the entire bundle before leases or provider callbacks. A missing
	// or ambiguous later provider must not leave an earlier resource deleted.
	providers := make(map[string]integration.CleanupProvider)
	for _, target := range preview.Targets {
		if _, ok := providers[target.Source]; ok {
			continue
		}
		provider, ok := s.provider(target.Source)
		if !ok {
			return result, remainingError(preview, result, fmt.Errorf("source %q cannot unambiguously clean up local resources", target.Source))
		}
		providers[target.Source] = provider
	}
	// Acquire the entire set before deleting any resource. Workspace providers
	// include the anchor, so its shared agent lease protects the full bundle.
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	seen := map[string]bool{}
	seenSandboxes := map[string]bool{}
	observation.Stage = "ownership_and_leases"
	for _, target := range preview.Targets {
		if err := ctx.Err(); err != nil {
			return result, remainingError(preview, result, err)
		}
		// A sandbox can be shared by different paths, or have no path at all.
		// Admit its reusable name before any provider runs, independently of
		// canonical path deduplication below.
		if target.Source == "sbx" && target.Kind == "sandbox" {
			name := strings.TrimSpace(target.ResourceID)
			if !seenSandboxes[name] {
				release, err := workspacelease.AcquireSandbox(name, true)
				if err != nil {
					return result, remainingError(preview, result, fmt.Errorf("%s: %w", resourceLabel(target), err))
				}
				releases = append(releases, release)
				seenSandboxes[name] = true
			}
		}
		if target.Path != "" {
			path, err := workspacelease.CanonicalPath(target.Path)
			if err != nil {
				return result, remainingError(preview, result, fmt.Errorf("%s: %w", resourceLabel(target), err))
			}
			if err := agentservice.CheckWorkspaceRemoval(target.Path); err != nil {
				return result, remainingError(preview, result, fmt.Errorf("%s: %w", resourceLabel(target), err))
			}
			if seen[path] {
				continue
			}
			release, err := workspacelease.Acquire(path, true)
			if err != nil {
				return result, remainingError(preview, result, fmt.Errorf("%s: %w", resourceLabel(target), err))
			}
			releases = append(releases, release)
			seen[path] = true
		}
	}
	// Providers may discover the actual owner behind a member/fallback target.
	// Reuse only this execution's real path leases, never a guessed busy lock.
	// Invalidate before releasing the bundle, including all failure paths.
	ctx, invalidatePaths := withAdmittedPaths(ctx, seen)
	defer invalidatePaths()
	// Native aliases are provider-owned: resolve their full set before even
	// the first provider's preflight or destructive call can run.
	admitted := make(map[string]integration.CleanupProvider)
	for _, provider := range s.providers {
		if admission, ok := provider.(integration.CleanupAdmissionProvider); ok {
			var targets []protocol.CleanupTarget
			for _, target := range preview.Targets {
				if target.Source == provider.Descriptor().Name {
					targets = append(targets, target)
				}
			}
			if len(targets) == 0 {
				continue
			}
			bound, release, err := admission.AdmitCleanup(ctx, targets, options.Mode)
			if err != nil {
				return result, remainingError(preview, result, fmt.Errorf("%s cleanup admission: %w", provider.Descriptor().Name, err))
			}
			releases = append(releases, release)
			admitted[provider.Descriptor().Name] = bound
		}
	}
	observation.Stage = "bundle_preflight"
	preflightDone := phasetiming.Start(ctx, "cleanup_bundle_preflight")
	for _, provider := range s.providers {
		if err := ctx.Err(); err != nil {
			preflightDone()
			return result, remainingError(preview, result, err)
		}
		if preflight, ok := provider.(integration.CleanupPreflightProvider); ok {
			if err := preflight.PreflightCleanup(ctx, preview.Targets, options.Mode); err != nil {
				preflightDone()
				return result, remainingError(preview, result, fmt.Errorf("%s cleanup preflight: %w", provider.Descriptor().Name, err))
			}
		}
	}
	preflightDone()
	if !dispatch {
		// A final preflight can cancel even when it returns nil. Do not report
		// successful evidence after that cancellation; no target was attempted.
		if err := ctx.Err(); err != nil {
			return result, remainingError(preview, result, err)
		}
		return result, nil
	}
	observation.Stage = "target_dispatch"
	for _, target := range preview.Targets {
		if err := ctx.Err(); err != nil {
			return result, remainingError(preview, result, err)
		}
		provider := providers[target.Source]
		if bound, ok := admitted[target.Source]; ok {
			provider = bound
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
		observation.Stage = "provider_call"
		observation.AttemptedTargets++
		cleaned, err := provider.Cleanup(ctx, integration.CleanupRequest{Target: target, Mode: options.Mode})
		done()
		if err != nil {
			return result, remainingError(preview, result, fmt.Errorf("%s: %w", resourceLabel(target), err))
		}
		result.Targets = append(result.Targets, cleaned)
		observation.Stage = "target_dispatch"
	}
	observation.Stage = "completed"
	return result, nil
}

func resourceLabel(target protocol.CleanupTarget) string {
	identity := target.Path
	if identity == "" {
		identity = target.ResourceID
	}
	label := strings.TrimSpace(target.Source + " " + target.Kind)
	if identity != "" {
		label += " " + identity
	}
	return label
}

func remainingError(preview protocol.CleanupPreview, result protocol.CleanupResult, problem error) error {
	var remaining []string
	for _, target := range preview.Targets[len(result.Targets):] {
		remaining = append(remaining, resourceLabel(target))
	}
	return fmt.Errorf("cleanup stopped after %d of %d resources: %w; remaining resources (including any partially removed target): %s; refresh Radar and preview cleanup again to retry", len(result.Targets), len(preview.Targets), problem, strings.Join(remaining, ", "))
}

func (s Service) provider(sourceName string) (integration.CleanupProvider, bool) {
	var selected integration.CleanupProvider
	for _, provider := range s.providers {
		if provider.Descriptor().Name == sourceName {
			if selected != nil {
				return nil, false
			}
			selected = provider
		}
	}
	return selected, selected != nil
}
