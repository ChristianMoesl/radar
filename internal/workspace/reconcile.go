package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"radar/internal/integration"
	"radar/internal/workspacegroup"
)

type ReconcileWorkspaceRequest struct {
	Workspace               string                      `json:"-"`
	WorkspaceRoot           string                      `json:"-"`
	AdditionalSandboxMounts []string                    `json:"-"`
	ExpectedPlanID          string                      `json:"-"`
	ExpectedPlanChangeCount *int                        `json:"-"`
	Revision                string                      `json:"revision"`
	Desired                 DesiredWorkspaceDescription `json:"desired"`
}

type DesiredWorkspaceDescription struct {
	Worktrees []DesiredWorkspaceWorktree `json:"worktrees"`
	Sandbox   *DesiredWorkspaceSandbox   `json:"sandbox"`
}

type DesiredWorkspaceWorktree struct {
	Repository string                          `json:"repository"`
	BranchMode integration.WorkspaceBranchMode `json:"branch_mode"`
	Name       string                          `json:"name,omitempty"`
	Branch     string                          `json:"branch,omitempty"`
	Base       string                          `json:"base,omitempty"`
}

type DesiredWorkspaceSandbox struct {
	AdditionalMounts []DesiredSandboxMount        `json:"additional_mounts"`
	Ports            []workspacegroup.SandboxPort `json:"ports"`
}

type DesiredSandboxMount struct {
	Path     string `json:"path"`
	ReadOnly *bool  `json:"read_only,omitempty"`
}

type WorkspaceChange struct {
	Action      string `json:"action"`
	Resource    string `json:"resource"`
	Summary     string `json:"summary"`
	Repository  string `json:"repository,omitempty"`
	Path        string `json:"path,omitempty"`
	Branch      string `json:"branch,omitempty"`
	HostPort    int    `json:"host_port,omitempty"`
	SandboxPort int    `json:"sandbox_port,omitempty"`
	ReadOnly    *bool  `json:"read_only,omitempty"`
}

type ReconcileWorkspacePlan struct {
	WorkspaceID         string            `json:"workspace_id"`
	WorkspaceName       string            `json:"workspace_name"`
	Revision            string            `json:"revision"`
	NextRevision        string            `json:"next_revision"`
	PlanID              string            `json:"plan_id"`
	EffectiveMountCount int               `json:"effective_sandbox_mount_count,omitempty"`
	Changes             []WorkspaceChange `json:"changes"`
	Warnings            []string          `json:"warnings,omitempty"`

	root      string
	group     workspacegroup.Workspace
	additions []reconcileAddition
	removals  []workspacegroup.Member
}

type ReconcileWorkspaceResult struct {
	OK                bool                    `json:"ok"`
	WorkspaceID       string                  `json:"workspace_id"`
	Revision          string                  `json:"revision,omitempty"`
	WorktreesAdded    int                     `json:"worktrees_added"`
	WorktreesRemoved  int                     `json:"worktrees_removed"`
	SandboxReconciled bool                    `json:"sandbox_reconciled"`
	MountsAdded       int                     `json:"mounts_added"`
	MountsRemoved     int                     `json:"mounts_removed"`
	PortsPublished    int                     `json:"ports_published"`
	PortsUnpublished  int                     `json:"ports_unpublished"`
	Retryable         bool                    `json:"retryable,omitempty"`
	ReconfirmRequired bool                    `json:"reconfirm_required,omitempty"`
	Reason            string                  `json:"reason,omitempty"`
	Plan              *ReconcileWorkspacePlan `json:"plan,omitempty"`
	Warning           string                  `json:"warning,omitempty"`
	Error             string                  `json:"error,omitempty"`
}

type ReconcileWorkspaceError struct {
	Reason      string `json:"reason"`
	Message     string `json:"error"`
	Repository  string `json:"repository,omitempty"`
	Path        string `json:"path,omitempty"`
	Branch      string `json:"branch,omitempty"`
	ChangeCount int    `json:"change_count,omitempty"`
}

func (e *ReconcileWorkspaceError) Error() string { return e.Message }

func ReconcileWorkspaceErrorDetails(err error) (*ReconcileWorkspaceError, bool) {
	var problem *ReconcileWorkspaceError
	if errors.As(err, &problem) {
		return problem, true
	}
	return nil, false
}

type reconcileAddition struct {
	plan WorktreePlan
}

const largeEffectiveMountCount = 20

