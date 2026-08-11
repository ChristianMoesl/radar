package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"radar/internal/workspacegroup"
)

type listedSandbox struct {
	Name       string   `json:"name"`
	Agent      string   `json:"agent"`
	Status     string   `json:"status"`
	KitPath    string   `json:"kit_path"`
	Workspaces []string `json:"workspaces"`
	Mounts     []string `json:"mounts"`
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
