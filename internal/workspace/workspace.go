package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"radar/internal/pi"
)

var invalidWorkspaceNameCharacters = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

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
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = cwd
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

type CreateOptions struct {
	Repo          string
	Name          string
	Branch        string
	Base          string
	Path          string
	SessionName   string
	WorkspaceRoot string
	Model         string
	Thinking      string
	Switch        bool
	ForkPiSession string
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

func Create(ctx context.Context, runner Runner, options CreateOptions) (Workspace, error) {
	for _, dependency := range []string{"git", "tmux", "pi", "nvim"} {
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
	if repoConfig.Sandbox != nil {
		if err := runner.LookPath("docker"); err != nil {
			return Workspace{}, fmt.Errorf("workspace sandbox requires %q: %w", "docker", err)
		}
	}
	name := strings.TrimSpace(options.Name)
	repoName := filepath.Base(repo)
	branch := options.Branch
	if branch == "" {
		branch = BranchName(name)
	} else {
		branch = BranchName(branch)
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
	if _, err := os.Stat(path); err == nil {
		return Workspace{}, fmt.Errorf("workspace already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return Workspace{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Workspace{}, err
	}

	args := []string{"worktree", "add", "-b", branch, path}
	if options.Base != "" {
		args = append(args, options.Base)
	}
	if _, err := runner.Run(ctx, repo, "git", args...); err != nil {
		return Workspace{}, err
	}
	createdSession := false
	createdSandbox := false
	sandboxName := ""
	if repoConfig.Sandbox != nil {
		sandboxName = SandboxName(repoName, name)
	}
	rollback := func() {
		if createdSession {
			_, _ = runner.Run(ctx, repo, "tmux", "kill-session", "-t", sessionName)
		}
		if createdSandbox {
			_, _ = stopSandbox(ctx, runner, path, *repoConfig.Sandbox, sandboxName)
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
	if repoConfig.Sandbox != nil {
		if _, err := startSandbox(ctx, runner, path, *repoConfig.Sandbox, sandboxName); err != nil {
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
		if repoConfig.Sandbox != nil {
			shellCommand := sandboxRunCommand(sandboxName)
			if _, err := runner.Run(ctx, repo, "tmux", "set-option", "-t", sessionName, "default-command", shellCommand); err != nil {
				rollback()
				return Workspace{}, err
			}
			if _, err := runner.Run(ctx, repo, "tmux", "new-window", "-t", sessionName+":", "-n", "shell", "-c", path, shellCommand); err != nil {
				rollback()
				return Workspace{}, err
			}
		}
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

func CreateSession(ctx context.Context, runner Runner, path string, sessionName string, switchClient bool) (Workspace, error) {
	for _, dependency := range []string{"tmux", "pi", "nvim"} {
		if err := runner.LookPath(dependency); err != nil {
			return Workspace{}, fmt.Errorf("workspace session creation requires %q: %w", dependency, err)
		}
	}
	if strings.TrimSpace(path) == "" {
		return Workspace{}, fmt.Errorf("workspace path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return Workspace{}, err
	}
	if sessionName == "" {
		sessionName = SessionName(filepath.Base(filepath.Dir(path)), filepath.Base(path))
	}
	if _, err := runner.Run(ctx, "", "tmux", "has-session", "-t", sessionName); err != nil {
		if _, err := runner.Run(ctx, "", "tmux", "new-session", "-d", "-s", sessionName, "-n", "pi", "-c", path, "pi"); err != nil {
			return Workspace{}, err
		}
		if _, err := runner.Run(ctx, "", "tmux", "new-window", "-t", sessionName+":", "-n", "nvim", "-c", path, "nvim ."); err != nil {
			_, _ = runner.Run(ctx, "", "tmux", "kill-session", "-t", sessionName)
			return Workspace{}, err
		}
		if _, err := runner.Run(ctx, "", "tmux", "select-window", "-t", sessionName+":pi"); err != nil {
			_, _ = runner.Run(ctx, "", "tmux", "kill-session", "-t", sessionName)
			return Workspace{}, err
		}
	}
	if switchClient {
		if _, err := runner.Run(ctx, "", "tmux", "switch-client", "-t", sessionName); err != nil {
			return Workspace{}, err
		}
	}
	return Workspace{Path: path, SessionName: sessionName}, nil
}

func DeleteSession(ctx context.Context, runner Runner, sessionName string) (Workspace, error) {
	if strings.TrimSpace(sessionName) == "" {
		return Workspace{}, fmt.Errorf("tmux session is required")
	}
	if _, err := runner.Run(ctx, "", "tmux", "kill-session", "-t", sessionName); err != nil {
		return Workspace{}, err
	}
	return Workspace{SessionName: sessionName}, nil
}

func Delete(ctx context.Context, runner Runner, path string, sessionName string, force bool) (Workspace, error) {
	if strings.TrimSpace(path) == "" {
		return Workspace{}, fmt.Errorf("workspace path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return Workspace{}, err
	}
	if sessionName == "" {
		sessionName = SessionName(filepath.Base(filepath.Dir(path)), filepath.Base(path))
	}
	status, err := runner.Run(ctx, "", "git", "-C", path, "status", "--porcelain")
	if err != nil {
		return Workspace{}, err
	}
	if status != "" && !force {
		return Workspace{}, fmt.Errorf("workspace has local changes; rerun with --force to delete it")
	}
	if _, err := runner.Run(ctx, "", "tmux", "has-session", "-t", sessionName); err == nil {
		if _, err := runner.Run(ctx, "", "tmux", "kill-session", "-t", sessionName); err != nil {
			return Workspace{}, err
		}
	}
	sandboxName := ""
	if repoConfig, err := loadRepoConfig(path); err != nil {
		return Workspace{}, err
	} else if repoConfig.Sandbox != nil {
		sandboxName = SandboxName(filepath.Base(filepath.Dir(path)), filepath.Base(path))
		if _, err := stopSandbox(ctx, runner, path, *repoConfig.Sandbox, sandboxName); err != nil {
			return Workspace{}, err
		}
	}
	args := []string{"-C", path, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	if _, err := runner.Run(ctx, "", "git", args...); err != nil {
		return Workspace{}, err
	}
	return Workspace{Path: path, SessionName: sessionName, SandboxName: sandboxName}, nil
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
	return WorktreeName("radar-" + repoName + "-" + workspaceName)
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

func startSandbox(ctx context.Context, runner Runner, path string, _ SandboxConfig, name string) (string, error) {
	return runner.Run(ctx, path, "docker", "sandbox", "create", "--name", name, "shell", path)
}

func stopSandbox(ctx context.Context, runner Runner, path string, _ SandboxConfig, name string) (string, error) {
	return runner.Run(ctx, path, "docker", "sandbox", "rm", name)
}

func sandboxRunCommand(name string) string {
	return "docker sandbox run " + shellQuote(name)
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
