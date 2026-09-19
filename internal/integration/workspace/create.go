package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	sessionlayout "radar/internal/integration/tmux/layout"
	"radar/internal/integration/workspace/group"
	"radar/internal/pi"
)

// Creation and editing use the same resource planner and apply engine.
func planCreate(ctx context.Context, runner Runner, options CreateOptions) (ReconcileWorkspacePlan, ReconcileWorkspaceRequest, error) {
	fail := func(err error) (ReconcileWorkspacePlan, ReconcileWorkspaceRequest, error) {
		return ReconcileWorkspacePlan{}, ReconcileWorkspaceRequest{}, err
	}
	if err := runner.LookPath("tmux"); err != nil {
		return fail(fmt.Errorf("workspace session requires %q: %w", "tmux", err))
	}
	if err := pi.ValidateThinking(options.Thinking); err != nil {
		return fail(err)
	}
	if err := sessionlayout.Validate(options.Tmux); err != nil {
		return fail(err)
	}
	if options.NoteAuthor == nil && options.Note != nil && options.Note.Create {
		options.NoteAuthor = obsidian.NewSource()
	}
	root, err := workspaceRoot(options.WorkspaceRoot)
	if err != nil {
		return fail(err)
	}
	members := append([]DesiredWorkspaceWorktree{}, options.Worktrees...)
	if options.Repo != "" {
		member := DesiredWorkspaceWorktree{Repository: options.Repo, BranchMode: options.BranchMode, Branch: options.Branch, Name: options.Name, Base: options.Base}
		if member.BranchMode == integration.WorkspaceBranchExisting {
			member.Name = ""
			member.Base = ""
		}
		members = append([]DesiredWorkspaceWorktree{member}, members...)
	}
	if existing, found, err := existingWorkspaceForTask(root, options.TaskLinkingKey); err != nil {
		return fail(err)
	} else if found {
		return planOpenWorkspace(ctx, runner, root, existing, options)
	}
	if options.Note != nil {
		if existing, found, err := existingWorkspaceForTask(root, options.Note.LinkingKey); err != nil {
			return fail(err)
		} else if found {
			if err := validateTaskLink(existing.Path, existing.TaskLinkingKey, options.TaskLinkingKey); err != nil {
				return fail(err)
			}
			return planOpenWorkspace(ctx, runner, root, existing, options)
		}
	}
	if len(members) == 1 {
		repository, err := canonicalRepository(ctx, runner, members[0].Repository)
		if err != nil {
			return fail(err)
		}
		branch := normalizeExistingBranch(members[0].Branch)
		if branch == "" && members[0].BranchMode == integration.WorkspaceBranchNew {
			branch = BranchName(members[0].Name)
		}
		if existing, _, found, err := existingWorkspaceForMember(root, repository, branch); err != nil {
			return fail(err)
		} else if found {
			if err := validateTaskLink(existing.Path, existing.TaskLinkingKey, options.TaskLinkingKey); err != nil {
				return fail(err)
			}
			return planOpenWorkspace(ctx, runner, root, existing, options)
		}
	}
	name := strings.TrimSpace(options.Name)
	if name == "" && options.Branch != "" {
		name = normalizeExistingBranch(options.Branch)
	}
	if name == "" && options.NotePath != "" {
		name = strings.TrimSuffix(filepath.Base(options.NotePath), filepath.Ext(options.NotePath))
	}
	if name == "" {
		return fail(fmt.Errorf("workspace name is required"))
	}
	anchor := options.Path
	if anchor == "" {
		anchor, err = workspaceAnchorPath(root, name, options.TaskLinkingKey)
	} else {
		anchor, err = filepath.Abs(anchor)
	}
	if err != nil {
		return fail(err)
	}
	if !isWorkspacePath(anchor, root) {
		return fail(fmt.Errorf("workspace anchor must be a direct child of %s", root))
	}
	if _, err := os.Lstat(anchor); err == nil {
		return fail(fmt.Errorf("workspace anchor is occupied by unrelated content: %s", anchor))
	} else if !os.IsNotExist(err) {
		return fail(err)
	}
	if err := validateSessionDependencies(runner, options.Tmux); err != nil {
		return fail(err)
	}
	repoConfig := RepoConfig{}
	repoName := "workspace"
	if len(members) > 0 {
		members[0].Repository, err = canonicalRepository(ctx, runner, members[0].Repository)
		if err != nil {
			return fail(err)
		}
		repoName = filepath.Base(members[0].Repository)
		repoConfig, err = loadRepoConfig(members[0].Repository)
		if err != nil {
			return fail(err)
		}
	}
	sandbox := workspaceSandboxConfig(repoConfig, options.Sandbox, options.SandboxKitName, options.SandboxKitPath, options.AdditionalSandboxMounts)
	if err := validateSandboxDependencies(runner, sandbox.Enabled); err != nil {
		return fail(err)
	}
	model, thinking := options.Model, options.Thinking
	if repoConfig.Model != "" {
		model = repoConfig.Model
	}
	if repoConfig.Thinking != "" {
		thinking = repoConfig.Thinking
	}
	sessionName := options.SessionName
	if sessionName == "" {
		sessionName = WorktreeName(name)
		if len(members) > 0 {
			sessionName = SessionName(repoName, name)
		}
	}
	initial := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: name, Path: anchor, SessionName: sessionName, TaskLinkingKey: options.TaskLinkingKey, Model: model, Thinking: thinking, Tmux: options.Tmux, Members: []workspacegroup.Member{}}
	desired := DesiredWorkspaceDescription{Worktrees: members, Note: options.Note}
	if desired.Note == nil && options.NotePath != "" {
		desired.Note = &DesiredWorkspaceNote{Path: options.NotePath, LinkingKey: options.TaskLinkingKey}
	}
	if desired.Note == nil {
		if options.NoteAuthor == nil {
			options.NoteAuthor = obsidian.NewSource()
		}
		note, err := options.NoteAuthor.PrepareWorkspaceNote(ctx, name)
		if err != nil {
			return fail(fmt.Errorf("prepare workspace notes.md: %w", err))
		}
		desired.Note = &note
	}
	if sandbox.Enabled {
		initial.Sandbox = &workspacegroup.Sandbox{Name: SandboxName(repoName, name), Agent: sandbox.Kit.Name, KitPath: ExpandPath(sandbox.Kit.Path), AdditionalMounts: []workspacegroup.SandboxMount{}, Ports: []workspacegroup.SandboxPort{}}
		desired.Sandbox = &DesiredWorkspaceSandbox{AdditionalMounts: []DesiredSandboxMount{}, Ports: []workspacegroup.SandboxPort{}}
	}
	revision, err := workspaceRevision(initial, nil)
	if err != nil {
		return fail(err)
	}
	request := ReconcileWorkspaceRequest{creating: true, Workspace: anchor, WorkspaceRoot: root, Revision: revision, Desired: desired, AdditionalSandboxMounts: options.AdditionalSandboxMounts, NoteAuthor: options.NoteAuthor}
	plan, err := planWorkspace(ctx, runner, root, initial, request)
	if err != nil {
		return fail(err)
	}
	plan.Note = desired.Note
	plan.create, plan.forkSession = true, options.ForkPiSession
	plan.Changes = append([]WorkspaceChange{{Action: "add", Resource: "workspace", Path: anchor, Summary: "create workspace " + anchor}}, plan.Changes...)
	plan.Changes = append(plan.Changes, WorkspaceChange{Action: "add", Resource: "session", Summary: "start tmux and Pi in " + anchor})
	plan.PlanID, err = workspacePlanID(plan.Revision, plan.Changes, plan.Warnings)
	return plan, request, err
}

