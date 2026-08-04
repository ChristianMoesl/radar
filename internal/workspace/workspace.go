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
	"radar/internal/pi"
	"radar/internal/sbxauth"
	"radar/internal/tmuxlayout"
)

var invalidWorkspaceNameCharacters = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

var workspaceGOOS = runtime.GOOS

const defaultSandboxKitName = "shell"
const maxSandboxNameLength = 63
const sandboxNameHashLength = 8

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
	Tmux                    tmuxlayout.Config
	Switch                  bool
	ForkPiSession           string
}

type Workspace struct {
	Name        string `json:"name,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Base        string `json:"base,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Path        string `json:"path"`
	SessionName string `json:"session_name"`
	SandboxName string `json:"sandbox_name,omitempty"`
}

type CreateSessionOptions struct {
	Path                    string
	SessionName             string
	PiSessionID             string
	InitialPrompt           string
	Environment             map[string]string
	Model                   string
	Thinking                string
	Sandbox                 bool
	SandboxKitName          string
	SandboxKitPath          string
	AdditionalSandboxMounts []string
	SandboxName             string
	Tmux                    tmuxlayout.Config
	Switch                  bool
}

func Create(ctx context.Context, runner Runner, options CreateOptions) (Workspace, error) {
	for _, dependency := range []string{"git", "tmux"} {
		if err := runner.LookPath(dependency); err != nil {
			return Workspace{}, fmt.Errorf("workspace creation requires %q: %w", dependency, err)
		}
	}
	if options.BranchMode != integration.WorkspaceBranchExisting && options.BranchMode != integration.WorkspaceBranchNew {
		return Workspace{}, fmt.Errorf("workspace branch mode must be existing or new")
	}

	repo, err := runner.Run(ctx, options.Repo, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return Workspace{}, err
	}
	repoConfig, err := loadRepoConfig(repo)
	if err != nil {
		return Workspace{}, err
	}
	if err := pi.ValidateThinking(options.Thinking); err != nil {
		return Workspace{}, err
	}
	if err := tmuxlayout.Validate(options.Tmux); err != nil {
		return Workspace{}, err
	}
	sandbox := workspaceSandboxConfig(repoConfig, options.Sandbox, options.SandboxKitName, options.SandboxKitPath, options.AdditionalSandboxMounts)
	if sandbox.Enabled {
		if workspaceGOOS != "darwin" {
			return Workspace{}, fmt.Errorf("workspace sandbox is only supported on macOS")
		}
		if err := runner.LookPath("sbx"); err != nil {
			return Workspace{}, fmt.Errorf("workspace sandbox requires %q: %w", "sbx", err)
		}
	}
	name := strings.TrimSpace(options.Name)
	branch := strings.TrimSpace(options.Branch)
	switch options.BranchMode {
	case integration.WorkspaceBranchExisting:
		branch = normalizeExistingBranch(branch)
		if branch == "" {
			return Workspace{}, fmt.Errorf("existing branch is required")
		}
		if name == "" {
			name = branch
		}
	case integration.WorkspaceBranchNew:
		if name == "" {
			return Workspace{}, fmt.Errorf("new branch name is required")
		}
		if branch == "" {
			branch = BranchName(name)
		}
		if strings.TrimSpace(options.Base) == "" {
			return Workspace{}, fmt.Errorf("new branch base is required")
		}
	}
	repoName := filepath.Base(repo)
	root := options.WorkspaceRoot
	if root == "" {
		root, err = DefaultRoot()
		if err != nil {
			return Workspace{}, err
		}
	}
	path := options.Path
	if path == "" {
		path = filepath.Join(root, repoName, WorktreeName(name))
	}
	sessionName := options.SessionName
	if sessionName == "" {
		sessionName = SessionName(repoName, name)
	}
	sandboxName := ""
	if sandbox.Enabled {
		sandboxName = SandboxName(repoName, name)
	}
	if existingPath, ok, err := worktreePathForBranch(ctx, runner, repo, branch); err != nil {
		return Workspace{}, err
	} else if ok {
		if options.BranchMode == integration.WorkspaceBranchNew {
			return Workspace{}, fmt.Errorf("branch %q already exists; choose the existing branch instead", branch)
		}
		if !isWorkspacePath(existingPath, root) {
			return Workspace{}, fmt.Errorf("branch %q is checked out at %s; detach that checkout before creating a Radar workspace", branch, existingPath)
		}
		return openExistingWorkspace(ctx, runner, Workspace{Name: name, Branch: branch, Base: options.Base, Repo: repo, Path: existingPath, SessionName: sessionName, SandboxName: sandboxName}, options)
	}
	if _, err := os.Stat(path); err == nil {
		return Workspace{}, fmt.Errorf("workspace already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return Workspace{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Workspace{}, err
	}

	args := []string{"worktree", "add"}
	switch options.BranchMode {
	case integration.WorkspaceBranchExisting:
		switch {
		case refExists(ctx, runner, repo, "refs/heads/"+branch):
			args = append(args, path, branch)
		case refExists(ctx, runner, repo, "refs/remotes/origin/"+branch):
			args = append(args, "--track", "-b", branch, path, "origin/"+branch)
		default:
			return Workspace{}, fmt.Errorf("branch %q does not exist locally or on origin", branch)
		}
	case integration.WorkspaceBranchNew:
		if refExists(ctx, runner, repo, "refs/heads/"+branch) || refExists(ctx, runner, repo, "refs/remotes/origin/"+branch) {
			return Workspace{}, fmt.Errorf("branch %q already exists; choose the existing branch instead", branch)
		}
		args = append(args, "-b", branch, path, options.Base)
	}
	if _, err := runner.Run(ctx, repo, "git", args...); err != nil {
		return Workspace{}, err
	}
	createdSession := false
	createdSandbox := false
	rollback := func() {
		if createdSession {
			_, _ = runner.Run(ctx, repo, "tmux", "kill-session", "-t", sessionName)
		}
		if createdSandbox {
			_, _ = stopSandbox(ctx, runner, path, sandboxName)
		}
		_, _ = runner.Run(ctx, repo, "git", "worktree", "remove", "--force", path)
	}

	if err := copyConfiguredFiles(repo, path, repoConfig.CopyFiles); err != nil {
		rollback()
		return Workspace{}, err
	}
	if sandbox.Enabled {
		if _, err := startSandbox(ctx, runner, path, sandboxName, sandbox.Kit, sandbox.AdditionalMounts); err != nil {
			rollback()
			return Workspace{}, err
		}
		createdSandbox = true
	}
	if err := runSetupCommands(ctx, runner, path, sandboxName, repoConfig.Setup); err != nil {
		rollback()
		return Workspace{}, err
	}
	if _, err := runner.Run(ctx, repo, "tmux", "has-session", "-t", sessionName); err != nil {
		model := options.Model
		if strings.TrimSpace(repoConfig.Model) != "" {
			model = repoConfig.Model
		}
		thinking := options.Thinking
		if strings.TrimSpace(repoConfig.Thinking) != "" {
			thinking = repoConfig.Thinking
		}
		piArgsText := piArgs(sessionName, model, thinking, options.ForkPiSession)
		if err := createTmuxWorkspace(ctx, runner, repo, path, sessionName, options.Tmux, piArgsText, nil); err != nil {
			rollback()
			return Workspace{}, err
		}
		createdSession = true
	}
	if options.Switch {
		if _, err := runner.Run(ctx, repo, "tmux", "switch-client", "-t", sessionName); err != nil {
			return Workspace{}, err
		}
	}

	return Workspace{Name: name, Branch: branch, Base: options.Base, Repo: repo, Path: path, SessionName: sessionName, SandboxName: sandboxName}, nil
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
	parts := strings.Split(relative, string(os.PathSeparator))
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
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
	if err := tmuxlayout.Validate(options.Tmux); err != nil {
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
		piSessionID := strings.TrimSpace(options.PiSessionID)
		if piSessionID == "" {
			piSessionID = sessionName
		}
		piArgsText := piArgsWithPrompt(piSessionID, sessionName, model, thinking, "", options.InitialPrompt)
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
		if err := createTmuxWorkspace(ctx, runner, "", path, sessionName, options.Tmux, piArgsText, options.Environment); err != nil {
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

func runSetupCommands(ctx context.Context, runner Runner, path string, sandboxName string, commands []string) error {
	for _, command := range commands {
		if sandboxName != "" {
			if _, err := runner.Run(ctx, path, "sbx", "exec", "--workdir", path, sandboxName, "sh", "-lc", command); err != nil {
				return sbxCommandError(err)
			}
			continue
		}
		if _, err := runner.Run(ctx, path, "sh", "-lc", command); err != nil {
			return err
		}
	}
	return nil
}

func startSandbox(ctx context.Context, runner Runner, path string, name string, kit SandboxKitConfig, additionalMounts []string) (string, error) {
	mounts, err := sandboxMounts(ctx, runner, path, additionalMounts)
	if err != nil {
		return "", err
	}
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
	if sbxauth.IsRequired(err.Error()) {
		return fmt.Errorf("sbx is not signed in; run sbx login")
	}
	return err
}

func createTmuxWorkspace(ctx context.Context, runner Runner, cwd string, path string, sessionName string, cfg tmuxlayout.Config, piArgsText string, environment map[string]string) error {
	cfg = tmuxlayout.WithDefaults(cfg)
	if err := tmuxlayout.Validate(cfg); err != nil {
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
		if strings.Contains(window.Panes[0].Command, tmuxlayout.PiArgsPlaceholder) {
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
			if strings.Contains(pane.Command, tmuxlayout.PiArgsPlaceholder) {
				piWindowID = paneWindowID
				piPaneID = paneID
			}
		}
		layout, err := tmuxlayout.NativeLayout(window.Layout)
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
	return strings.ReplaceAll(command, tmuxlayout.PiArgsPlaceholder, args)
}

func piArgs(sessionName string, model string, thinking string, forkSession string) string {
	return piArgsWithPrompt(sessionName, sessionName, model, thinking, forkSession, "")
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