func PreviewReconcileWorkspace(ctx context.Context, runner Runner, request ReconcileWorkspaceRequest) (ReconcileWorkspacePlan, error) {
	root, _, group, err := resolveWorkspaceGroup(ctx, runner, request.Workspace, request.WorkspaceRoot)
	if err != nil {
		return ReconcileWorkspacePlan{}, err
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return ReconcileWorkspacePlan{}, err
	}
	actualPorts, incompatiblePorts, _, err := observedSandboxPortState(ctx, runner, group)
	if err != nil {
		return ReconcileWorkspacePlan{}, err
	}
	revision, err := workspaceRevision(group, actualPorts)
	if err != nil {
		return ReconcileWorkspacePlan{}, err
	}
	if strings.TrimSpace(request.Revision) == "" {
		return ReconcileWorkspacePlan{}, fmt.Errorf("workspace revision is required")
	}
	if request.Revision != revision {
		return ReconcileWorkspacePlan{}, &ReconcileWorkspaceError{
			Reason:  "stale_revision",
			Message: fmt.Sprintf("workspace changed since it was inspected: expected revision %s, current revision is %s", request.Revision, revision),
		}
	}
	if len(request.Desired.Worktrees) == 0 {
		return ReconcileWorkspacePlan{}, fmt.Errorf("desired workspace must contain its primary worktree")
	}

	existingByIdentity := make(map[string]workspacegroup.Member, len(group.Members))
	for _, member := range group.Members {
		existingByIdentity[workspaceMemberKey(member.Repository, member.Branch)] = member
	}
	candidate := group
	candidate.Members = nil
	additions := []reconcileAddition{}
	desiredIdentities := map[string]bool{}
	plansByPath := map[string]WorktreePlan{}

	for _, desired := range request.Desired.Worktrees {
		switch desired.BranchMode {
		case integration.WorkspaceBranchNew:
			if strings.TrimSpace(desired.Name) == "" || strings.TrimSpace(desired.Base) == "" || strings.TrimSpace(desired.Branch) != "" {
				return ReconcileWorkspacePlan{}, fmt.Errorf("new desired worktrees require name and base only")
			}
		case integration.WorkspaceBranchExisting:
			if strings.TrimSpace(desired.Branch) == "" || strings.TrimSpace(desired.Name) != "" || strings.TrimSpace(desired.Base) != "" {
				return ReconcileWorkspacePlan{}, fmt.Errorf("existing desired worktrees require branch only")
			}
		default:
			return ReconcileWorkspacePlan{}, fmt.Errorf("desired worktree branch mode must be new or existing")
		}
		repository, err := filepath.Abs(strings.TrimSpace(desired.Repository))
		if err != nil || strings.TrimSpace(desired.Repository) == "" {
			return ReconcileWorkspacePlan{}, fmt.Errorf("every desired worktree requires an absolute repository path")
		}
		repository = filepath.Clean(repository)
		branch := normalizeExistingBranch(desired.Branch)
		if desired.BranchMode == integration.WorkspaceBranchNew {
			branch = BranchName(desired.Name)
		}
		identity := workspaceMemberKey(repository, branch)
		if desiredIdentities[identity] {
			return ReconcileWorkspacePlan{}, &ReconcileWorkspaceError{
				Reason: "duplicate_member", Repository: repository, Branch: branch,
				Message: fmt.Sprintf("repository %s branch %q appears more than once in desired worktrees", repository, branch),
			}
		}
		desiredIdentities[identity] = true

		if member, ok := existingByIdentity[identity]; ok {
			if desired.BranchMode != integration.WorkspaceBranchExisting {
				return ReconcileWorkspacePlan{}, fmt.Errorf("repository %s branch %q already belongs to the workspace; use existing branch mode", repository, branch)
			}
			candidate.Members = append(candidate.Members, member)
			continue
		}

		name := desired.Name
		if desired.BranchMode == integration.WorkspaceBranchExisting {
			name = desired.Branch
		}
		destination := filepath.Join(root, WorktreeDirectoryName(repository, name))
		plan, err := PlanWorktree(ctx, runner, WorktreeOptions{
			Repo: repository, BranchMode: desired.BranchMode, Name: desired.Name,
			Branch: desired.Branch, Base: desired.Base, Path: destination,
			WorkspaceRoot: root, AllowExisting: true,
		})
		if err != nil {
			return ReconcileWorkspacePlan{}, err
		}
		if owner, found := workspacegroup.FindByMemberPath(registry, plan.Path); found && owner.ID != group.ID {
			return ReconcileWorkspacePlan{}, fmt.Errorf("repository %s branch %q already belongs to Radar workspace %q", plan.Repo, plan.Branch, owner.Name)
		}
		for _, member := range candidate.Members {
			if sameCleanPath(member.Path, plan.Path) {
				return ReconcileWorkspacePlan{}, fmt.Errorf("repository basename/path collision at %s", plan.Path)
			}
		}
		member := workspacegroup.Member{Repository: plan.Repo, Path: plan.Path, Branch: plan.Branch}
		candidate.Members = append(candidate.Members, member)
		additions = append(additions, reconcileAddition{plan: plan})
		plansByPath[pathKey(plan.Path)] = plan
	}

	removals := []workspacegroup.Member{}
	for _, member := range group.Members {
		if desiredIdentities[workspaceMemberKey(member.Repository, member.Branch)] {
			continue
		}
		if member.Primary {
			return ReconcileWorkspacePlan{}, fmt.Errorf("the primary worktree cannot be removed from its workspace")
		}
		changeCount, statusErr := worktreeChangeCount(ctx, runner, member.Path)
		if statusErr != nil {
			return ReconcileWorkspacePlan{}, statusErr
		}
		if changeCount > 0 {
			return ReconcileWorkspacePlan{}, &ReconcileWorkspaceError{
				Reason: "dirty_removal", Repository: member.Repository, Path: member.Path, Branch: member.Branch, ChangeCount: changeCount,
				Message: fmt.Sprintf("cannot remove dirty workspace member %s (%d changed entries); to retain it, include repository %s and branch %q unchanged in desired.worktrees; to remove it, commit, stash, or discard its changes, then inspect the workspace again", member.Path, changeCount, member.Repository, member.Branch),
			}
		}
		removals = append(removals, member)
	}

	if group.Sandbox == nil && request.Desired.Sandbox != nil {
		return ReconcileWorkspacePlan{}, fmt.Errorf("this workspace has no SBX sandbox; sandbox mounts and ports are unavailable")
	}
	if group.Sandbox != nil && request.Desired.Sandbox == nil {
		return ReconcileWorkspacePlan{}, fmt.Errorf("sandbox attachment cannot be changed through workspace reconciliation")
	}

	changes := []WorkspaceChange{}
	for _, addition := range additions {
		changes = append(changes, WorkspaceChange{
			Action: "add", Resource: "worktree", Repository: addition.plan.Repo,
			Path: addition.plan.Path, Branch: addition.plan.Branch,
			Summary: fmt.Sprintf("add worktree %s on %s", addition.plan.Path, addition.plan.Branch),
		})
	}
	for _, removal := range removals {
		changes = append(changes, WorkspaceChange{
			Action: "remove", Resource: "worktree", Repository: removal.Repository,
			Path: removal.Path, Branch: removal.Branch,
			Summary: fmt.Sprintf("remove clean worktree %s", removal.Path),
		})
	}

	recreateSandbox := false
	writableMountAdded := false
	effectiveMountCount := 0
	if candidate.Sandbox != nil {
		desiredAdditionalMounts, err := normalizeDesiredSandboxMounts(request.Desired.Sandbox.AdditionalMounts)
		if err != nil {
			return ReconcileWorkspacePlan{}, err
		}
		currentAdditionalMounts, err := normalizeSandboxMounts(group.Sandbox.AdditionalMounts)
		if err != nil {
			return ReconcileWorkspacePlan{}, err
		}
		candidate.Sandbox.AdditionalMounts = desiredAdditionalMounts
		desiredPorts, err := normalizeSandboxPorts(request.Desired.Sandbox.Ports)
		if err != nil {
			return ReconcileWorkspacePlan{}, err
		}
		candidate.Sandbox.Ports = desiredPorts
		mounts, err := desiredReconciledSandboxMounts(ctx, runner, candidate, plansByPath, request.AdditionalSandboxMounts, desiredAdditionalMounts)
		if err != nil {
			return ReconcileWorkspacePlan{}, err
		}
		candidate.Sandbox.Mounts = mounts
		effectiveMountCount = len(mounts)
		actual, found, err := findSandbox(ctx, runner, group.PrimaryPath, group.Sandbox.Name)
		if err != nil {
			return ReconcileWorkspacePlan{}, err
		}
		recreateSandbox = !found || !sameMountSet(mounts, sandboxWorkspaceMounts(actual))
		for _, mount := range mountDifference(desiredAdditionalMounts, currentAdditionalMounts) {
			readOnly := mount.ReadOnly
			mode := "writable"
			if mount.ReadOnly {
				mode = "read-only"
			} else {
				writableMountAdded = true
			}
			changes = append(changes, WorkspaceChange{Action: "add", Resource: "sandbox_mount", Path: mount.Path, ReadOnly: &readOnly, Summary: fmt.Sprintf("mount %s in the sandbox as %s", mount.Path, mode)})
		}
		for _, mount := range mountDifference(currentAdditionalMounts, desiredAdditionalMounts) {
			readOnly := mount.ReadOnly
			changes = append(changes, WorkspaceChange{Action: "remove", Resource: "sandbox_mount", Path: mount.Path, ReadOnly: &readOnly, Summary: fmt.Sprintf("remove sandbox mount %s", mount.Path)})
		}
		if recreateSandbox {
			changes = append(changes, WorkspaceChange{Action: "recreate", Resource: "sandbox", Summary: fmt.Sprintf("recreate SBX sandbox %s with %d effective mounts", group.Sandbox.Name, effectiveMountCount)})
		}
		for _, port := range portDifference(desiredPorts, actualPorts) {
			changes = append(changes, WorkspaceChange{Action: "add", Resource: "sandbox_port", HostPort: port.HostPort, SandboxPort: port.SandboxPort, Summary: fmt.Sprintf("expose IPv4 localhost:%d to sandbox port %d", port.HostPort, port.SandboxPort)})
		}
		for _, port := range portDifference(actualPorts, desiredPorts) {
			changes = append(changes, WorkspaceChange{Action: "remove", Resource: "sandbox_port", HostPort: port.HostPort, SandboxPort: port.SandboxPort, Summary: fmt.Sprintf("remove localhost:%d exposure for sandbox port %d", port.HostPort, port.SandboxPort)})
		}
		incompatibleSet := portSet(incompatiblePorts)
		actualSet := portSet(actualPorts)
		for _, port := range desiredPorts {
			if incompatibleSet[portKey(port)] && actualSet[portKey(port)] {
				changes = append(changes, WorkspaceChange{Action: "replace", Resource: "sandbox_port", HostPort: port.HostPort, SandboxPort: port.SandboxPort, Summary: fmt.Sprintf("restrict localhost:%d exposure for sandbox port %d to IPv4", port.HostPort, port.SandboxPort)})
			}
		}
	}

	nextRevision, err := workspaceRevision(candidate, sandboxPorts(candidate))
	if err != nil {
		return ReconcileWorkspacePlan{}, err
	}
	warnings := []string{}
	if recreateSandbox {
		warnings = append(warnings, "recreating the sandbox interrupts processes running inside it")
		if effectiveMountCount >= largeEffectiveMountCount {
			warnings = append(warnings, fmt.Sprintf("the sandbox will use %d effective mounts; unusually large mount sets may make SBX recreation less reliable", effectiveMountCount))
		}
	}
	if writableMountAdded {
		warnings = append(warnings, "a writable sandbox mount grants the sandbox write access to that host directory")
	}
	planID, err := workspacePlanID(revision, changes, warnings)
	if err != nil {
		return ReconcileWorkspacePlan{}, err
	}
	return ReconcileWorkspacePlan{
		WorkspaceID: group.ID, WorkspaceName: group.Name, Revision: revision,
		NextRevision: nextRevision, PlanID: planID, EffectiveMountCount: effectiveMountCount,
		Changes: changes, Warnings: warnings,
		root: root, group: candidate, additions: additions, removals: removals,
	}, nil
}

