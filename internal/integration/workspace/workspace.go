package workspace

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"radar/internal/integration"
	"radar/internal/integration/sbx/auth"
	sessionlayout "radar/internal/integration/tmux/layout"
	"radar/internal/integration/workspace/group"
	"radar/internal/pi"
)

var invalidWorkspaceNameCharacters = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

var workspaceGOOS = runtime.GOOS

const defaultSandboxKitName = "shell"
const maxSandboxNameLength = 63
const sandboxNameHashLength = 8
const maxWorktreeDirectoryNameLength = 120
const worktreeDirectoryHashLength = 8

type Runner interface {
	LookPath(name string) error
	Run(ctx context.Context, cwd string, name string, args ...string) (string, error)
}

type ExecRunner struct{}

func (ExecRunner) LookPath(name string) error {
	_, err := exec.LookPath(name)
	return err
}

func (ExecRunner) Run(ctx context.Context, cwd string, name string, args ...string) (string, error) {
	candidates := commandCandidates(name)
	if len(candidates) == 0 {
		candidates = []string{name}
	}
	formatErrors := make([]error, 0)
	for _, candidate := range candidates {
		command := exec.CommandContext(ctx, candidate, args...)
		command.Dir = cwd
		output, err := command.CombinedOutput()
		if err == nil {
			return strings.TrimSpace(string(output)), nil
		}
		if errors.Is(err, syscall.ENOEXEC) && candidate != name {
			formatErrors = append(formatErrors, fmt.Errorf("%s: %w", candidate, err))
			continue
		}
		return "", commandError(name, args, output, err)
	}
	return "", commandError(name, args, nil, errors.Join(formatErrors...))
}

func commandCandidates(name string) []string {
	if strings.Contains(name, string(os.PathSeparator)) {
		return []string{name}
	}
	seen := map[string]bool{}
	candidates := make([]string, 0)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}

func commandError(name string, args []string, output []byte, err error) error {
	detail := strings.TrimSpace(string(output))
	if detail != "" {
		detail += "\n"
	}
	detail += err.Error()
	return fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), detail)
}

type CreateOptions struct {
	Repo                    string
	BranchMode              integration.WorkspaceBranchMode
	Name                    string
	Branch                  string
	Base                    string
	Path                    string
	SessionName             string
	WorkspaceRoot           string
	Model                   string
	Thinking                string
	Sandbox                 bool
	SandboxKitName          string
	SandboxKitPath          string
	AdditionalSandboxMounts []string
	Tmux                    sessionlayout.Config
	Switch                  bool
	ForkPiSession           string
	TaskLinkingKey          string
	NotePath                string
}

type Workspace struct {
	Name        string `json:"name,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Base        string `json:"base,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Path        string `json:"path"`
	SessionName string `json:"session_name"`
	SandboxName string `json:"sandbox_name,omitempty"`
	Warning     string `json:"warning,omitempty"`
}

type CreateSessionOptions struct {
	Path                    string
	SessionName             string
	TaskLinkingKey          string
	InitialPrompt           string
	Environment             map[string]string
	Model                   string
	Thinking                string
	Sandbox                 bool
	SandboxKitName          string
	SandboxKitPath          string
	AdditionalSandboxMounts []string
	SandboxName             string
	Tmux                    sessionlayout.Config
	Switch                  bool
}