func Create(ctx context.Context, runner Runner, options CreateOptions) (Workspace, error) {
	root, err := workspaceRoot(options.WorkspaceRoot)
	if err != nil {
		return Workspace{}, err
	}
	var result Workspace
	err = workspacegroup.WithNoteLock(root, func() error {
		var err error
		result, err = createWorkspace(ctx, runner, options)
		return err
	})
	return result, err
}

func createWorkspace(ctx context.Context, runner Runner, options CreateOptions) (Workspace, error) {
	plan, request, err := planCreate(ctx, runner, options)
	if err != nil {
		return Workspace{}, err
	}
	if options.ExpectedPlanID != "" && options.ExpectedPlanID != plan.PlanID {
		return Workspace{}, fmt.Errorf("workspace creation plan changed; review it again")
	}
	if plan.openExisting {
		return openRegisteredWorkspace(ctx, runner, plan.root, plan.group, options, options.Repo, options.Branch)
	}
	if err := createAnchorDirectory(plan.root, plan.group.Path); err != nil {
		return Workspace{}, err
	}
	initial := plan.group
	initial.Members = []workspacegroup.Member{}
	initial.NotePath, initial.NoteLinkingKey = "", ""
	if err := registerWorkspace(plan.root, initial); err != nil {
		removeEmptyAnchor(initial.Path)
		return Workspace{}, err
	}
	result, err := applyWorkspacePlan(ctx, runner, nil, request, plan)
	created := Workspace{Name: plan.group.Name, Path: plan.group.Path, SessionName: plan.group.SessionName, SandboxName: sandboxName(plan.group), Warning: result.Warning}
	if len(plan.additions) > 0 {
		member := plan.additions[0].plan
		created.Repo, created.Branch, created.Base = member.Repo, member.Branch, member.Base
	}
	if err != nil {
		return created, fmt.Errorf("workspace %s was partially created; inspect it before retrying: %w", created.Path, err)
	}
	if !result.OK {
		return created, fmt.Errorf("workspace %s needs reconciliation: %s", created.Path, result.Error)
	}
	if options.Switch {
		if _, err := runner.Run(ctx, created.Path, "tmux", "switch-client", "-t", created.SessionName); err != nil {
			return created, err
		}
	}
	return created, nil
}