func ApplyReconcileWorkspace(ctx context.Context, runner Runner, logger *slog.Logger, request ReconcileWorkspaceRequest) (ReconcileWorkspaceResult, error) {
	plan, err := PreviewReconcileWorkspace(ctx, runner, request)
	if err != nil {
		logReconciliationFailure(logger, request.Workspace, "plan", ReconcileWorkspaceResult{}, err)
		return ReconcileWorkspaceResult{}, err
	}
	if request.ExpectedPlanID != "" && request.ExpectedPlanID != plan.PlanID {
		result := ReconcileWorkspaceResult{
			WorkspaceID: plan.WorkspaceID, ReconfirmRequired: true, Reason: "plan_changed",
			Plan: &plan, Error: "workspace reconciliation plan changed after confirmation",
		}
		if logger != nil {
			attributes := []any{
				"workspace_id", plan.WorkspaceID, "expected_plan_id", request.ExpectedPlanID,
				"actual_plan_id", plan.PlanID, "new_change_count", len(plan.Changes),
			}
			if request.ExpectedPlanChangeCount != nil {
				attributes = append(attributes, "old_change_count", *request.ExpectedPlanChangeCount)
			}
			logger.Warn("workspace reconciliation requires reconfirmation", attributes...)
		}
		return result, nil
	}
	result := ReconcileWorkspaceResult{WorkspaceID: plan.WorkspaceID}
	if logger != nil {
		logger.Info("workspace reconciliation started",
			"workspace_id", plan.WorkspaceID, "workspace_name", plan.WorkspaceName,
			"plan_id", plan.PlanID, "revision", plan.Revision, "change_count", len(plan.Changes),
			"effective_mount_count", plan.EffectiveMountCount)
	}
	prepared := make([]WorktreeResult, 0, len(plan.additions))
	for _, addition := range plan.additions {
		created, createErr := EnsureWorktree(ctx, runner, addition.plan)
		if createErr != nil {
			logReconciliationFailure(logger, plan.WorkspaceID, "worktrees", result, createErr)
			return result, createErr
		}
		prepared = append(prepared, created)
		result.WorktreesAdded++
	}
	for _, removal := range plan.removals {
		if _, removeErr := RemoveWorktree(ctx, runner, removal.Path, false); removeErr != nil {
			logReconciliationFailure(logger, plan.WorkspaceID, "worktrees", result, removeErr)
			return result, removeErr
		}
		result.WorktreesRemoved++
	}
	if logger != nil {
		logger.Info("workspace reconciliation worktrees completed", "workspace_id", plan.WorkspaceID,
			"worktrees_added", result.WorktreesAdded, "worktrees_removed", result.WorktreesRemoved)
	}
	if err := workspacegroup.Update(plan.root, func(registry *workspacegroup.Registry) error {
		workspacegroup.Put(registry, plan.group)
		return nil
	}); err != nil {
		logReconciliationFailure(logger, plan.WorkspaceID, "registry", result, err)
		return result, err
	}

	if plan.group.Sandbox != nil {
		if err := reconcileSandbox(ctx, runner, plan.group, logger); err != nil {
			result.Retryable = true
			result.Error = err.Error()
			logRetryableReconciliationFailure(logger, plan, "sandbox", result, err)
			return result, nil
		}
		result.SandboxReconciled = true
		for _, change := range plan.Changes {
			if change.Resource != "sandbox_mount" {
				continue
			}
			if change.Action == "add" {
				result.MountsAdded++
			} else if change.Action == "remove" {
				result.MountsRemoved++
			}
		}
		published, unpublished, err := reconcileSandboxPorts(ctx, runner, plan.group.Sandbox.Name, plan.group.Sandbox.Ports)
		result.PortsPublished = published
		result.PortsUnpublished = unpublished
		if logger != nil {
			logger.Info("workspace reconciliation ports completed", "workspace_id", plan.WorkspaceID,
				"ports_published", published, "ports_unpublished", unpublished,
				"desired_port_count", len(plan.group.Sandbox.Ports))
		}
		if err != nil {
			result.Retryable = true
			result.Error = err.Error()
			logRetryableReconciliationFailure(logger, plan, "ports", result, err)
			return result, nil
		}
	} else {
		result.SandboxReconciled = true
	}

	warnings := []string{}
	for index, addition := range plan.additions {
		memberIndex := memberIndexByPath(plan.group.Members, addition.plan.Path)
		if memberIndex < 0 || plan.group.Members[memberIndex].SetupScheduled {
			continue
		}
		commands := prepared[index].Plan.RepoConfig.Setup
		if setupErr := scheduleMemberSetup(ctx, runner, addition.plan.Path, plan.group.SessionName, sandboxName(plan.group), filepath.Base(addition.plan.Repo), commands); setupErr != nil {
			warnings = append(warnings, fmt.Sprintf("workspace setup for %s could not be started: %v", addition.plan.Path, setupErr))
			continue
		}
		plan.group.Members[memberIndex].SetupScheduled = true
	}
	if err := workspacegroup.Update(plan.root, func(registry *workspacegroup.Registry) error {
		workspacegroup.Put(registry, plan.group)
		return nil
	}); err != nil {
		result.Retryable = true
		result.Error = err.Error()
		logRetryableReconciliationFailure(logger, plan, "registry", result, err)
		return result, nil
	}
	ports, _, err := observedSandboxPorts(ctx, runner, plan.group)
	if err != nil {
		result.Retryable = true
		result.Error = err.Error()
		logRetryableReconciliationFailure(logger, plan, "revision", result, err)
		return result, nil
	}
	result.Revision, err = workspaceRevision(plan.group, ports)
	if err != nil {
		logReconciliationFailure(logger, plan.WorkspaceID, "revision", result, err)
		return result, err
	}
	result.OK = true
	result.Warning = strings.Join(warnings, "; ")
	if logger != nil {
		logger.Info("workspace reconciliation completed", "workspace_id", plan.WorkspaceID,
			"plan_id", plan.PlanID, "revision", result.Revision,
			"worktrees_added", result.WorktreesAdded, "worktrees_removed", result.WorktreesRemoved,
			"mounts_added", result.MountsAdded, "mounts_removed", result.MountsRemoved,
			"ports_published", result.PortsPublished, "ports_unpublished", result.PortsUnpublished)
	}
	return result, nil
}