func Create(ctx context.Context, runner Runner, options CreateOptions) (Workspace, error) {
	if strings.TrimSpace(options.Repo) == "" {
		return createNoteWorkspace(ctx, runner, options)
	}
	for _, dependency := range []string{"git", "tmux"} {
		if err := runner.LookPath(dependency); err != nil {
			return Workspace{}, fmt.Errorf("workspace creation requires %q: %w", dependency, err)
		}
	}
	if err := pi.ValidateThinking(options.Thinking); err != nil {
		return Workspace{}, err
	}
	if err := sessionlayout.Validate(options.Tmux); err != nil {
		return Workspace{}, err
	}
	root, err := workspaceRoot(options.WorkspaceRoot)
	if err != nil {
		return Workspace{}, err
	}
	repository, err := canonicalRepository(ctx, runner, options.Repo)
	if err != nil {
		return Workspace{}, err
	}
	name := strings.TrimSpace(options.Name)
	branch := normalizeExistingBranch(options.Branch)
	if options.BranchMode == integration.WorkspaceBranchExisting && name == "" {
		name = branch
	}
	if options.BranchMode == integration.WorkspaceBranchNew {
		if name == "" {
			return Workspace{}, fmt.Errorf("new branch name is required")
		}
		if branch == "" {
			branch = BranchName(name)
		}
	}
	if existing, found, err := existingWorkspaceForTask(root, options.TaskLinkingKey); err != nil {
		return Workspace{}, err
	} else if found {
		return openRegisteredWorkspace(ctx, runner, root, existing, options, repository, branch)
	}
	if existing, _, found, err := existingWorkspaceForMember(root, repository, branch); err != nil {
		return Workspace{}, err
	} else if found {
		if err := validateTaskLink(existing.Path, existing.TaskLinkingKey, options.TaskLinkingKey); err != nil {
			return Workspace{}, err
		}
		return openRegisteredWorkspace(ctx, runner, root, existing, options, repository, branch)
	}

	anchor := strings.TrimSpace(options.Path)
	if anchor == "" {
		anchor, err = workspaceAnchorPath(root, name, options.TaskLinkingKey)
	} else {
		anchor, err = filepath.Abs(anchor)
	}
	if err != nil {
		return Workspace{}, err
	}
	anchor = filepath.Clean(anchor)
	if !isWorkspacePath(anchor, root) {
		return Workspace{}, fmt.Errorf("workspace anchor must be a direct child of %s", root)
	}
	destination := filepath.Join(anchor, WorktreeDirectoryName(repository, branch))
	plan, err := PlanWorktree(ctx, runner, WorktreeOptions{
		Repo: repository, BranchMode: options.BranchMode, Name: name, Branch: options.Branch,
		Base: options.Base, Path: destination, WorkspaceRoot: anchor,
	})
	if err != nil {
		return Workspace{}, err
	}
	sandbox := workspaceSandboxConfig(plan.RepoConfig, options.Sandbox, options.SandboxKitName, options.SandboxKitPath, options.AdditionalSandboxMounts)
	if err := validateSandboxDependencies(runner, sandbox.Enabled); err != nil {
		return Workspace{}, err
	}
	if err := createAnchorDirectory(root, anchor); err != nil {
		return Workspace{}, err
	}
	if options.NotePath != "" {
		if err := ensureNoteLink(anchor, options.NotePath); err != nil {
			removeEmptyAnchor(anchor)
			return Workspace{}, err
		}
	}
	prepared, err := EnsureWorktree(ctx, runner, plan)
	if err != nil {
		removeManagedNoteLink(anchor)
		removeEmptyAnchor(anchor)
		return Workspace{}, err
	}

	sessionName := strings.TrimSpace(options.SessionName)
	if sessionName == "" {
		sessionName = SessionName(filepath.Base(repository), name)
	}
	sandboxName := ""
	if sandbox.Enabled {
		sandboxName = SandboxName(filepath.Base(repository), name)
	}
	model := options.Model
	if strings.TrimSpace(plan.RepoConfig.Model) != "" {
		model = plan.RepoConfig.Model
	}
	thinking := options.Thinking
	if strings.TrimSpace(plan.RepoConfig.Thinking) != "" {
		thinking = plan.RepoConfig.Thinking
	}
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: name, Path: anchor, SessionName: sessionName,
		TaskLinkingKey: strings.TrimSpace(options.TaskLinkingKey), NotePath: cleanOptionalAbsolutePath(options.NotePath),
		Model: strings.TrimSpace(model), Thinking: strings.TrimSpace(thinking), Tmux: options.Tmux,
		Members: []workspacegroup.Member{{Repository: plan.Repo, Path: plan.Path, Branch: plan.Branch}},
	}
	if sandbox.Enabled {
		group.Sandbox = &workspacegroup.Sandbox{Name: sandboxName, Agent: sandbox.Kit.Name, KitPath: ExpandPath(sandbox.Kit.Path), AdditionalMounts: []workspacegroup.SandboxMount{}, Ports: []workspacegroup.SandboxPort{}}
		mounts, mountErr := desiredReconciledSandboxMounts(ctx, runner, group, nil, options.AdditionalSandboxMounts, nil)
		if mountErr != nil {
			rollbackCreatedWorktree(ctx, runner, prepared, anchor)
			return Workspace{}, mountErr
		}
		group.Sandbox.Mounts = mounts
	}
	createdSession, createdSandbox, err := startWorkspaceRuntime(ctx, runner, group, options.ForkPiSession)
	if err != nil {
		rollbackWorkspaceRuntime(ctx, runner, group, createdSession, createdSandbox)
		rollbackCreatedWorktree(ctx, runner, prepared, anchor)
		return Workspace{}, err
	}
	setupScheduled := len(plan.RepoConfig.Setup) == 0
	warning := ""
	if err := scheduleSetupCommandsNamed(ctx, runner, plan.Path, sessionName, sandboxName, "setup", plan.RepoConfig.Setup); err != nil {
		warning = fmt.Sprintf("workspace setup could not be started: %v", err)
	} else {
		setupScheduled = true
	}
	group.Members[0].SetupScheduled = setupScheduled
	if err := registerWorkspace(root, group); err != nil {
		rollbackWorkspaceRuntime(ctx, runner, group, createdSession, createdSandbox)
		rollbackCreatedWorktree(ctx, runner, prepared, anchor)
		return Workspace{}, err
	}
	if options.Switch {
		if _, err := runner.Run(ctx, anchor, "tmux", "switch-client", "-t", sessionName); err != nil {
			return Workspace{}, err
		}
	}
	return Workspace{Name: name, Branch: plan.Branch, Base: plan.Base, Repo: plan.Repo, Path: anchor, SessionName: sessionName, SandboxName: sandboxName, Warning: warning}, nil
}