func planOpenWorkspace(ctx context.Context, runner Runner, root string, group workspacegroup.Workspace, options CreateOptions) (ReconcileWorkspacePlan, ReconcileWorkspaceRequest, error) {
	if group.NotePath == "" {
		return ReconcileWorkspacePlan{}, ReconcileWorkspaceRequest{}, fmt.Errorf("workspace %s has no canonical note; associate its Obsidian note before opening it", group.Path)
	}
	notePath := options.NotePath
	if options.Note != nil {
		notePath = options.Note.Path
	}
	if notePath != "" && !sameCleanPath(notePath, group.NotePath) {
		return ReconcileWorkspacePlan{}, ReconcileWorkspaceRequest{}, fmt.Errorf("workspace already exists at %s; its canonical note cannot be replaced", group.Path)
	}
	ports, _, err := observedSandboxPorts(ctx, runner, group)
	if err != nil {
		return ReconcileWorkspacePlan{}, ReconcileWorkspaceRequest{}, err
	}
	revision, err := workspaceRevision(group, ports)
	if err != nil {
		return ReconcileWorkspacePlan{}, ReconcileWorkspaceRequest{}, err
	}
	changes := []WorkspaceChange{{Action: "open", Resource: "workspace", Path: group.Path, Summary: "open existing workspace " + group.Path}}
	planID, err := workspacePlanID(revision, changes, nil)
	return ReconcileWorkspacePlan{WorkspaceID: group.ID, WorkspaceName: group.Name, Revision: revision, PlanID: planID, Changes: changes, root: root, group: group, openExisting: true}, ReconcileWorkspaceRequest{}, err
}