func logReconciliationFailure(logger *slog.Logger, workspaceID, phase string, result ReconcileWorkspaceResult, err error) {
	if logger == nil {
		return
	}
	attributes := []any{
		"workspace_id", workspaceID, "phase", phase,
		"worktrees_added", result.WorktreesAdded, "worktrees_removed", result.WorktreesRemoved, "error", err,
	}
	if problem, ok := ReconcileWorkspaceErrorDetails(err); ok {
		attributes = append(attributes, "reason", problem.Reason, "path", problem.Path, "change_count", problem.ChangeCount)
	}
	logger.Error("workspace reconciliation failed", attributes...)
}

func logRetryableReconciliationFailure(logger *slog.Logger, plan ReconcileWorkspacePlan, phase string, result ReconcileWorkspaceResult, err error) {
	if logger == nil {
		return
	}
	logger.Warn("workspace reconciliation needs retry", "workspace_id", plan.WorkspaceID,
		"workspace_name", plan.WorkspaceName, "plan_id", plan.PlanID, "phase", phase,
		"worktrees_added", result.WorktreesAdded, "worktrees_removed", result.WorktreesRemoved,
		"ports_published", result.PortsPublished, "ports_unpublished", result.PortsUnpublished, "error", err)
}

func resolveWorkspaceGroup(ctx context.Context, runner Runner, currentDirectory, workspaceRoot string) (string, string, workspacegroup.Workspace, error) {
	root := strings.TrimSpace(workspaceRoot)
	var err error
	if root == "" {
		root, err = DefaultRoot()
		if err != nil {
			return "", "", workspacegroup.Workspace{}, err
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", workspacegroup.Workspace{}, err
	}
	root = filepath.Clean(root)
	current, err := currentGitTopLevel(ctx, runner, currentDirectory)
	if err != nil {
		return "", "", workspacegroup.Workspace{}, fmt.Errorf("current workspace: %w", err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return "", "", workspacegroup.Workspace{}, err
	}
	group, found := workspacegroup.FindByMemberPath(registry, current)
	if !found {
		group, err = enrollmentPlan(ctx, runner, root, current)
		if err != nil {
			return "", "", workspacegroup.Workspace{}, err
		}
	}
	return root, current, group, nil
}

func workspaceMemberKey(repository, branch string) string {
	return pathKey(repository) + "\x00" + strings.TrimSpace(branch)
}

func worktreeChangeCount(ctx context.Context, runner Runner, path string) (int, error) {
	status, err := runner.Run(ctx, "", "git", "-C", path, "status", "--porcelain")
	if err != nil {
		return 0, err
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return 0, nil
	}
	return len(strings.Split(status, "\n")), nil
}

func workspacePlanID(revision string, changes []WorkspaceChange, warnings []string) (string, error) {
	data, err := json.Marshal(struct {
		Revision string            `json:"revision"`
		Changes  []WorkspaceChange `json:"changes"`
		Warnings []string          `json:"warnings"`
	}{Revision: revision, Changes: changes, Warnings: warnings})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16]), nil
}