func createNoteWorkspace(ctx context.Context, runner Runner, options CreateOptions) (Workspace, error) {
	if strings.TrimSpace(options.NotePath) == "" {
		return Workspace{}, fmt.Errorf("workspace repository or note path is required")
	}
	if strings.TrimSpace(options.TaskLinkingKey) == "" {
		return Workspace{}, fmt.Errorf("note workspace task linking key is required")
	}
	if err := runner.LookPath("tmux"); err != nil {
		return Workspace{}, fmt.Errorf("workspace creation requires %q: %w", "tmux", err)
	}
	if err := pi.ValidateThinking(options.Thinking); err != nil {
		return Workspace{}, err
	}
	if err := sessionlayout.Validate(options.Tmux); err != nil {
		return Workspace{}, err
	}
	root, err := workspaceRoot(options.WorkspaceRoot)
	if err != nil {
		return Workspace{}, err
	}
	name := strings.TrimSpace(options.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(options.NotePath), filepath.Ext(options.NotePath))
	}
	if existing, found, err := existingWorkspaceForTask(root, options.TaskLinkingKey); err != nil {
		return Workspace{}, err
	} else if found {
		return openRegisteredWorkspace(ctx, runner, root, existing, options, "", "")
	}
	anchor := strings.TrimSpace(options.Path)
	if anchor == "" {
		anchor, err = workspaceAnchorPath(root, name, options.TaskLinkingKey)
	} else {
		anchor, err = filepath.Abs(anchor)
	}
	if err != nil {
		return Workspace{}, err
	}
	anchor = filepath.Clean(anchor)
	if !isWorkspacePath(anchor, root) {
		return Workspace{}, fmt.Errorf("workspace anchor must be a direct child of %s", root)
	}
	sandbox := workspaceSandboxConfig(RepoConfig{}, options.Sandbox, options.SandboxKitName, options.SandboxKitPath, options.AdditionalSandboxMounts)
	if err := validateSandboxDependencies(runner, sandbox.Enabled); err != nil {
		return Workspace{}, err
	}
	if err := createAnchorDirectory(root, anchor); err != nil {
		return Workspace{}, err
	}
	if err := ensureNoteLink(anchor, options.NotePath); err != nil {
		removeEmptyAnchor(anchor)
		return Workspace{}, err
	}
	sessionName := strings.TrimSpace(options.SessionName)
	if sessionName == "" {
		sessionName = WorktreeName(name)
	}
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: name, Path: anchor, SessionName: sessionName,
		TaskLinkingKey: strings.TrimSpace(options.TaskLinkingKey), NotePath: cleanOptionalAbsolutePath(options.NotePath),
		Model: strings.TrimSpace(options.Model), Thinking: strings.TrimSpace(options.Thinking), Tmux: options.Tmux,
		Members: []workspacegroup.Member{},
	}
	if sandbox.Enabled {
		group.Sandbox = &workspacegroup.Sandbox{Name: SandboxName("workspace", name), Agent: sandbox.Kit.Name, KitPath: ExpandPath(sandbox.Kit.Path), AdditionalMounts: []workspacegroup.SandboxMount{}, Ports: []workspacegroup.SandboxPort{}}
		mounts, mountErr := desiredReconciledSandboxMounts(ctx, runner, group, nil, options.AdditionalSandboxMounts, nil)
		if mountErr != nil {
			removeManagedNoteLink(anchor)
			removeEmptyAnchor(anchor)
			return Workspace{}, mountErr
		}
		group.Sandbox.Mounts = mounts
	}
	createdSession, createdSandbox, err := startWorkspaceRuntime(ctx, runner, group, "")
	if err != nil {
		rollbackWorkspaceRuntime(ctx, runner, group, createdSession, createdSandbox)
		removeManagedNoteLink(anchor)
		removeEmptyAnchor(anchor)
		return Workspace{}, err
	}
	if err := registerWorkspace(root, group); err != nil {
		rollbackWorkspaceRuntime(ctx, runner, group, createdSession, createdSandbox)
		removeManagedNoteLink(anchor)
		removeEmptyAnchor(anchor)
		return Workspace{}, err
	}
	if options.Switch {
		if _, err := runner.Run(ctx, anchor, "tmux", "switch-client", "-t", sessionName); err != nil {
			return Workspace{}, err
		}
	}
	return Workspace{Name: name, Path: anchor, SessionName: sessionName, SandboxName: sandboxName(group)}, nil
}

func OpenRegisteredWorkspace(ctx context.Context, runner Runner, current string, switchClient bool) (Workspace, error) {
	_, group, found, err := RegisteredWorkspace(current, "")
	if err != nil {
		return Workspace{}, err
	}
	if !found {
		return Workspace{}, fmt.Errorf("no registered Radar workspace contains %s", current)
	}
	createdSession, createdSandbox, err := startWorkspaceRuntime(ctx, runner, group, "")
	if err != nil {
		rollbackWorkspaceRuntime(ctx, runner, group, createdSession, createdSandbox)
		return Workspace{}, err
	}
	if switchClient {
		if _, err := runner.Run(ctx, group.Path, "tmux", "switch-client", "-t", group.SessionName); err != nil {
			return Workspace{}, err
		}
	}
	return Workspace{Name: group.Name, Path: group.Path, SessionName: group.SessionName, SandboxName: sandboxName(group)}, nil
}

func openRegisteredWorkspace(ctx context.Context, runner Runner, root string, group workspacegroup.Workspace, options CreateOptions, repository, branch string) (Workspace, error) {
	if options.NotePath != "" {
		if err := ensureNoteLink(group.Path, options.NotePath); err != nil {
			return Workspace{}, err
		}
		group.NotePath = cleanOptionalAbsolutePath(options.NotePath)
	}
	if options.TaskLinkingKey != "" {
		if err := validateTaskLink(group.Path, group.TaskLinkingKey, options.TaskLinkingKey); err != nil {
			return Workspace{}, err
		}
		group.TaskLinkingKey = strings.TrimSpace(options.TaskLinkingKey)
	}
	if err := registerWorkspace(root, group); err != nil {
		return Workspace{}, err
	}
	createdSession, createdSandbox, err := startWorkspaceRuntime(ctx, runner, group, options.ForkPiSession)
	if err != nil {
		rollbackWorkspaceRuntime(ctx, runner, group, createdSession, createdSandbox)
		return Workspace{}, err
	}
	if options.Switch {
		if _, err := runner.Run(ctx, group.Path, "tmux", "switch-client", "-t", group.SessionName); err != nil {
			return Workspace{}, err
		}
	}
	return Workspace{Name: group.Name, Branch: branch, Repo: repository, Path: group.Path, SessionName: group.SessionName, SandboxName: sandboxName(group)}, nil
}

