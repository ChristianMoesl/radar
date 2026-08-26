package workspace

import (
	"context"
	"log/slog"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
)

func (Source) DefaultRoot() (string, error)  { return DefaultRoot() }
func (Source) ExpandPath(path string) string { return ExpandPath(path) }

func (Source) RegisteredWorkspace(currentDirectory string) (integration.WorkspaceRegistration, bool, error) {
	_, registered, found, err := RegisteredWorkspace(currentDirectory, "")
	if err != nil || !found {
		return integration.WorkspaceRegistration{}, found, err
	}
	members := make([]integration.WorkspaceMember, 0, len(registered.Members))
	for _, member := range registered.Members {
		members = append(members, integration.WorkspaceMember{Repository: member.Repository, Path: member.Path, Branch: member.Branch})
	}
	return integration.WorkspaceRegistration{ID: registered.ID, Name: registered.Name, Path: registered.Path, Members: members}, true, nil
}

func (Source) DiscoverRepositories(ctx context.Context, currentDirectory string) ([]string, error) {
	return DiscoverRepos(ctx, ExecRunner{}, currentDirectory)
}

func (Source) RepositoryBranches(ctx context.Context, repository string) ([]string, string, error) {
	fetchErr := FetchBranches(ctx, ExecRunner{}, repository)
	branches, err := Branches(ctx, ExecRunner{}, repository)
	if err != nil {
		return nil, "", err
	}
	warning := ""
	if fetchErr != nil {
		warning = "Could not refresh origin; using cached branches: " + fetchErr.Error()
	}
	return branches, warning, nil
}

func (Source) SessionName(repositoryName, workspaceName string) string {
	return SessionName(repositoryName, workspaceName)
}

func (Source) OpenWorkspace(ctx context.Context, path string, switchClient bool) (integration.Workspace, error) {
	created, err := OpenRegisteredWorkspace(ctx, ExecRunner{}, path, switchClient)
	return integrationWorkspace(created), err
}

func (Source) CreateWorkspace(ctx context.Context, req integration.ManagedWorkspaceRequest) (integration.Workspace, error) {
	cfg, err := config.Load()
	if err != nil {
		return integration.Workspace{}, err
	}
	created, err := Create(ctx, ExecRunner{}, CreateOptions{
		Repo: req.Repo, BranchMode: req.BranchMode, Name: req.Name, Branch: req.Branch,
		Base: req.Base, Path: req.Path, SessionName: req.SessionName, WorkspaceRoot: req.WorkspaceRoot,
		Model: cfg.Model, Thinking: cfg.Thinking, Sandbox: cfg.SBX.Enabled,
		SandboxKitName: cfg.SBX.Kit.Name, SandboxKitPath: cfg.SBX.Kit.Path,
		AdditionalSandboxMounts: cfg.SBX.AdditionalMounts, Tmux: cfg.Tmux,
		Switch: req.Switch, ForkPiSession: req.ForkPiSession,
		TaskLinkingKey: req.TaskLinkingKey, NotePath: req.NotePath,
	})
	return integrationWorkspace(created), err
}

func (Source) CreateSession(ctx context.Context, req integration.CreateSessionRequest) (integration.Workspace, error) {
	cfg, err := config.Load()
	if err != nil {
		return integration.Workspace{}, err
	}
	created, err := CreateSessionWithOptions(ctx, ExecRunner{}, CreateSessionOptions{
		Path: req.Path, SessionName: req.SessionName, TaskLinkingKey: req.TaskLinkingKey,
		Model: cfg.Model, Thinking: cfg.Thinking, Sandbox: cfg.SBX.Enabled,
		SandboxKitName: cfg.SBX.Kit.Name, SandboxKitPath: cfg.SBX.Kit.Path,
		AdditionalSandboxMounts: cfg.SBX.AdditionalMounts, SandboxName: req.RuntimeResourceID,
		Tmux: cfg.Tmux, Switch: req.Switch,
	})
	return integrationWorkspace(created), err
}

func (Source) PreviewReconcile(ctx context.Context, req integration.WorkspaceReconcileRequest) (integration.WorkspaceReconcilePlan, error) {
	plan, err := PreviewReconcileWorkspace(ctx, ExecRunner{}, reconcileRequest(req))
	return integrationPlan(plan), err
}

func (Source) ApplyReconcile(ctx context.Context, logger *slog.Logger, req integration.WorkspaceReconcileRequest) (integration.WorkspaceReconcileResult, error) {
	result, err := ApplyReconcileWorkspace(ctx, ExecRunner{}, logger, reconcileRequest(req))
	return integrationResult(result), err
}