func workspaceRevision(group workspacegroup.Workspace, ports []workspacegroup.SandboxPort) (string, error) {
	type revisionSandbox struct {
		Name             string                        `json:"name"`
		Agent            string                        `json:"agent"`
		KitPath          string                        `json:"kit_path"`
		AdditionalMounts []workspacegroup.SandboxMount `json:"additional_mounts"`
		Ports            []workspacegroup.SandboxPort  `json:"ports"`
	}
	type revisionState struct {
		ID             string                  `json:"id"`
		Name           string                  `json:"name"`
		PrimaryPath    string                  `json:"primary_path"`
		SessionName    string                  `json:"session_name"`
		TaskLinkingKey string                  `json:"task_linking_key"`
		Members        []workspacegroup.Member `json:"members"`
		Sandbox        *revisionSandbox        `json:"sandbox"`
	}
	members := append([]workspacegroup.Member(nil), group.Members...)
	for index := range members {
		members[index].SetupScheduled = false
	}
	sort.Slice(members, func(i, j int) bool { return pathKey(members[i].Path) < pathKey(members[j].Path) })
	state := revisionState{ID: group.ID, Name: group.Name, PrimaryPath: group.PrimaryPath, SessionName: group.SessionName, TaskLinkingKey: group.TaskLinkingKey, Members: members}
	if group.Sandbox != nil {
		normalizedPorts, err := normalizeSandboxPorts(ports)
		if err != nil {
			return "", err
		}
		normalizedMounts, err := normalizeSandboxMounts(group.Sandbox.AdditionalMounts)
		if err != nil {
			return "", err
		}
		state.Sandbox = &revisionSandbox{Name: group.Sandbox.Name, Agent: group.Sandbox.Agent, KitPath: group.Sandbox.KitPath, AdditionalMounts: normalizedMounts, Ports: normalizedPorts}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16]), nil
}