func startWorkspaceRuntime(ctx context.Context, runner Runner, group workspacegroup.Workspace, forkSession string) (bool, bool, error) {
	createdSandbox := false
	if group.Sandbox != nil {
		exists, err := sandboxExists(ctx, runner, group.Sandbox.Name)
		if err != nil {
			return false, false, err
		}
		if !exists {
			if _, err := startSandboxWithMounts(ctx, runner, group.Path, group.Sandbox.Name, SandboxKitConfig{Name: group.Sandbox.Agent, Path: group.Sandbox.KitPath}, group.Sandbox.Mounts); err != nil {
				return false, false, err
			}
			createdSandbox = true
		}
	}
	if _, err := runner.Run(ctx, group.Path, "tmux", "has-session", "-t", group.SessionName); err == nil {
		return false, createdSandbox, nil
	}
	piArgsText, environment, err := radarPiLaunch(piArgsWithPrompt(taskPiSessionID(group.SessionName, group.TaskLinkingKey), group.SessionName, group.Model, group.Thinking, forkSession, ""), nil)
	if err != nil {
		return false, createdSandbox, err
	}
	if err := createTmuxWorkspace(ctx, runner, group.Path, group.Path, group.SessionName, group.Tmux, piArgsText, environment); err != nil {
		return false, createdSandbox, err
	}
	return true, createdSandbox, nil
}

func rollbackWorkspaceRuntime(ctx context.Context, runner Runner, group workspacegroup.Workspace, session, sandbox bool) {
	if session {
		_, _ = runner.Run(ctx, group.Path, "tmux", "kill-session", "-t", group.SessionName)
	}
	if sandbox && group.Sandbox != nil {
		_, _ = stopSandbox(ctx, runner, group.Path, group.Sandbox.Name)
	}
}

func rollbackCreatedWorktree(ctx context.Context, runner Runner, prepared WorktreeResult, anchor string) {
	if prepared.Created {
		_, _ = runner.Run(ctx, prepared.Plan.Repo, "git", "worktree", "remove", "--force", prepared.Plan.Path)
	}
	removeManagedNoteLink(anchor)
	removeEmptyAnchor(anchor)
}

func createAnchorDirectory(root, anchor string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(anchor, 0o755); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("workspace anchor is occupied by unrelated content: %s", anchor)
		}
		return err
	}
	return nil
}

func removeManagedNoteLink(anchor string) {
	path := filepath.Join(anchor, "note.md")
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(path)
	}
}

func removeEmptyAnchor(anchor string) { _ = os.Remove(anchor) }

func workspaceRoot(configured string) (string, error) {
	root := strings.TrimSpace(configured)
	var err error
	if root == "" {
		root, err = DefaultRoot()
		if err != nil {
			return "", err
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Clean(root), nil
}

func canonicalRepository(ctx context.Context, runner Runner, repository string) (string, error) {
	path, err := runner.Run(ctx, repository, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}

func cleanOptionalAbsolutePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(absolute)
}

func ensureNoteLink(anchor, notePath string) error {
	notePath = cleanOptionalAbsolutePath(notePath)
	info, err := os.Lstat(notePath)
	if err != nil {
		return fmt.Errorf("canonical task note %s: %w", notePath, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("canonical task note must be a regular file: %s", notePath)
	}
	link := filepath.Join(anchor, "note.md")
	if current, err := os.Readlink(link); err == nil {
		if filepath.Clean(current) == notePath {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("Radar managed note path is occupied: %s", link)
	}
	temporary := filepath.Join(anchor, fmt.Sprintf(".note-%d.tmp", os.Getpid()))
	_ = os.Remove(temporary)
	if err := os.Symlink(notePath, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, link); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// RefreshWorkspaceNote repairs the managed note link after an Obsidian file
// rename. It never searches outside the note's stable task directory.
func RefreshWorkspaceNote(root string, group workspacegroup.Workspace) (workspacegroup.Workspace, error) {
	if group.NotePath == "" {
		return group, nil
	}
	if info, err := os.Lstat(group.NotePath); err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return group, ensureNoteLink(group.Path, group.NotePath)
	} else if err != nil && !os.IsNotExist(err) {
		return group, err
	}
	directory := filepath.Dir(group.NotePath)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return group, err
	}
	candidates := make([]string, 0, 1)
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			candidates = append(candidates, filepath.Join(directory, entry.Name()))
		}
	}
	if len(candidates) != 1 {
		return group, fmt.Errorf("canonical task directory %s contains %d Markdown notes", directory, len(candidates))
	}
	expectedID := strings.TrimPrefix(group.TaskLinkingKey, "obsidian:task:")
	if expectedID == group.TaskLinkingKey || expectedID == "" {
		return group, fmt.Errorf("workspace %s has no Obsidian task identity", group.Path)
	}
	data, err := os.ReadFile(candidates[0])
	if err != nil {
		return group, err
	}
	foundID := ""
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "radar-id" {
			foundID = strings.TrimSpace(value)
			break
		}
	}
	if foundID != expectedID {
		return group, fmt.Errorf("task identity changed in %s", candidates[0])
	}
	group.NotePath = filepath.Clean(candidates[0])
	if err := ensureNoteLink(group.Path, group.NotePath); err != nil {
		return group, err
	}
	if err := registerWorkspace(root, group); err != nil {
		return group, err
	}
	return group, nil
}

