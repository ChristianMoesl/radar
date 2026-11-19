package workspace

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"radar/internal/config"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

// The shared directory is a separate recursive deletion root, not a child of
// the anchor admitted by Service. Retain its exclusive deletion admission for
// the whole bundle, including failures in providers preceding anchor cleanup.
type sharedDirectoryCleanupAdmission struct {
	root       string
	paths      map[string]string
	identities map[string]string
}

type admittedWorkspaceCleanup struct {
	Source
	runner    Runner
	admission *sharedDirectoryCleanupAdmission
}

func (s Source) AdmitCleanup(ctx context.Context, targets []protocol.CleanupTarget, mode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	return s.admitCleanupWithRunner(ctx, ExecRunner{}, targets, mode)
}

func (s Source) admitCleanupWithRunner(ctx context.Context, runner Runner, targets []protocol.CleanupTarget, mode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	bound := &admittedWorkspaceCleanup{Source: s, runner: runner}
	if mode == integration.CleanupRetire {
		return bound, func() {}, nil
	}
	root, err := DefaultRoot()
	if err != nil {
		return nil, nil, err
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return nil, nil, err
	}
	var groups []workspacegroup.Workspace
	for _, target := range targets {
		if target.Kind != "workspace" {
			continue
		}
		if group, found := workspacegroup.FindByID(registry, strings.TrimPrefix(target.SourceRefID, "workspace:")); found {
			groups = append(groups, group)
		}
	}
	admission, release, err := acquireSharedDirectoryCleanup(ctx, root, groups)
	if err != nil {
		return nil, nil, err
	}
	// Only short metadata decisions use the note transaction here. Do not scan
	// trees or run providers while holding it; all resource spans remain owned
	// by their existing cleanup methods.
	err = workspacegroup.WithNoteLock(root, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if !sameCleanPath(root, ExpandPath(cfg.Workspace.RootDir)) {
			return fmt.Errorf("configured workspace root changed; preview cleanup again")
		}
		registry, err := workspacegroup.Load(root)
		if err != nil {
			return err
		}
		for _, expected := range groups {
			group, found := workspacegroup.FindByID(registry, expected.ID)
			if !found {
				return fmt.Errorf("workspace registration changed; preview cleanup again")
			}
			if err := admission.validate(root, group); err != nil {
				return err
			}
			if err := validateSharedDirectoryCleanup(root, registry, group); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
	if err != nil {
		release()
		return nil, nil, err
	}
	bound.admission = admission
	return bound, release, nil
}

func (b *admittedWorkspaceCleanup) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return b.Source.cleanupWithRunnerAndAdmission(ctx, b.runner, req, b.admission)
}

func cleanupSharedDirectoryPath(group workspacegroup.Workspace) (string, error) {
	if group.Sandbox == nil {
		return "", nil
	}
	path := group.Sandbox.SharedDirectory
	if err := workspacegroup.ValidateSharedDirectory(group.Path, path); err != nil {
		return "", err
	}
	return path, nil
}

func acquireSharedDirectoryCleanup(ctx context.Context, root string, groups []workspacegroup.Workspace) (*sharedDirectoryCleanupAdmission, func(), error) {
	admission := &sharedDirectoryCleanupAdmission{root: root, paths: map[string]string{}, identities: map[string]string{}}
	identities := map[string]bool{}
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		path, err := cleanupSharedDirectoryPath(group)
		if err != nil {
			return nil, nil, err
		}
		admission.paths[group.ID] = path
		if path != "" {
			identity, err := workspacelease.CanonicalPath(path)
			if err != nil {
				return nil, nil, err
			}
			admission.identities[group.ID] = identity
			identities[identity] = true
		}
	}
	ordered := make([]string, 0, len(identities))
	for identity := range identities {
		ordered = append(ordered, identity)
	}
	sort.Strings(ordered)
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, identity := range ordered {
		if err := ctx.Err(); err != nil {
			release()
			return nil, nil, err
		}
		owned, err := workspacelease.Acquire(identity, true)
		if err != nil {
			release()
			return nil, nil, err
		}
		releases = append(releases, owned)
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, nil, err
	}
	return admission, release, nil
}

func (a *sharedDirectoryCleanupAdmission) validate(root string, group workspacegroup.Workspace) error {
	path, err := cleanupSharedDirectoryPath(group)
	if err != nil {
		return err
	}
	expected, found := a.paths[group.ID]
	if !sameCleanPath(root, a.root) || !found || path != expected {
		return fmt.Errorf("workspace shared directory changed; preview cleanup again")
	}
	if path != "" {
		identity, err := workspacelease.CanonicalPath(path)
		if err != nil {
			return err
		}
		if identity != a.identities[group.ID] {
			return fmt.Errorf("workspace shared directory identity changed; preview cleanup again")
		}
	}
	return nil
}

func validateSharedDirectoryCleanup(root string, registry workspacegroup.Registry, group workspacegroup.Workspace) error {
	path, err := cleanupSharedDirectoryPath(group)
	if err != nil || path == "" {
		return err
	}
	// Apply the same registered/configured protected-reference policy to the
	// separate deletion root. Its owner's normal shared mount is not external.
	shared := group
	shared.Path = path
	if err := validateDiscardingAnchor(root, registry, shared); err != nil {
		return fmt.Errorf("shared directory cleanup: %w", err)
	}
	return nil
}

var _ integration.CleanupAdmissionProvider = Source{}