func sandboxPorts(group workspacegroup.Workspace) []workspacegroup.SandboxPort {
	if group.Sandbox == nil {
		return []workspacegroup.SandboxPort{}
	}
	return append([]workspacegroup.SandboxPort(nil), group.Sandbox.Ports...)
}

func normalizeDesiredSandboxMounts(mounts []DesiredSandboxMount) ([]workspacegroup.SandboxMount, error) {
	result := make([]workspacegroup.SandboxMount, 0, len(mounts))
	for _, mount := range mounts {
		readOnly := true
		if mount.ReadOnly != nil {
			readOnly = *mount.ReadOnly
		}
		result = append(result, workspacegroup.SandboxMount{Path: mount.Path, ReadOnly: readOnly})
	}
	return normalizeSandboxMounts(result)
}

func normalizeSandboxMounts(mounts []workspacegroup.SandboxMount) ([]workspacegroup.SandboxMount, error) {
	result := make([]workspacegroup.SandboxMount, 0, len(mounts))
	seen := map[string]bool{}
	for _, mount := range mounts {
		if strings.HasSuffix(strings.TrimSpace(mount.Path), ":ro") {
			return nil, fmt.Errorf("sandbox additional mount %q must use read_only instead of a :ro suffix", mount.Path)
		}
		mount.Path = filepath.Clean(ExpandPath(strings.TrimSpace(mount.Path)))
		if !filepath.IsAbs(mount.Path) {
			return nil, fmt.Errorf("sandbox additional mount %q must be absolute or start with ~/", mount.Path)
		}
		key := pathKey(mount.Path)
		if seen[key] {
			return nil, fmt.Errorf("sandbox additional mount %q appears more than once", mount.Path)
		}
		seen[key] = true
		result = append(result, mount)
	}
	sort.Slice(result, func(i, j int) bool { return pathKey(result[i].Path) < pathKey(result[j].Path) })
	if result == nil {
		result = []workspacegroup.SandboxMount{}
	}
	return result, nil
}

func mountDifference(left, right []workspacegroup.SandboxMount) []workspacegroup.SandboxMount {
	rightSet := map[string]bool{}
	for _, mount := range right {
		rightSet[mountKey(mount)] = true
	}
	result := []workspacegroup.SandboxMount{}
	for _, mount := range left {
		if !rightSet[mountKey(mount)] {
			result = append(result, mount)
		}
	}
	return result
}

func mountKey(mount workspacegroup.SandboxMount) string {
	return fmt.Sprintf("%s:%t", pathKey(mount.Path), mount.ReadOnly)
}

func sandboxMountArgument(mount workspacegroup.SandboxMount) string {
	if mount.ReadOnly {
		return mount.Path + ":ro"
	}
	return mount.Path
}