func validateSandboxDependencies(runner Runner, enabled bool) error {
	if !enabled {
		return nil
	}
	if workspaceGOOS != "darwin" {
		return fmt.Errorf("workspace sandbox is only supported on macOS")
	}
	if err := runner.LookPath("sbx"); err != nil {
		return fmt.Errorf("workspace sandbox requires %q: %w", "sbx", err)
	}
	return nil
}

type sandboxSettings struct {
	Enabled          bool
	Kit              SandboxKitConfig
	AdditionalMounts []string
}

func workspaceSandboxConfig(repoConfig RepoConfig, enabled bool, kitName string, kitPath string, additionalMounts []string) sandboxSettings {
	settings := sandboxSettings{
		Enabled: enabled,
		Kit: SandboxKitConfig{
			Name: strings.TrimSpace(kitName),
			Path: strings.TrimSpace(kitPath),
		},
		AdditionalMounts: append([]string(nil), additionalMounts...),
	}
	if settings.Kit.Name == "" {
		settings.Kit.Name = defaultSandboxKitName
	}
	if repoConfig.SBX == nil {
		return settings
	}
	if repoConfig.SBX.Enabled != nil {
		settings.Enabled = *repoConfig.SBX.Enabled
	}
	if repoConfig.SBX.Kit != nil {
		settings.Kit = SandboxKitConfig{
			Name: strings.TrimSpace(repoConfig.SBX.Kit.Name),
			Path: strings.TrimSpace(repoConfig.SBX.Kit.Path),
		}
	}
	settings.AdditionalMounts = append(settings.AdditionalMounts, repoConfig.SBX.AdditionalMounts...)
	return settings
}

func openExistingWorkspace(ctx context.Context, runner Runner, workspace Workspace, options CreateOptions) (Workspace, error) {
	created, err := CreateSessionWithOptions(ctx, runner, CreateSessionOptions{
		Path:                    workspace.Path,
		SessionName:             workspace.SessionName,
		TaskLinkingKey:          options.TaskLinkingKey,
		Model:                   options.Model,
		Thinking:                options.Thinking,
		Sandbox:                 options.Sandbox,
		SandboxKitName:          options.SandboxKitName,
		SandboxKitPath:          options.SandboxKitPath,
		AdditionalSandboxMounts: options.AdditionalSandboxMounts,
		SandboxName:             workspace.SandboxName,
		Tmux:                    options.Tmux,
		Switch:                  options.Switch,
	})
	if err != nil {
		return Workspace{}, err
	}
	workspace.Path = created.Path
	workspace.SessionName = created.SessionName
	return workspace, nil
}

func normalizeExistingBranch(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/remotes/origin/")
	branch = strings.TrimPrefix(branch, "origin/")
	return strings.TrimPrefix(branch, "refs/heads/")
}

func refExists(ctx context.Context, runner Runner, repo string, ref string) bool {
	_, err := runner.Run(ctx, repo, "git", "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

func isWorkspacePath(path string, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return false
	}
	return !strings.Contains(relative, string(os.PathSeparator))
}

func worktreePathForBranch(ctx context.Context, runner Runner, repo string, branch string) (string, bool, error) {
	output, err := runner.Run(ctx, repo, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return "", false, err
	}
	var currentPath string
	var currentBranch string
	flush := func() (string, bool) {
		if currentPath != "" && currentBranch == branch {
			return currentPath, true
		}
		return "", false
	}
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			if path, ok := flush(); ok {
				return path, true, nil
			}
			currentPath = ""
			currentBranch = ""
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			currentPath = value
		case "branch":
			currentBranch = strings.TrimPrefix(value, "refs/heads/")
		}
	}
	if path, ok := flush(); ok {
		return path, true, nil
	}
	return "", false, nil
}

func CreateSession(ctx context.Context, runner Runner, path string, sessionName string, switchClient bool) (Workspace, error) {
	return CreateSessionWithOptions(ctx, runner, CreateSessionOptions{Path: path, SessionName: sessionName, Switch: switchClient})
}

