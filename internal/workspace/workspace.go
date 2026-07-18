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
	"strings"
	"syscall"

	"radar/internal/pi"
	"radar/internal/sbxauth"
)

var invalidWorkspaceNameCharacters = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

var workspaceGOOS = runtime.GOOS

const defaultSandboxTemplate = "christianmoesl/radar-sandbox:latest"
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
	Repo            string
	Name            string
	Branch          string
	Base            string
	Path            string
	SessionName     string
	WorkspaceRoot   string
	Model           string
	Thinking        string
	Sandbox         bool
	SandboxTemplate string
	Switch          bool
	ForkPiSession   string
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
	Path            string
	SessionName     string
	Model           string
	Thinking        string
	Sandbox         bool
	SandboxTemplate string
	SandboxName     string
	Switch          bool
}

func Create(ctx context.Context, runner Runner, options CreateOptions) (Workspace, error) {
	for _, dependency := range []string{"git", "tmux", "nvim", "pi"} {
		if err := runner.LookPath(dependency); err != nil {
			return Workspace{}, fmt.Errorf("workspace creation requires %q: %w", dependency, err)
		}
	}
	if strings.TrimSpace(options.Name) == "" {
		return Workspace{}, fmt.Errorf("workspace name is required")
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
	sandboxConfig, sandboxEnabled := workspaceSandboxConfig(repoConfig, options.Sandbox)
	if sandboxEnabled {
		if workspaceGOOS != "darwin" {
			return Workspace{}, fmt.Errorf("workspace sandbox is only supported on macOS")
		}
		if err := runner.LookPath("sbx"); err != nil {
			return Workspace{}, fmt.Errorf("workspace sandbox requires %q: %w", "sbx", err)
		}
	}
	name := strings.TrimSpace(options.Name)
	repoName := filepath.Base(repo)
	branch := strings.TrimSpace(options.Branch)
	if branch == "" {
		branch = BranchName(name)
	}
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
	if sandboxEnabled {
		sandboxName = SandboxName(repoName, name)
	}
	if existingPath, ok, err := worktreePathForBranch(ctx, runner, repo, branch); err != nil {
		return Workspace{}, err
	} else if ok {
		return openExistingWorkspace(ctx, runner, Workspace{Name: name, Branch: branch, Base: options.Base, Repo: repo, Path: existingPath, SessionName: sessionName, SandboxName: sandboxName}, options.Switch)
	}
	if _, err := os.Stat(path); err == nil {
		if isGitWorktree(ctx, runner, path) {
			return openExistingWorkspace(ctx, runner, Workspace{Name: name, Branch: branch, Base: options.Base, Repo: repo, Path: path, SessionName: sessionName, SandboxName: sandboxName}, options.Switch)
		}
		return Workspace{}, fmt.Errorf("workspace already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return Workspace{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Workspace{}, err
	}

	args := []string{"worktree", "add"}
	if branchExists(ctx, runner, repo, branch) {
		args = append(args, path, branch)
	} else {
		args = append(args, "-b", branch, path)
		if options.Base != "" {
			args = append(args, options.Base)
		}
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
			_, _ = stopSandbox(ctx, runner, path, sandboxConfig, sandboxName)
		}
		_, _ = runner.Run(ctx, repo, "git", "worktree", "remove", "--force", path)
	}

	if err := copyConfiguredFiles(repo, path, repoConfig.CopyFiles); err != nil {
		rollback()
		return Workspace{}, err
	}
	for _, command := range repoConfig.Setup {
		if _, err := runner.Run(ctx, path, "sh", "-lc", command); err != nil {
			rollback()
			return Workspace{}, err
		}
	}
	sandboxTemplate := strings.TrimSpace(options.SandboxTemplate)
	if sandboxTemplate == "" {
		sandboxTemplate = defaultSandboxTemplate
	}
	if sandboxEnabled {
		if _, err := startSandbox(ctx, runner, path, sandboxConfig, sandboxName, sandboxTemplate); err != nil {
			rollback()
			return Workspace{}, err
		}
		createdSandbox = true
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
		piCommandText := piCommand(sessionName, model, thinking, options.ForkPiSession)
		if _, err := runner.Run(ctx, repo, "tmux", "new-session", "-d", "-s", sessionName, "-n", "pi", "-c", path, piCommandText); err != nil {
			rollback()
			return Workspace{}, err
		}
		createdSession = true
		if _, err := runner.Run(ctx, repo, "tmux", "new-window", "-t", sessionName+":", "-n", "nvim", "-c", path, "nvim ."); err != nil {
			rollback()
			return Workspace{}, err
		}
		if _, err := runner.Run(ctx, repo, "tmux", "select-window", "-t", sessionName+":pi"); err != nil {
			rollback()
			return Workspace{}, err
		}
	}
	if options.Switch {
		if _, err := runner.Run(ctx, repo, "tmux", "switch-client", "-t", sessionName); err != nil {
			return Workspace{}, err
		}
	}

	return Workspace{Name: name, Branch: branch, Base: options.Base, Repo: repo, Path: path, SessionName: sessionName, SandboxName: sandboxName}, nil
}

func workspaceSandboxConfig(repoConfig RepoConfig, enabledByUserConfig bool) (SandboxConfig, bool) {
	if repoConfig.Sandbox != nil {
		return *repoConfig.Sandbox, true
	}
	if enabledByUserConfig {
		return SandboxConfig{}, true
	}
	return SandboxConfig{}, false
}

func openExistingWorkspace(ctx context.Context, runner Runner, workspace Workspace, switchClient bool) (Workspace, error) {
	created, err := CreateSession(ctx, runner, workspace.Path, workspace.SessionName, switchClient)
	if err != nil {
		return Workspace{}, err
	}
	workspace.Path = created.Path
	workspace.SessionName = created.SessionName
	return workspace, nil
}

func branchExists(ctx context.Context, runner Runner, repo string, branch string) bool {
	_, err := runner.Run(ctx, repo, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func isGitWorktree(ctx context.Context, runner Runner, path string) bool {
	_, err := runner.Run(ctx, path, "git", "rev-parse", "--show-toplevel")
	return err == nil
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
	for _, dependency := range []string{"tmux", "nvim", "pi"} {
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
	sandboxConfig, sandboxEnabled := workspaceSandboxConfig(repoConfig, options.Sandbox)
	sandboxName := strings.TrimSpace(options.SandboxName)
	if sandboxName != "" {
		sandboxEnabled = true
	}
	if sandboxEnabled {
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
	if sandboxEnabled && sandboxName == "" {
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
		piCommandText := piCommand(sessionName, model, thinking, "")
		createdSandbox := false
		if sandboxEnabled {
			if exists, err := sandboxExists(ctx, runner, sandboxName); err != nil {
				return Workspace{}, err
			} else if !exists {
				sandboxTemplate := strings.TrimSpace(options.SandboxTemplate)
				if sandboxTemplate == "" {
					sandboxTemplate = defaultSandboxTemplate
				}
				if _, err := startSandbox(ctx, runner, path, sandboxConfig, sandboxName, sandboxTemplate); err != nil {
					return Workspace{}, err
				}
				createdSandbox = true
			}
		}
		if _, err := runner.Run(ctx, "", "tmux", "new-session", "-d", "-s", sessionName, "-n", "pi", "-c", path, piCommandText); err != nil {
			if createdSandbox {
				_, _ = stopSandbox(ctx, runner, path, sandboxConfig, sandboxName)
			}
			return Workspace{}, err
		}
		if _, err := runner.Run(ctx, "", "tmux", "new-window", "-t", sessionName+":", "-n", "nvim", "-c", path, "nvim ."); err != nil {
			_, _ = runner.Run(ctx, "", "tmux", "kill-session", "-t", sessionName)
			if createdSandbox {
				_, _ = stopSandbox(ctx, runner, path, sandboxConfig, sandboxName)
			}
			return Workspace{}, err
		}
		if _, err := runner.Run(ctx, "", "tmux", "select-window", "-t", sessionName+":pi"); err != nil {
			_, _ = runner.Run(ctx, "", "tmux", "kill-session", "-t", sessionName)
			if createdSandbox {
				_, _ = stopSandbox(ctx, runner, path, sandboxConfig, sandboxName)
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

func startSandbox(ctx context.Context, runner Runner, path string, _ SandboxConfig, name string, template string) (string, error) {
	mounts, err := sandboxMounts(ctx, runner, path)
	if err != nil {
		return "", err
	}
	args := []string{"create", "--name", name, "--template", template, "shell"}
	args = append(args, mounts...)
	output, err := runner.Run(ctx, path, "sbx", args...)
	if err != nil {
		return output, sbxCommandError(err)
	}
	return output, nil
}

func sandboxMounts(ctx context.Context, runner Runner, path string) ([]string, error) {
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
	return mounts, nil
}

func pathContains(root string, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func stopSandbox(ctx context.Context, runner Runner, path string, _ SandboxConfig, name string) (string, error) {
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

func piCommand(sessionName string, model string, thinking string, forkSession string) string {
	args := []string{"pi"}
	if forkSession != "" {
		args = append(args, "--fork", shellQuote(forkSession))
	}
	if strings.TrimSpace(model) != "" {
		args = append(args, "--model", shellQuote(strings.TrimSpace(model)))
	}
	if strings.TrimSpace(thinking) != "" {
		args = append(args, "--thinking", shellQuote(strings.TrimSpace(thinking)))
	}
	args = append(args, "--session-id", shellQuote(sessionName), "--name", shellQuote(sessionName))
	return strings.Join(args, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
