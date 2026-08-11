package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	WorkspaceID   string            `json:"workspace_id"`
	WorkspaceName string            `json:"workspace_name"`
	Revision      string            `json:"revision"`
	NextRevision  string            `json:"next_revision"`
	PlanID        string            `json:"plan_id"`
	Changes       []WorkspaceChange `json:"changes"`
	Warnings      []string          `json:"warnings,omitempty"`

	root      string
	group     workspacegroup.Workspace
	additions []reconcileAddition
	removals  []workspacegroup.Member
}

type ReconcileWorkspaceResult struct {
	OK                bool   `json:"ok"`
	WorkspaceID       string `json:"workspace_id"`
	Revision          string `json:"revision,omitempty"`
	WorktreesAdded    int    `json:"worktrees_added"`
	WorktreesRemoved  int    `json:"worktrees_removed"`
	SandboxReconciled bool   `json:"sandbox_reconciled"`
	MountsAdded       int    `json:"mounts_added"`
	MountsRemoved     int    `json:"mounts_removed"`
	PortsPublished    int    `json:"ports_published"`
	PortsUnpublished  int    `json:"ports_unpublished"`
	Retryable         bool   `json:"retryable,omitempty"`
	Warning           string `json:"warning,omitempty"`
	Error             string `json:"error,omitempty"`
}

type reconcileAddition struct {
	plan WorktreePlan
}

func PreviewReconcileWorkspace(ctx context.Context, runner Runner, request ReconcileWorkspaceRequest) (ReconcileWorkspacePlan, error) {
	root, _, group, err := resolveWorkspaceGroup(ctx, runner, request.Workspace, request.WorkspaceRoot)
	if err != nil {
		return ReconcileWorkspacePlan{}, err
	}
	actualPorts, _, err := observedSandboxPorts(ctx, runner, group)
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
		return ReconcileWorkspacePlan{}, fmt.Errorf("workspace changed since it was inspected: expected revision %s, current revision is %s", request.Revision, revision)
	}
	if len(request.Desired.Worktrees) == 0 {
		return ReconcileWorkspacePlan{}, fmt.Errorf("desired workspace must contain its primary worktree")
	}

	existingByRepository := make(map[string]workspacegroup.Member, len(group.Members))
	for _, member := range group.Members {
		existingByRepository[pathKey(member.Repository)] = member
	}
	candidate := group
	candidate.Members = nil
	additions := []reconcileAddition{}
	desiredRepositories := map[string]bool{}
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
		key := pathKey(repository)
		if desiredRepositories[key] {
			return ReconcileWorkspacePlan{}, fmt.Errorf("repository %s appears more than once in desired worktrees", repository)
		}
		desiredRepositories[key] = true

		if member, ok := existingByRepository[key]; ok {
			if desired.BranchMode != integration.WorkspaceBranchExisting || strings.TrimSpace(desired.Name) != "" || strings.TrimSpace(desired.Base) != "" {
				return ReconcileWorkspacePlan{}, fmt.Errorf("existing workspace member %s must use existing branch mode", repository)
			}
			branch := normalizeExistingBranch(desired.Branch)
			if branch != member.Branch {
				return ReconcileWorkspacePlan{}, fmt.Errorf("repository %s already belongs to the workspace on branch %q; changing a member branch is not supported", repository, member.Branch)
			}
			candidate.Members = append(candidate.Members, member)
			continue
		}

		name := desired.Name
		if desired.BranchMode == integration.WorkspaceBranchExisting {
			name = desired.Branch
		}
		destination := filepath.Join(root, filepath.Base(repository), WorktreeName(name))
		plan, err := PlanWorktree(ctx, runner, WorktreeOptions{
			Repo: repository, BranchMode: desired.BranchMode, Name: desired.Name,
			Branch: desired.Branch, Base: desired.Base, Path: destination,
			WorkspaceRoot: root, AllowExisting: true,
		})
		if err != nil {
			return ReconcileWorkspacePlan{}, err
		}
		for _, member := range candidate.Members {
			if sameCleanPath(member.Repository, plan.Repo) {
				return ReconcileWorkspacePlan{}, fmt.Errorf("repository %s already belongs to Radar workspace %q", plan.Repo, group.Name)
			}
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
		if desiredRepositories[pathKey(member.Repository)] {
			continue
		}
		if member.Primary {
			return ReconcileWorkspacePlan{}, fmt.Errorf("the primary worktree cannot be removed from its workspace")
		}
		status, statusErr := runner.Run(ctx, "", "git", "-C", member.Path, "status", "--porcelain")
		if statusErr != nil {
			return ReconcileWorkspacePlan{}, statusErr
		}
		if strings.TrimSpace(status) != "" {
			return ReconcileWorkspacePlan{}, fmt.Errorf("workspace member %s has local changes and cannot be removed", member.Path)
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
			changes = append(changes, WorkspaceChange{Action: "recreate", Resource: "sandbox", Summary: fmt.Sprintf("recreate SBX sandbox %s with the desired mounts", group.Sandbox.Name)})
		}
		for _, port := range portDifference(desiredPorts, actualPorts) {
			changes = append(changes, WorkspaceChange{Action: "add", Resource: "sandbox_port", HostPort: port.HostPort, SandboxPort: port.SandboxPort, Summary: fmt.Sprintf("expose localhost:%d to sandbox port %d", port.HostPort, port.SandboxPort)})
		}
		for _, port := range portDifference(actualPorts, desiredPorts) {
			changes = append(changes, WorkspaceChange{Action: "remove", Resource: "sandbox_port", HostPort: port.HostPort, SandboxPort: port.SandboxPort, Summary: fmt.Sprintf("remove localhost:%d exposure for sandbox port %d", port.HostPort, port.SandboxPort)})
		}
	}

	nextRevision, err := workspaceRevision(candidate, sandboxPorts(candidate))
	if err != nil {
		return ReconcileWorkspacePlan{}, err
	}
	warnings := []string{}
	if recreateSandbox {
		warnings = append(warnings, "recreating the sandbox interrupts processes running inside it")
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
		NextRevision: nextRevision, PlanID: planID, Changes: changes, Warnings: warnings,
		root: root, group: candidate, additions: additions, removals: removals,
	}, nil
}