func CreateSessionWithOptions(ctx context.Context, runner Runner, options CreateSessionOptions) (Workspace, error) {
	for _, dependency := range []string{"tmux"} {
		if err := runner.LookPath(dependency); err != nil {
			return Workspace{}, fmt.Errorf("workspace session creation requires %q: %w", dependency, err)
		}
	}
	if strings.TrimSpace(options.Path) == "" {
		return Workspace{}, fmt.Errorf("workspace path is required")
	}
	path, err := filepath.Abs(options.Path)
	if err != nil {
		return Workspace{}, err
	}
	repoConfig, err := loadRepoConfig(path)
	if err != nil {
		return Workspace{}, err
	}
	if err := pi.ValidateThinking(options.Thinking); err != nil {
		return Workspace{}, err
	}
	if err := sessionlayout.Validate(options.Tmux); err != nil {
		return Workspace{}, err
	}
	sandbox := workspaceSandboxConfig(repoConfig, options.Sandbox, options.SandboxKitName, options.SandboxKitPath, options.AdditionalSandboxMounts)
	sandboxName := strings.TrimSpace(options.SandboxName)
	if sandboxName != "" {
		sandbox.Enabled = true
	}
	if sandbox.Enabled {
		if workspaceGOOS != "darwin" {
			return Workspace{}, fmt.Errorf("workspace sandbox is only supported on macOS")
		}
		if err := runner.LookPath("sbx"); err != nil {
			return Workspace{}, fmt.Errorf("workspace sandbox requires %q: %w", "sbx", err)
		}
	}
	sessionName := options.SessionName
	if sessionName == "" {
		sessionName = SessionName(filepath.Base(filepath.Dir(path)), filepath.Base(path))
	}
	if sandbox.Enabled && sandboxName == "" {
		sandboxName = SandboxName(filepath.Base(filepath.Dir(path)), filepath.Base(path))
	}
	if _, err := runner.Run(ctx, "", "tmux", "has-session", "-t", sessionName); err != nil {
		model := options.Model
		if strings.TrimSpace(repoConfig.Model) != "" {
			model = repoConfig.Model
		}
		thinking := options.Thinking
		if strings.TrimSpace(repoConfig.Thinking) != "" {
			thinking = repoConfig.Thinking
		}
		piSessionID := taskPiSessionID(sessionName, options.TaskLinkingKey)
		piArgsText, environment, err := radarPiLaunch(piArgsWithPrompt(piSessionID, sessionName, model, thinking, "", options.InitialPrompt), options.Environment)
		if err != nil {
			return Workspace{}, err
		}
		createdSandbox := false
		if sandbox.Enabled {
			if exists, err := sandboxExists(ctx, runner, sandboxName); err != nil {
				return Workspace{}, err
			} else if !exists {
				if _, err := startSandbox(ctx, runner, path, sandboxName, sandbox.Kit, sandbox.AdditionalMounts); err != nil {
					return Workspace{}, err
				}
				createdSandbox = true
			}
		}
		if err := createTmuxWorkspace(ctx, runner, "", path, sessionName, options.Tmux, piArgsText, environment); err != nil {
			if createdSandbox {
				_, _ = stopSandbox(ctx, runner, path, sandboxName)
			}
			return Workspace{}, err
		}
	}
	if options.Switch {
		if _, err := runner.Run(ctx, "", "tmux", "switch-client", "-t", sessionName); err != nil {
			return Workspace{}, err
		}
	}
	return Workspace{Path: path, SessionName: sessionName, SandboxName: sandboxName}, nil
}

func RemoveSession(ctx context.Context, runner Runner, sessionName string) (Workspace, error) {
	if strings.TrimSpace(sessionName) == "" {
		return Workspace{}, fmt.Errorf("tmux session is required")
	}
	if _, err := runner.Run(ctx, "", "tmux", "has-session", "-t", sessionName); err != nil {
		return Workspace{SessionName: sessionName}, nil
	}
	if _, err := runner.Run(ctx, "", "tmux", "kill-session", "-t", sessionName); err != nil {
		return Workspace{}, err
	}
	return Workspace{SessionName: sessionName}, nil
}