func normalizeSandboxPorts(ports []workspacegroup.SandboxPort) ([]workspacegroup.SandboxPort, error) {
	result := append([]workspacegroup.SandboxPort(nil), ports...)
	hosts := map[int]bool{}
	for _, port := range result {
		if port.HostPort < 1 || port.HostPort > 65535 || port.SandboxPort < 1 || port.SandboxPort > 65535 {
			return nil, fmt.Errorf("sandbox ports must be between 1 and 65535")
		}
		if hosts[port.HostPort] {
			return nil, fmt.Errorf("host port %d is exposed more than once", port.HostPort)
		}
		hosts[port.HostPort] = true
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].HostPort != result[j].HostPort {
			return result[i].HostPort < result[j].HostPort
		}
		return result[i].SandboxPort < result[j].SandboxPort
	})
	if result == nil {
		result = []workspacegroup.SandboxPort{}
	}
	return result, nil
}

func observedSandboxPorts(ctx context.Context, runner Runner, group workspacegroup.Workspace) ([]workspacegroup.SandboxPort, bool, error) {
	ports, _, found, err := observedSandboxPortState(ctx, runner, group)
	return ports, found, err
}

func observedSandboxPortState(ctx context.Context, runner Runner, group workspacegroup.Workspace) ([]workspacegroup.SandboxPort, []workspacegroup.SandboxPort, bool, error) {
	if group.Sandbox == nil {
		return []workspacegroup.SandboxPort{}, []workspacegroup.SandboxPort{}, false, nil
	}
	_, found, err := findSandbox(ctx, runner, group.PrimaryPath, group.Sandbox.Name)
	if err != nil {
		return nil, nil, false, err
	}
	if !found {
		ports, normalizeErr := normalizeSandboxPorts(group.Sandbox.Ports)
		return ports, []workspacegroup.SandboxPort{}, false, normalizeErr
	}
	bindings, err := listSandboxPortBindings(ctx, runner, group.Sandbox.Name)
	if err != nil {
		return nil, nil, true, err
	}
	ports, err := logicalSandboxPorts(bindings)
	if err != nil {
		return nil, nil, true, err
	}
	return ports, incompatibleSandboxPorts(bindings), true, nil
}

func listSandboxPorts(ctx context.Context, runner Runner, name string) ([]workspacegroup.SandboxPort, error) {
	bindings, err := listSandboxPortBindings(ctx, runner, name)
	if err != nil {
		return nil, err
	}
	return logicalSandboxPorts(bindings)
}

type sandboxPortBinding struct {
	Port     workspacegroup.SandboxPort
	HostIP   string
	Protocol string
}

func listSandboxPortBindings(ctx context.Context, runner Runner, name string) ([]sandboxPortBinding, error) {
	output, err := runner.Run(ctx, "", "sbx", "ports", name, "--json")
	if err != nil {
		return nil, sbxCommandError(err)
	}
	bindings, err := parseSandboxPortsJSON([]byte(output))
	if err != nil {
		return nil, fmt.Errorf("unexpected sbx ports output: %w", err)
	}
	return bindings, nil
}

func parseSandboxPortsJSON(data []byte) ([]sandboxPortBinding, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	items, ok := value.([]any)
	if !ok {
		if object, objectOK := value.(map[string]any); objectOK {
			for _, key := range []string{"ports", "published_ports", "publishedPorts"} {
				if nested, exists := object[key]; exists {
					items, ok = nested.([]any)
					break
				}
			}
		}
	}
	if !ok {
		return nil, fmt.Errorf("expected a JSON array")
	}
	bindings := make([]sandboxPortBinding, 0, len(items))
	for _, item := range items {
		object, objectOK := item.(map[string]any)
		if !objectOK {
			return nil, fmt.Errorf("expected port objects")
		}
		host, hostOK := integerField(object, "hostport")
		sandbox, sandboxOK := integerField(object, "sandboxport", "containerport", "targetport")
		hostIP, hostIPOK := stringField(object, "hostip")
		protocol, protocolOK := stringField(object, "protocol")
		if !hostOK || !sandboxOK || !hostIPOK || !protocolOK {
			return nil, fmt.Errorf("port object does not contain host_ip, host_port, sandbox_port, and protocol")
		}
		bindings = append(bindings, sandboxPortBinding{
			Port:   workspacegroup.SandboxPort{HostPort: host, SandboxPort: sandbox},
			HostIP: strings.TrimSpace(hostIP), Protocol: strings.ToLower(strings.TrimSpace(protocol)),
		})
	}
	return bindings, nil
}

func stringField(object map[string]any, names ...string) (string, bool) {
	for key, value := range object {
		normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
		for _, name := range names {
			if normalized != name {
				continue
			}
			text, ok := value.(string)
			return text, ok
		}
	}
	return "", false
}

func logicalSandboxPorts(bindings []sandboxPortBinding) ([]workspacegroup.SandboxPort, error) {
	unique := map[string]workspacegroup.SandboxPort{}
	for _, binding := range bindings {
		unique[portKey(binding.Port)] = binding.Port
	}
	ports := make([]workspacegroup.SandboxPort, 0, len(unique))
	for _, port := range unique {
		ports = append(ports, port)
	}
	return normalizeSandboxPorts(ports)
}

func incompatibleSandboxPorts(bindings []sandboxPortBinding) []workspacegroup.SandboxPort {
	incompatible := map[string]workspacegroup.SandboxPort{}
	for _, binding := range bindings {
		if binding.HostIP != "127.0.0.1" || binding.Protocol != "tcp4" {
			incompatible[portKey(binding.Port)] = binding.Port
		}
	}
	ports := make([]workspacegroup.SandboxPort, 0, len(incompatible))
	for _, port := range incompatible {
		ports = append(ports, port)
	}
	ports, _ = normalizeSandboxPorts(ports)
	return ports
}

