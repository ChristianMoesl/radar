package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"radar/internal/integration/workspace/group"
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
		return workspacegroup.Workspace{}, fmt.Errorf("current directory is not a flat Radar worktree under %s", root)
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
		ID: workspacegroup.ID(current), Name: filepath.Base(current), Path: current,
		Members: []workspacegroup.Member{{Repository: repository, Path: current, Branch: branch, SetupScheduled: true}},
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

func discoverSession(ctx context.Context, runner Runner, workspacePath string) string {
	output, err := runner.Run(ctx, "", "tmux", "list-sessions", "-F", "#{session_name}\t#{session_path}")
	if err != nil {
		return ""
	}
	matches := []string{}
	for _, line := range strings.Split(output, "\n") {
		name, path, ok := strings.Cut(line, "\t")
		if ok && sameCleanPath(path, workspacePath) {
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

func findSandbox(ctx context.Context, runner Runner, workspacePath, preferredName string) (listedSandbox, bool, error) {
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
			if sameCleanPath(strings.TrimSuffix(mount, ":ro"), workspacePath) {
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
	type candidate struct {
		path   string
		suffix string
	}
	seen := map[string]bool{}
	candidates := make([]candidate, 0, len(mounts))
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
		mount = filepath.Clean(mount)
		key := mount + suffix
		if !seen[key] {
			seen[key] = true
			candidates = append(candidates, candidate{path: mount, suffix: suffix})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		iDepth := strings.Count(filepath.Clean(candidates[i].path), string(filepath.Separator))
		jDepth := strings.Count(filepath.Clean(candidates[j].path), string(filepath.Separator))
		if iDepth != jDepth {
			return iDepth < jDepth
		}
		return candidates[i].path+candidates[i].suffix < candidates[j].path+candidates[j].suffix
	})
	kept := make([]candidate, 0, len(candidates))
	for _, current := range candidates {
		redundant := false
		for _, parent := range kept {
			if parent.suffix == current.suffix && pathContains(parent.path, current.path) {
				redundant = true
				break
			}
		}
		if !redundant {
			kept = append(kept, current)
		}
	}
	result := make([]string, 0, len(kept))
	for _, mount := range kept {
		result = append(result, mount.path+mount.suffix)
	}
	sort.Strings(result)
	return result
}

func sameMountSet(left, right []string) bool {
	left = normalizeMountSet(left)
	right = normalizeMountSet(right)
	return strings.Join(left, "\x00") == strings.Join(right, "\x00")
}

const sandboxCreateAttempts = 3

var defaultSandboxReconcilePolicy = sandboxReconcilePolicy{
	createAttempts:  sandboxCreateAttempts,
	removalChecks:   20,
	removalInterval: 250 * time.Millisecond,
	settleDelay:     time.Second,
	backoff:         time.Second,
}

type sandboxReconcilePolicy struct {
	createAttempts  int
	removalChecks   int
	removalInterval time.Duration
	settleDelay     time.Duration
	backoff         time.Duration
}

func reconcileSandbox(ctx context.Context, runner Runner, group workspacegroup.Workspace, logger *slog.Logger) error {
	return reconcileSandboxWithPolicy(ctx, runner, group, logger, defaultSandboxReconcilePolicy)
}

func reconcileSandboxWithPolicy(ctx context.Context, runner Runner, group workspacegroup.Workspace, logger *slog.Logger, policy sandboxReconcilePolicy) error {
	sandbox := group.Sandbox
	actual, found, err := findSandbox(ctx, runner, group.Path, sandbox.Name)
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
	if err := removeSandboxForRecreation(ctx, runner, sandbox.Name, group.ID, "before_create", logger, policy); err != nil {
		return err
	}

	args := []string{"create", "--name", sandbox.Name}
	if sandbox.KitPath != "" {
		args = append(args, "--kit", sandbox.KitPath)
	}
	args = append(args, sandbox.Agent)
	args = append(args, sandbox.Mounts...)
	attempts := max(policy.createAttempts, 1)
	for attempt := 1; attempt <= attempts; attempt++ {
		if logger != nil {
			logger.Info("workspace reconciliation sandbox create attempt",
				"workspace_id", group.ID, "sandbox", sandbox.Name, "attempt", attempt,
				"max_attempts", attempts, "effective_mount_count", len(sandbox.Mounts))
		}
		if _, runErr := runner.Run(ctx, group.Path, "sbx", args...); runErr == nil {
			return nil
		} else {
			createErr := conciseSandboxCreateError(sbxCommandError(runErr))
			if logger != nil {
				logger.Warn("workspace reconciliation sandbox create failed",
					"workspace_id", group.ID, "sandbox", sandbox.Name, "attempt", attempt,
					"max_attempts", attempts, "effective_mount_count", len(sandbox.Mounts), "error", createErr)
			}
			if !retryableSandboxCreateError(createErr) || attempt == attempts {
				cleanupErr := removeSandboxForRecreation(ctx, runner, sandbox.Name, group.ID, "after_failed_create", logger, policy)
				if cleanupErr != nil {
					return fmt.Errorf("create SBX sandbox %s after %d attempt(s): %w; failed to clean up the failed runtime: %v", sandbox.Name, attempt, createErr, cleanupErr)
				}
				return fmt.Errorf("create SBX sandbox %s after %d attempt(s): %w", sandbox.Name, attempt, createErr)
			}
			if cleanupErr := removeSandboxForRecreation(ctx, runner, sandbox.Name, group.ID, "between_create_attempts", logger, policy); cleanupErr != nil {
				return fmt.Errorf("clean up failed SBX sandbox %s after create attempt %d: %w", sandbox.Name, attempt, cleanupErr)
			}
			if err := waitForContext(ctx, time.Duration(attempt)*policy.backoff); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeSandboxForRecreation(ctx context.Context, runner Runner, name, workspaceID, reason string, logger *slog.Logger, policy sandboxReconcilePolicy) error {
	if logger != nil {
		logger.Info("workspace reconciliation sandbox remove attempt",
			"workspace_id", workspaceID, "sandbox", name, "reason", reason)
	}
	if _, err := stopSandbox(ctx, runner, "", name); err != nil {
		return err
	}
	checks := max(policy.removalChecks, 1)
	for check := 0; check < checks; check++ {
		exists, err := sandboxExists(ctx, runner, name)
		if err != nil {
			return err
		}
		if !exists {
			if logger != nil {
				logger.Info("workspace reconciliation sandbox removal completed",
					"workspace_id", workspaceID, "sandbox", name, "checks", check+1)
			}
			return waitForContext(ctx, policy.settleDelay)
		}
		if check+1 < checks {
			if err := waitForContext(ctx, policy.removalInterval); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("SBX sandbox %s still exists after removal", name)
}

func retryableSandboxCreateError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return (strings.Contains(message, "500 internal server error") && strings.Contains(message, "sandbox container")) ||
		strings.Contains(message, "failed to add nic") ||
		strings.Contains(message, "failed to add virtiofs tag") ||
		strings.Contains(message, "failed to register in-process vsock")
}

func conciseSandboxCreateError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if _, detail, found := strings.Cut(message, " failed: "); found {
		message = detail
	}
	return fmt.Errorf("%s", message)
}

func waitForContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