func RemoveWorktree(ctx context.Context, runner Runner, path string, force bool) (Workspace, error) {
	if strings.TrimSpace(path) == "" {
		return Workspace{}, fmt.Errorf("workspace path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return Workspace{}, err
	}
	status, err := runner.Run(ctx, "", "git", "-C", path, "status", "--porcelain")
	if err != nil {
		return Workspace{}, err
	}
	if status != "" && !force {
		return Workspace{}, fmt.Errorf("workspace has local changes; force is required to clean it up")
	}
	args := []string{"-C", path, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	if _, err := runner.Run(ctx, "", "git", args...); err != nil {
		return Workspace{}, err
	}
	return Workspace{Path: path}, nil
}

func WorktreeName(workspaceName string) string {
	name := invalidWorkspaceNameCharacters.ReplaceAllString(workspaceName, "-")
	name = strings.Trim(name, "-_")
	if name == "" {
		return "workspace"
	}
	return name
}

func WorkspaceDirectoryName(workspaceName string) string {
	name := WorktreeName(workspaceName)
	if len(name) <= maxWorktreeDirectoryNameLength {
		return name
	}
	sum := sha1.Sum([]byte(name))
	hash := hex.EncodeToString(sum[:])[:worktreeDirectoryHashLength]
	prefixLength := maxWorktreeDirectoryNameLength - len(hash) - 2
	prefix := strings.TrimRight(name[:prefixLength], "-_")
	return prefix + "--" + hash
}

func WorktreeDirectoryName(repository string, workspaceName string) string {
	name := WorktreeName(filepath.Base(filepath.Clean(repository)) + "--" + workspaceName)
	if len(name) <= maxWorktreeDirectoryNameLength {
		return name
	}
	sum := sha1.Sum([]byte(name))
	hash := hex.EncodeToString(sum[:])[:worktreeDirectoryHashLength]
	prefixLength := maxWorktreeDirectoryNameLength - len(hash) - 2
	prefix := strings.TrimRight(name[:prefixLength], "-_")
	return prefix + "--" + hash
}

func BranchName(workspaceName string) string {
	name := WorktreeName(workspaceName)
	if name == "HEAD" {
		return "workspace-HEAD"
	}
	return name
}

func SessionName(repoName string, workspaceName string) string {
	return WorktreeName(repoName + "-" + workspaceName)
}

func SandboxName(repoName string, workspaceName string) string {
	slug := WorktreeName(workspaceName)
	hash := sandboxNameHash(repoName, workspaceName)
	suffixLength := 1 + len(hash)
	maxSlugLength := maxSandboxNameLength - suffixLength
	if len(slug) > maxSlugLength {
		slug = strings.Trim(slug[:maxSlugLength], "-_")
		if slug == "" {
			slug = "workspace"
		}
	}
	return slug + "-" + hash
}

func sandboxNameHash(repoName string, workspaceName string) string {
	sum := sha1.Sum([]byte(WorktreeName(repoName) + "\x00" + WorktreeName(workspaceName)))
	return hex.EncodeToString(sum[:])[:sandboxNameHashLength]
}

func copyConfiguredFiles(source string, target string, names []string) error {
	for _, name := range names {
		from := filepath.Join(source, name)
		to := filepath.Join(target, name)
		if _, err := os.Stat(to); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		info, err := os.Stat(from)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("configured copy path is not a file: %s", from)
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		if err := copyFile(from, to, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source string, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

func scheduleSetupCommands(ctx context.Context, runner Runner, path string, sessionName string, sandboxName string, commands []string) error {
	return scheduleSetupCommandsNamed(ctx, runner, path, sessionName, sandboxName, "setup", commands)
}

func scheduleSetupCommandsNamed(ctx context.Context, runner Runner, path string, sessionName string, sandboxName string, windowName string, commands []string) error {
	if len(commands) == 0 {
		return nil
	}
	args := []string{"new-window", "-t", sessionName + ":", "-n", windowName, "-c", path, "-P", "-F", "#{window_id} #{pane_id}"}
	if sandboxName != "" {
		args = append(args, strings.Join([]string{
			"sbx exec -it --workdir",
			shellQuote(path),
			shellQuote(sandboxName),
			"sh -i",
		}, " "))
	}
	output, err := runner.Run(ctx, path, "tmux", args...)
	if err != nil {
		return err
	}
	_, paneID, err := parseTmuxIDs(output)
	if err != nil {
		return err
	}
	if _, err := runner.Run(ctx, path, "tmux", "set-option", "-p", "-t", paneID, "remain-on-exit", "off"); err != nil {
		return err
	}
	if _, err := runner.Run(ctx, path, "tmux", "send-keys", "-l", "-t", paneID, setupInteractiveCommand(commands)); err != nil {
		return err
	}
	_, err = runner.Run(ctx, path, "tmux", "send-keys", "-t", paneID, "Enter")
	return err
}

func setupInteractiveCommand(commands []string) string {
	steps := make([]string, 0, len(commands)+1)
	for _, command := range commands {
		steps = append(steps, "sh -lc "+shellQuote(command))
	}
	steps = append(steps, "exit")
	return strings.Join(steps, " && ")
}

func startSandbox(ctx context.Context, runner Runner, path string, name string, kit SandboxKitConfig, additionalMounts []string) (string, error) {
	mounts, err := sandboxMounts(ctx, runner, path, additionalMounts)
	if err != nil {
		return "", err
	}
	return startSandboxWithMounts(ctx, runner, path, name, kit, mounts)
}

func startSandboxWithMounts(ctx context.Context, runner Runner, path string, name string, kit SandboxKitConfig, mounts []string) (string, error) {
	args := []string{"create", "--name", name}
	if kit.Path != "" {
		args = append(args, "--kit", ExpandPath(kit.Path))
	}
	args = append(args, kit.Name)
	args = append(args, mounts...)
	output, err := runner.Run(ctx, path, "sbx", args...)
	if err != nil {
		return output, sbxCommandError(err)
	}
	return output, nil
}

func sandboxMounts(ctx context.Context, runner Runner, path string, additionalMounts []string) ([]string, error) {
	commonDir, err := runner.Run(ctx, path, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	commonDir = strings.TrimSpace(commonDir)
	if commonDir == "" {
		return nil, fmt.Errorf("git common directory is empty for workspace %s", path)
	}
	mounts := []string{path}
	if !pathContains(path, commonDir) {
		mounts = append(mounts, commonDir)
	}
	for _, configuredMount := range additionalMounts {
		mount := strings.TrimSpace(configuredMount)
		if mount == "" {
			continue
		}
		mount = filepath.Clean(ExpandPath(mount))
		if !filepath.IsAbs(mount) {
			return nil, fmt.Errorf("additional sandbox mount %q must be absolute or start with ~/", configuredMount)
		}
		if err := os.MkdirAll(mount, 0o755); err != nil {
			return nil, fmt.Errorf("create additional sandbox mount %s: %w", mount, err)
		}
		if !containsPath(mounts, mount) {
			mounts = append(mounts, mount)
		}
	}
	return mounts, nil
}

func containsPath(paths []string, candidate string) bool {
	for _, path := range paths {
		if filepath.Clean(path) == candidate {
			return true
		}
	}
	return false
}

func pathContains(root string, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func stopSandbox(ctx context.Context, runner Runner, path string, name string) (string, error) {
	output, err := runner.Run(ctx, "", "sbx", "rm", "--force", name)
	if err != nil && strings.Contains(err.Error(), "not found") {
		return output, nil
	}
	if err != nil {
		return output, sbxCommandError(err)
	}
	return output, nil
}

type sandboxListResponse struct {
	Sandboxes []struct {
		Name string `json:"name"`
	} `json:"sandboxes"`
}

func sandboxExists(ctx context.Context, runner Runner, name string) (bool, error) {
	output, err := runner.Run(ctx, "", "sbx", "ls", "--json")
	if err != nil {
		return false, sbxCommandError(err)
	}
	var response sandboxListResponse
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		return false, fmt.Errorf("unexpected sbx ls output: %w", err)
	}
	for _, sandbox := range response.Sandboxes {
		if sandbox.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func sbxCommandError(err error) error {
	if err == nil {
		return nil
	}
	if auth.IsRequired(err.Error()) {
		return fmt.Errorf("sbx is not signed in; run sbx login")
	}
	return err
}

func createTmuxWorkspace(ctx context.Context, runner Runner, cwd string, path string, sessionName string, cfg sessionlayout.Config, piArgsText string, environment map[string]string) error {
	cfg = sessionlayout.WithDefaults(cfg)
	if err := sessionlayout.Validate(cfg); err != nil {
		return err
	}

	createdSession := false
	cleanup := func(err error) error {
		if createdSession {
			_, _ = runner.Run(ctx, cwd, "tmux", "kill-session", "-t", sessionName)
		}
		return err
	}
	var piWindowID string
	var piPaneID string
	for windowIndex, window := range cfg.Windows {
		firstCommand := expandPiArgs(window.Panes[0].Command, piArgsText)
		var output string
		var err error
		if windowIndex == 0 {
			args := []string{"new-session", "-d", "-s", sessionName, "-n", window.Name, "-c", path, "-P", "-F", "#{window_id} #{pane_id}"}
			keys := make([]string, 0, len(environment))
			for key := range environment {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				args = append(args, "-e", key+"="+environment[key])
			}
			args = append(args, firstCommand)
			output, err = runner.Run(ctx, cwd, "tmux", args...)
			if err == nil {
				createdSession = true
			}
		} else {
			output, err = runner.Run(ctx, cwd, "tmux", "new-window", "-t", sessionName+":", "-d", "-n", window.Name, "-c", path, "-P", "-F", "#{window_id} #{pane_id}", firstCommand)
		}
		if err != nil {
			return cleanup(err)
		}
		windowID, firstPaneID, err := parseTmuxIDs(output)
		if err != nil {
			return cleanup(err)
		}
		if strings.Contains(window.Panes[0].Command, sessionlayout.PiArgsPlaceholder) {
			piWindowID = windowID
			piPaneID = firstPaneID
		}

		for _, pane := range window.Panes[1:] {
			command := expandPiArgs(pane.Command, piArgsText)
			output, err := runner.Run(ctx, cwd, "tmux", "split-window", "-d", "-t", firstPaneID, "-c", path, "-P", "-F", "#{window_id} #{pane_id}", command)
			if err != nil {
				return cleanup(err)
			}
			paneWindowID, paneID, err := parseTmuxIDs(output)
			if err != nil {
				return cleanup(err)
			}
			if strings.Contains(pane.Command, sessionlayout.PiArgsPlaceholder) {
				piWindowID = paneWindowID
				piPaneID = paneID
			}
		}
		layout, err := sessionlayout.NativeLayout(window.Layout)
		if err != nil {
			return cleanup(err)
		}
		if layout != "" {
			if _, err := runner.Run(ctx, cwd, "tmux", "select-layout", "-t", windowID, layout); err != nil {
				return cleanup(err)
			}
		}
	}
	if _, err := runner.Run(ctx, cwd, "tmux", "select-window", "-t", piWindowID); err != nil {
		return cleanup(err)
	}
	if _, err := runner.Run(ctx, cwd, "tmux", "select-pane", "-t", piPaneID); err != nil {
		return cleanup(err)
	}
	return nil
}

func parseTmuxIDs(output string) (string, string, error) {
	fields := strings.Fields(output)
	if len(fields) != 2 || !strings.HasPrefix(fields[0], "@") || !strings.HasPrefix(fields[1], "%") {
		return "", "", fmt.Errorf("unexpected tmux pane output %q", output)
	}
	return fields[0], fields[1], nil
}

func expandPiArgs(command string, args string) string {
	return strings.ReplaceAll(command, sessionlayout.PiArgsPlaceholder, args)
}

func taskPiSessionID(sessionName string, taskLinkingKey string) string {
	taskLinkingKey = strings.TrimSpace(taskLinkingKey)
	if taskLinkingKey == "" {
		return sessionName
	}
	sum := sha1.Sum([]byte(taskLinkingKey))
	return "radar-task-" + hex.EncodeToString(sum[:])[:16]
}

func piArgsWithPrompt(sessionID string, name string, model string, thinking string, forkSession string, prompt string) string {
	args := []string{}
	if forkSession != "" {
		args = append(args, "--fork", shellQuote(forkSession))
	}
	if strings.TrimSpace(model) != "" {
		args = append(args, "--model", shellQuote(strings.TrimSpace(model)))
	}
	if strings.TrimSpace(thinking) != "" {
		args = append(args, "--thinking", shellQuote(strings.TrimSpace(thinking)))
	}
	args = append(args, "--session-id", shellQuote(sessionID), "--name", shellQuote(name))
	if strings.TrimSpace(prompt) != "" {
		args = append(args, shellQuote(strings.TrimSpace(prompt)))
	}
	return strings.Join(args, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