func integerField(object map[string]any, names ...string) (int, bool) {
	for key, value := range object {
		normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
		for _, name := range names {
			if normalized != name {
				continue
			}
			switch typed := value.(type) {
			case float64:
				return int(typed), typed == float64(int(typed))
			case string:
				parsed, err := strconv.Atoi(typed)
				return parsed, err == nil
			}
		}
	}
	return 0, false
}

func portDifference(left, right []workspacegroup.SandboxPort) []workspacegroup.SandboxPort {
	rightSet := portSet(right)
	result := []workspacegroup.SandboxPort{}
	for _, port := range left {
		if !rightSet[portKey(port)] {
			result = append(result, port)
		}
	}
	return result
}

func portSet(ports []workspacegroup.SandboxPort) map[string]bool {
	set := make(map[string]bool, len(ports))
	for _, port := range ports {
		set[portKey(port)] = true
	}
	return set
}

func portKey(port workspacegroup.SandboxPort) string {
	return fmt.Sprintf("%d:%d", port.HostPort, port.SandboxPort)
}

func ipv4PortKey(port workspacegroup.SandboxPort) string {
	return fmt.Sprintf("%d:%d/tcp4", port.HostPort, port.SandboxPort)
}

func protocolPortKey(port workspacegroup.SandboxPort, protocol string) string {
	return fmt.Sprintf("%d:%d/%s", port.HostPort, port.SandboxPort, protocol)
}

func reconcileSandboxPorts(ctx context.Context, runner Runner, name string, desired []workspacegroup.SandboxPort) (int, int, error) {
	bindings, err := listSandboxPortBindings(ctx, runner, name)
	if err != nil {
		return 0, 0, err
	}
	actual, err := logicalSandboxPorts(bindings)
	if err != nil {
		return 0, 0, err
	}
	published, unpublished := 0, 0
	incompatible := portSet(incompatibleSandboxPorts(bindings))
	cleaned := map[string]bool{}
	unpublishSpecs := map[string]bool{}
	for _, binding := range bindings {
		key := portKey(binding.Port)
		if !incompatible[key] {
			continue
		}
		unpublishSpecs[protocolPortKey(binding.Port, binding.Protocol)] = true
		cleaned[key] = true
	}
	specs := make([]string, 0, len(unpublishSpecs))
	for spec := range unpublishSpecs {
		specs = append(specs, spec)
	}
	sort.Strings(specs)
	for _, spec := range specs {
		if _, err := runner.Run(ctx, "", "sbx", "ports", name, "--unpublish", spec); err != nil {
			return published, unpublished, sbxCommandError(err)
		}
		unpublished++
	}
	compatibleActual := make([]workspacegroup.SandboxPort, 0, len(actual))
	for _, port := range actual {
		if !cleaned[portKey(port)] {
			compatibleActual = append(compatibleActual, port)
		}
	}
	for _, port := range portDifference(compatibleActual, desired) {
		if _, err := runner.Run(ctx, "", "sbx", "ports", name, "--unpublish", ipv4PortKey(port)); err != nil {
			return published, unpublished, sbxCommandError(err)
		}
		unpublished++
	}
	for _, port := range portDifference(desired, compatibleActual) {
		if _, err := runner.Run(ctx, "", "sbx", "ports", name, "--publish", ipv4PortKey(port)); err != nil {
			return published, unpublished, sbxCommandError(err)
		}
		published++
	}
	return published, unpublished, nil
}

func desiredReconciledSandboxMounts(ctx context.Context, runner Runner, group workspacegroup.Workspace, plans map[string]WorktreePlan, global []string, additional []workspacegroup.SandboxMount) ([]string, error) {
	mounts := []string{}
	for _, member := range group.Members {
		mounts = append(mounts, member.Path)
		lookupPath := member.Path
		if plan, ok := plans[pathKey(member.Path)]; ok && !plan.Existing {
			lookupPath = plan.Repo
		}
		commonDir, err := runner.Run(ctx, lookupPath, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return nil, err
		}
		commonDir = filepath.Clean(strings.TrimSpace(commonDir))
		if !pathContains(member.Path, commonDir) {
			mounts = append(mounts, commonDir)
		}
		config, err := loadRepoConfig(member.Repository)
		if err != nil {
			return nil, err
		}
		if config.SBX != nil {
			mounts = append(mounts, config.SBX.AdditionalMounts...)
		}
	}
	mounts = append(mounts, global...)
	managed, err := normalizeConfiguredMounts(mounts)
	if err != nil {
		return nil, err
	}
	managedPaths := map[string]bool{}
	for _, mount := range managed {
		managedPaths[pathKey(strings.TrimSuffix(mount, ":ro"))] = true
	}
	for _, mount := range additional {
		if managedPaths[pathKey(mount.Path)] {
			return nil, fmt.Errorf("sandbox additional mount %q is already managed by the workspace or Radar configuration", mount.Path)
		}
		managed = append(managed, sandboxMountArgument(mount))
	}
	return normalizeConfiguredMounts(managed)
}