func (Source) ReconcileErrorDetails(err error) (integration.WorkspaceReconcileError, bool) {
	problem, ok := ReconcileWorkspaceErrorDetails(err)
	if !ok {
		return integration.WorkspaceReconcileError{}, false
	}
	return integration.WorkspaceReconcileError{
		Reason: problem.Reason, Message: problem.Message, Repository: problem.Repository,
		Path: problem.Path, Branch: problem.Branch, ChangeCount: problem.ChangeCount,
	}, true
}

func (Source) InspectWorkspace(ctx context.Context, currentDirectory, workspaceRoot string) (any, error) {
	return InspectWorkspace(ctx, ExecRunner{}, currentDirectory, workspaceRoot)
}

func (Source) InspectRepositoryRefs(ctx context.Context, repository string) (any, error) {
	return InspectRepositoryRefs(ctx, ExecRunner{}, repository)
}

func integrationWorkspace(value Workspace) integration.Workspace {
	return integration.Workspace{
		Name: value.Name, Branch: value.Branch, Base: value.Base, Repo: value.Repo,
		Path: value.Path, SessionName: value.SessionName, SandboxName: value.SandboxName, Warning: value.Warning,
	}
}

func reconcileRequest(req integration.WorkspaceReconcileRequest) ReconcileWorkspaceRequest {
	worktrees := make([]DesiredWorkspaceWorktree, 0, len(req.Desired.Worktrees))
	for _, worktree := range req.Desired.Worktrees {
		worktrees = append(worktrees, DesiredWorkspaceWorktree{
			Repository: worktree.Repository, BranchMode: worktree.BranchMode,
			Name: worktree.Name, Branch: worktree.Branch, Base: worktree.Base,
		})
	}
	var sandbox *DesiredWorkspaceSandbox
	if req.Desired.Sandbox != nil {
		mounts := make([]DesiredSandboxMount, 0, len(req.Desired.Sandbox.AdditionalMounts))
		for _, mount := range req.Desired.Sandbox.AdditionalMounts {
			mounts = append(mounts, DesiredSandboxMount{Path: mount.Path, ReadOnly: mount.ReadOnly})
		}
		ports := make([]workspacegroup.SandboxPort, 0, len(req.Desired.Sandbox.Ports))
		for _, port := range req.Desired.Sandbox.Ports {
			ports = append(ports, workspacegroup.SandboxPort{HostPort: port.HostPort, SandboxPort: port.SandboxPort})
		}
		sandbox = &DesiredWorkspaceSandbox{AdditionalMounts: mounts, Ports: ports}
	}
	return ReconcileWorkspaceRequest{
		Workspace: req.Workspace, WorkspaceRoot: req.WorkspaceRoot,
		AdditionalSandboxMounts: req.AdditionalSandboxMounts,
		ExpectedPlanID:          req.ExpectedPlanID, ExpectedPlanChangeCount: req.ExpectedPlanChangeCount,
		Revision: req.Revision, Desired: DesiredWorkspaceDescription{Worktrees: worktrees, Sandbox: sandbox},
	}
}

func integrationPlan(value ReconcileWorkspacePlan) integration.WorkspaceReconcilePlan {
	changes := make([]integration.WorkspaceChange, 0, len(value.Changes))
	for _, change := range value.Changes {
		changes = append(changes, integration.WorkspaceChange{
			Action: change.Action, Resource: change.Resource, Summary: change.Summary,
			Repository: change.Repository, Path: change.Path, Branch: change.Branch,
			HostPort: change.HostPort, SandboxPort: change.SandboxPort, ReadOnly: change.ReadOnly,
		})
	}
	return integration.WorkspaceReconcilePlan{
		WorkspaceID: value.WorkspaceID, WorkspaceName: value.WorkspaceName,
		Revision: value.Revision, NextRevision: value.NextRevision, PlanID: value.PlanID,
		AutoConfirm: value.AutoConfirm, EffectiveMountCount: value.EffectiveMountCount,
		Changes: changes, Warnings: append([]string(nil), value.Warnings...),
	}
}

func integrationResult(value ReconcileWorkspaceResult) integration.WorkspaceReconcileResult {
	result := integration.WorkspaceReconcileResult{
		OK: value.OK, WorkspaceID: value.WorkspaceID, Revision: value.Revision,
		WorktreesAdded: value.WorktreesAdded, WorktreesRemoved: value.WorktreesRemoved,
		SandboxReconciled: value.SandboxReconciled, MountsAdded: value.MountsAdded,
		MountsRemoved: value.MountsRemoved, PortsPublished: value.PortsPublished,
		PortsUnpublished: value.PortsUnpublished, Retryable: value.Retryable,
		ReconfirmRequired: value.ReconfirmRequired, Reason: value.Reason,
		Warning: value.Warning, Error: value.Error,
	}
	if value.Plan != nil {
		plan := integrationPlan(*value.Plan)
		result.Plan = &plan
	}
	return result
}