func ApplyReconcileWorkspace(ctx context.Context, runner Runner, request ReconcileWorkspaceRequest) (ReconcileWorkspaceResult, error) {
	plan, err := PreviewReconcileWorkspace(ctx, runner, request)
	if err != nil {
		return ReconcileWorkspaceResult{}, err
	}
	if request.ExpectedPlanID != "" && request.ExpectedPlanID != plan.PlanID {
		return ReconcileWorkspaceResult{}, fmt.Errorf("workspace reconciliation plan changed after confirmation")
	}
	result := ReconcileWorkspaceResult{WorkspaceID: plan.WorkspaceID}
	prepared := make([]WorktreeResult, 0, len(plan.additions))
	for _, addition := range plan.additions {
		created, createErr := EnsureWorktree(ctx, runner, addition.plan)
		if createErr != nil {
			return result, createErr
		}
		prepared = append(prepared, created)
		result.WorktreesAdded++
	}
	for _, removal := range plan.removals {
		if _, removeErr := RemoveWorktree(ctx, runner, removal.Path, false); removeErr != nil {
			return result, removeErr
		}
		result.WorktreesRemoved++
	}
	if err := workspacegroup.Update(plan.root, func(registry *workspacegroup.Registry) error {
		workspacegroup.Put(registry, plan.group)
		return nil
	}); err != nil {
		return result, err
	}

	if plan.group.Sandbox != nil {
		if err := reconcileSandbox(ctx, runner, plan.group); err != nil {
			result.Retryable = true
			result.Error = err.Error()
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
		if err != nil {
			result.Retryable = true
			result.Error = err.Error()
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
		return result, nil
	}
	ports, _, err := observedSandboxPorts(ctx, runner, plan.group)
	if err != nil {
		result.Retryable = true
		result.Error = err.Error()
		return result, nil
	}
	result.Revision, err = workspaceRevision(plan.group, ports)
	if err != nil {
		return result, err
	}
	result.OK = true
	result.Warning = strings.Join(warnings, "; ")
	return result, nil
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
		ID          string                  `json:"id"`
		Name        string                  `json:"name"`
		PrimaryPath string                  `json:"primary_path"`
		SessionName string                  `json:"session_name"`
		Members     []workspacegroup.Member `json:"members"`
		Sandbox     *revisionSandbox        `json:"sandbox"`
	}
	members := append([]workspacegroup.Member(nil), group.Members...)
	for index := range members {
		members[index].SetupScheduled = false
	}
	sort.Slice(members, func(i, j int) bool { return pathKey(members[i].Path) < pathKey(members[j].Path) })
	state := revisionState{ID: group.ID, Name: group.Name, PrimaryPath: group.PrimaryPath, SessionName: group.SessionName, Members: members}
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
	if group.Sandbox == nil {
		return []workspacegroup.SandboxPort{}, false, nil
	}
	_, found, err := findSandbox(ctx, runner, group.PrimaryPath, group.Sandbox.Name)
	if err != nil {
		return nil, false, err
	}
	if !found {
		ports, normalizeErr := normalizeSandboxPorts(group.Sandbox.Ports)
		return ports, false, normalizeErr
	}
	ports, err := listSandboxPorts(ctx, runner, group.Sandbox.Name)
	return ports, true, err
}

func listSandboxPorts(ctx context.Context, runner Runner, name string) ([]workspacegroup.SandboxPort, error) {
	output, err := runner.Run(ctx, "", "sbx", "ports", name, "--json")
	if err != nil {
		return nil, sbxCommandError(err)
	}
	ports, err := parseSandboxPortsJSON([]byte(output))
	if err != nil {
		return nil, fmt.Errorf("unexpected sbx ports output: %w", err)
	}
	return normalizeSandboxPorts(ports)
}

func parseSandboxPortsJSON(data []byte) ([]workspacegroup.SandboxPort, error) {
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
	ports := make([]workspacegroup.SandboxPort, 0, len(items))
	for _, item := range items {
		object, objectOK := item.(map[string]any)
		if !objectOK {
			return nil, fmt.Errorf("expected port objects")
		}
		host, hostOK := integerField(object, "hostport")
		sandbox, sandboxOK := integerField(object, "sandboxport", "containerport", "targetport")
		if !hostOK || !sandboxOK {
			return nil, fmt.Errorf("port object does not contain host_port and sandbox_port")
		}
		ports = append(ports, workspacegroup.SandboxPort{HostPort: host, SandboxPort: sandbox})
	}
	return ports, nil
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
	rightSet := map[string]bool{}
	for _, port := range right {
		rightSet[portKey(port)] = true
	}
	result := []workspacegroup.SandboxPort{}
	for _, port := range left {
		if !rightSet[portKey(port)] {
			result = append(result, port)
		}
	}
	return result
}

func portKey(port workspacegroup.SandboxPort) string {
	return fmt.Sprintf("%d:%d", port.HostPort, port.SandboxPort)
}

func reconcileSandboxPorts(ctx context.Context, runner Runner, name string, desired []workspacegroup.SandboxPort) (int, int, error) {
	actual, err := listSandboxPorts(ctx, runner, name)
	if err != nil {
		return 0, 0, err
	}
	published, unpublished := 0, 0
	for _, port := range portDifference(actual, desired) {
		if _, err := runner.Run(ctx, "", "sbx", "ports", name, "--unpublish", portKey(port)); err != nil {
			return published, unpublished, sbxCommandError(err)
		}
		unpublished++
	}
	for _, port := range portDifference(desired, actual) {
		if _, err := runner.Run(ctx, "", "sbx", "ports", name, "--publish", portKey(port)); err != nil {
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
