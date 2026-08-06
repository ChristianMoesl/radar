package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"radar/internal/integration"
	"radar/internal/workspacegroup"
)

type AddWorktreeRequest struct {
	Workspace               string
	Repository              string
	BranchMode              integration.WorkspaceBranchMode
	Name                    string
	Branch                  string
	Base                    string
	WorkspaceRoot           string
	AdditionalSandboxMounts []string
}

type AddWorktreePlan struct {
	WorkspaceID     string   `json:"workspace_id"`
	WorkspaceName   string   `json:"workspace_name"`
	PrimaryPath     string   `json:"primary_path"`
	Repository      string   `json:"repository"`
	BranchMode      string   `json:"branch_mode"`
	Branch          string   `json:"branch"`
	Base            string   `json:"base,omitempty"`
	Path            string   `json:"path"`
	SessionName     string   `json:"session_name,omitempty"`
	SandboxName     string   `json:"sandbox_name,omitempty"`
	RecreateSandbox bool     `json:"recreate_sandbox"`
	SandboxMounts   []string `json:"sandbox_mounts,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
	EnrollWorkspace bool     `json:"enroll_workspace,omitempty"`

	group        workspacegroup.Workspace
	worktreePlan WorktreePlan
}

type AddWorktreeResult struct {
	OK                       bool   `json:"ok"`
	WorkspaceID              string `json:"workspace_id"`
	Path                     string `json:"path"`
	Branch                   string `json:"branch"`
	WorktreeCreated          bool   `json:"worktree_created"`
	WorkspaceMembershipSaved bool   `json:"workspace_membership_saved"`
	SandboxReconciled        bool   `json:"sandbox_reconciled"`
	Retryable                bool   `json:"retryable,omitempty"`
	Warning                  string `json:"warning,omitempty"`
	Error                    string `json:"error,omitempty"`
}

type listedSandbox struct {
	Name       string   `json:"name"`
	Agent      string   `json:"agent"`
	Status     string   `json:"status"`
	KitPath    string   `json:"kit_path"`
	Workspaces []string `json:"workspaces"`
	Mounts     []string `json:"mounts"`
}

func PreviewAddWorktree(ctx context.Context, runner Runner, request AddWorktreeRequest) (AddWorktreePlan, error) {
	if strings.TrimSpace(request.Repository) == "" {
		return AddWorktreePlan{}, fmt.Errorf("repository is required")
	}
	switch request.BranchMode {
	case integration.WorkspaceBranchNew:
		if strings.TrimSpace(request.Name) == "" || strings.TrimSpace(request.Base) == "" || strings.TrimSpace(request.Branch) != "" {
			return AddWorktreePlan{}, fmt.Errorf("new branch mode requires name and base only")
		}
	case integration.WorkspaceBranchExisting:
		if strings.TrimSpace(request.Branch) == "" || strings.TrimSpace(request.Name) != "" || strings.TrimSpace(request.Base) != "" {
			return AddWorktreePlan{}, fmt.Errorf("existing branch mode requires branch only")
		}
	default:
		return AddWorktreePlan{}, fmt.Errorf("branch mode must be new or existing")
	}
	root := strings.TrimSpace(request.WorkspaceRoot)
	var err error
	if root == "" {
		root, err = DefaultRoot()
		if err != nil {
			return AddWorktreePlan{}, err
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return AddWorktreePlan{}, err
	}
	current, err := currentGitTopLevel(ctx, runner, request.Workspace)
	if err != nil {
		return AddWorktreePlan{}, fmt.Errorf("current workspace: %w", err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return AddWorktreePlan{}, err
	}
	group, registered := workspacegroup.FindByMemberPath(registry, current)
	enroll := false
	if !registered {
		group, err = enrollmentPlan(ctx, runner, root, current)
		if err != nil {
			return AddWorktreePlan{}, err
		}
		enroll = true
	}

	name := request.Name
	if request.BranchMode == integration.WorkspaceBranchExisting {
		name = request.Branch
	}
	destination := filepath.Join(root, filepath.Base(filepath.Clean(request.Repository)), WorktreeName(name))
	worktreePlan, err := PlanWorktree(ctx, runner, WorktreeOptions{
		Repo: request.Repository, BranchMode: request.BranchMode, Name: request.Name,
		Branch: request.Branch, Base: request.Base, Path: destination, WorkspaceRoot: root,
		AllowExisting: true,
	})
	if err != nil {
		return AddWorktreePlan{}, err
	}
	for _, member := range group.Members {
		if sameCleanPath(member.Repository, worktreePlan.Repo) {
			if !sameCleanPath(member.Path, worktreePlan.Path) || member.Branch != worktreePlan.Branch {
				return AddWorktreePlan{}, fmt.Errorf("repository %s already belongs to Radar workspace %q", worktreePlan.Repo, group.Name)
			}
		} else if sameCleanPath(member.Path, worktreePlan.Path) {
			return AddWorktreePlan{}, fmt.Errorf("repository basename/path collision at %s", worktreePlan.Path)
		}
	}

	desiredMounts := []string(nil)
	recreate := false
	if group.Sandbox != nil {
		desiredMounts, err = desiredSandboxMounts(ctx, runner, group, worktreePlan, request.AdditionalSandboxMounts)
		if err != nil {
			return AddWorktreePlan{}, err
		}
		actual, found, err := findSandbox(ctx, runner, group.PrimaryPath, group.Sandbox.Name)
		if err != nil {
			return AddWorktreePlan{}, err
		}
		recreate = !found || !sameMountSet(desiredMounts, sandboxWorkspaceMounts(actual))
		group.Sandbox.Mounts = append([]string(nil), desiredMounts...)
	}
	warnings := []string{}
	if recreate {
		warnings = append(warnings, "recreating the sandbox interrupts processes running inside it")
	}
	return AddWorktreePlan{
		WorkspaceID: group.ID, WorkspaceName: group.Name, PrimaryPath: group.PrimaryPath,
		Repository: worktreePlan.Repo, BranchMode: string(worktreePlan.BranchMode), Branch: worktreePlan.Branch,
		Base: worktreePlan.Base, Path: worktreePlan.Path, SessionName: group.SessionName,
		SandboxName: sandboxName(group), RecreateSandbox: recreate, SandboxMounts: desiredMounts,
		Warnings: warnings, EnrollWorkspace: enroll, group: group, worktreePlan: worktreePlan,
	}, nil
}

func ApplyAddWorktree(ctx context.Context, runner Runner, request AddWorktreeRequest) (AddWorktreeResult, error) {
	plan, err := PreviewAddWorktree(ctx, runner, request)
	if err != nil {
		return AddWorktreeResult{}, err
	}
	result := AddWorktreeResult{WorkspaceID: plan.WorkspaceID, Path: plan.Path, Branch: plan.Branch}
	prepared, err := EnsureWorktree(ctx, runner, plan.worktreePlan)
	if err != nil {
		return result, err
	}
	result.WorktreeCreated = prepared.Created || prepared.Plan.Existing

	memberIndex := -1
	for index, member := range plan.group.Members {
		if sameCleanPath(member.Path, plan.Path) {
			memberIndex = index
			break
		}
	}
	if memberIndex < 0 {
		plan.group.Members = append(plan.group.Members, workspacegroup.Member{Repository: plan.Repository, Path: plan.Path, Branch: plan.Branch})
	}
	if plan.group.Sandbox != nil {
		plan.group.Sandbox.Mounts = append([]string(nil), plan.SandboxMounts...)
	}
	root := plan.worktreePlan.Root
	if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
		workspacegroup.Put(registry, plan.group)
		return nil
	}); err != nil {
		result.Error = err.Error()
		result.Retryable = true
		return result, nil
	}
	result.WorkspaceMembershipSaved = true

	if plan.group.Sandbox != nil {
		if err := reconcileSandbox(ctx, runner, plan.group); err != nil {
			result.Error = err.Error()
			result.Retryable = true
			return result, nil
		}
	}
	result.SandboxReconciled = true

	setupWarning := ""
	if index := memberIndexByPath(plan.group.Members, plan.Path); index >= 0 && !plan.group.Members[index].SetupScheduled {
		commands := prepared.Plan.RepoConfig.Setup
		setupErr := scheduleMemberSetup(ctx, runner, plan.Path, plan.group.SessionName, sandboxName(plan.group), filepath.Base(plan.Repository), commands)
		if setupErr != nil {
			setupWarning = fmt.Sprintf("workspace setup could not be started: %v", setupErr)
		} else {
			plan.group.Members[index].SetupScheduled = true
			if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
				workspacegroup.Put(registry, plan.group)
				return nil
			}); err != nil {
				result.Error = err.Error()
				result.Retryable = true
				return result, nil
			}
		}
	}
	result.OK = true
	result.Warning = setupWarning
	return result, nil
}

func enrollmentPlan(ctx context.Context, runner Runner, root, current string) (workspacegroup.Workspace, error) {
	if !isWorkspacePath(current, root) {
		return workspacegroup.Workspace{}, fmt.Errorf("current directory is not a two-level Radar worktree under %s", root)
	}
	branch, err := runner.Run(ctx, current, "git", "branch", "--show-current")
	if err != nil {
		return workspacegroup.Workspace{}, err
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return workspacegroup.Workspace{}, fmt.Errorf("current Radar worktree has no local branch")
	}
	commonDir, err := runner.Run(ctx, current, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return workspacegroup.Workspace{}, err
	}
	repository := filepath.Dir(filepath.Clean(strings.TrimSpace(commonDir)))
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(current), Name: filepath.Base(current), PrimaryPath: current,
		Members: []workspacegroup.Member{{Repository: repository, Path: current, Branch: branch, Primary: true, SetupScheduled: true}},
	}
	group.SessionName = discoverSession(ctx, runner, current)
	if sandbox, found, err := findSandbox(ctx, runner, current, ""); err != nil {
		if runner.LookPath("sbx") == nil {
			return workspacegroup.Workspace{}, err
		}
	} else if found {
		agent := strings.TrimSpace(sandbox.Agent)
		if agent == "" {
			agent = defaultSandboxKitName
		}
		group.Sandbox = &workspacegroup.Sandbox{Name: sandbox.Name, Agent: agent, KitPath: sandbox.KitPath, Mounts: sandboxWorkspaceMounts(sandbox)}
	}
	return group, nil
}

func currentGitTopLevel(ctx context.Context, runner Runner, cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	path, err := runner.Run(ctx, cwd, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(path)), nil
}

func discoverSession(ctx context.Context, runner Runner, primaryPath string) string {
	output, err := runner.Run(ctx, "", "tmux", "list-sessions", "-F", "#{session_name}\t#{session_path}")
	if err != nil {
		return ""
	}
	matches := []string{}
	for _, line := range strings.Split(output, "\n") {
		name, path, ok := strings.Cut(line, "\t")
		if ok && sameCleanPath(path, primaryPath) {
			matches = append(matches, name)
		}
	}
	sort.Strings(matches)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func listSandboxes(ctx context.Context, runner Runner) ([]listedSandbox, error) {
	output, err := runner.Run(ctx, "", "sbx", "ls", "--json")
	if err != nil {
		return nil, sbxCommandError(err)
	}
	var response struct {
		Sandboxes []listedSandbox `json:"sandboxes"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		return nil, fmt.Errorf("unexpected sbx ls output: %w", err)
	}
	return response.Sandboxes, nil
}

func findSandbox(ctx context.Context, runner Runner, primaryPath, preferredName string) (listedSandbox, bool, error) {
	if err := runner.LookPath("sbx"); err != nil {
		return listedSandbox{}, false, nil
	}
	sandboxes, err := listSandboxes(ctx, runner)
	if err != nil {
		return listedSandbox{}, false, err
	}
	if preferredName != "" {
		for _, sandbox := range sandboxes {
			if sandbox.Name == preferredName {
				return sandbox, true, nil
			}
		}
		return listedSandbox{}, false, nil
	}
	matches := []listedSandbox{}
	for _, sandbox := range sandboxes {
		for _, mount := range sandboxWorkspaceMounts(sandbox) {
			if sameCleanPath(strings.TrimSuffix(mount, ":ro"), primaryPath) {
				matches = append(matches, sandbox)
				break
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		iRunning := strings.EqualFold(matches[i].Status, "running")
		jRunning := strings.EqualFold(matches[j].Status, "running")
		if iRunning != jRunning {
			return iRunning
		}
		return matches[i].Name < matches[j].Name
	})
	if len(matches) == 0 {
		return listedSandbox{}, false, nil
	}
	return matches[0], true, nil
}

func sandboxWorkspaceMounts(sandbox listedSandbox) []string {
	mounts := sandbox.Workspaces
	if len(sandbox.Mounts) > 0 {
		mounts = sandbox.Mounts
	}
	return normalizeMountSet(mounts)
}

func desiredSandboxMounts(ctx context.Context, runner Runner, group workspacegroup.Workspace, target WorktreePlan, global []string) ([]string, error) {
	mounts := append([]string(nil), group.Sandbox.Mounts...)
	members := append([]workspacegroup.Member(nil), group.Members...)
	if memberIndexByPath(members, target.Path) < 0 {
		members = append(members, workspacegroup.Member{Repository: target.Repo, Path: target.Path, Branch: target.Branch})
	}
	for _, member := range members {
		mounts = append(mounts, member.Path)
		commonDir, err := runner.Run(ctx, member.Path, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil && sameCleanPath(member.Path, target.Path) && !target.Existing {
			commonDir, err = runner.Run(ctx, target.Repo, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
		}
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
	return normalizeConfiguredMounts(mounts)
}

func normalizeConfiguredMounts(mounts []string) ([]string, error) {
	result := []string{}
	for _, mount := range mounts {
		mount = strings.TrimSpace(mount)
		if mount == "" {
			continue
		}
		suffix := ""
		if strings.HasSuffix(mount, ":ro") {
			mount = strings.TrimSuffix(mount, ":ro")
			suffix = ":ro"
		}
		mount = filepath.Clean(ExpandPath(mount))
		if !filepath.IsAbs(mount) {
			return nil, fmt.Errorf("additional sandbox mount %q must be absolute or start with ~/", mount)
		}
		result = append(result, mount+suffix)
	}
	return normalizeMountSet(result), nil
}

func normalizeMountSet(mounts []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, mount := range mounts {
		mount = strings.TrimSpace(mount)
		if mount == "" {
			continue
		}
		suffix := ""
		if strings.HasSuffix(mount, ":ro") {
			mount = strings.TrimSuffix(mount, ":ro")
			suffix = ":ro"
		}
		mount = filepath.Clean(mount) + suffix
		if !seen[mount] {
			seen[mount] = true
			result = append(result, mount)
		}
	}
	sort.Strings(result)
	return result
}

func sameMountSet(left, right []string) bool {
	left = normalizeMountSet(left)
	right = normalizeMountSet(right)
	return strings.Join(left, "\x00") == strings.Join(right, "\x00")
}

func reconcileSandbox(ctx context.Context, runner Runner, group workspacegroup.Workspace) error {
	sandbox := group.Sandbox
	actual, found, err := findSandbox(ctx, runner, group.PrimaryPath, sandbox.Name)
	if err != nil {
		return err
	}
	if found && sameMountSet(sandbox.Mounts, sandboxWorkspaceMounts(actual)) {
		return nil
	}
	for _, mount := range sandbox.Mounts {
		path := strings.TrimSuffix(mount, ":ro")
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	if _, err := stopSandbox(ctx, runner, group.PrimaryPath, sandbox.Name); err != nil {
		return err
	}
	args := []string{"create", "--name", sandbox.Name}
	if sandbox.KitPath != "" {
		args = append(args, "--kit", sandbox.KitPath)
	}
	args = append(args, sandbox.Agent)
	args = append(args, sandbox.Mounts...)
	if _, err := runner.Run(ctx, group.PrimaryPath, "sbx", args...); err != nil {
		return sbxCommandError(err)
	}
	return nil
}

func scheduleMemberSetup(ctx context.Context, runner Runner, path, sessionName, sandboxName, repositoryName string, commands []string) error {
	if len(commands) == 0 {
		return nil
	}
	if sessionName != "" {
		return scheduleSetupCommandsNamed(ctx, runner, path, sessionName, sandboxName, WorktreeName("setup-"+repositoryName), commands)
	}
	steps := make([]string, 0, len(commands))
	for _, command := range commands {
		steps = append(steps, "sh -lc "+shellQuote(command))
	}
	_, err := runner.Run(ctx, path, "sh", "-lc", "("+strings.Join(steps, " && ")+") >/dev/null 2>&1 &")
	return err
}

func sandboxName(group workspacegroup.Workspace) string {
	if group.Sandbox == nil {
		return ""
	}
	return group.Sandbox.Name
}

func memberIndexByPath(members []workspacegroup.Member, path string) int {
	for index, member := range members {
		if sameCleanPath(member.Path, path) {
			return index
		}
	}
	return -1
}

func sameCleanPath(left, right string) bool {
	return filepath.Clean(strings.TrimSpace(left)) == filepath.Clean(strings.TrimSpace(right))
}
