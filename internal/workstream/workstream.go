package workstream

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var invalidSessionCharacters = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

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
	Switch        bool
}

type Workstream struct {
	Name        string `json:"name,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Base        string `json:"base,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Path        string `json:"path"`
	SessionName string `json:"session_name"`
}

func Create(ctx context.Context, runner Runner, options CreateOptions) (Workstream, error) {
	for _, dependency := range []string{"git", "tmux", "pi", "nvim"} {
		if err := runner.LookPath(dependency); err != nil {
			return Workstream{}, fmt.Errorf("workstream creation requires %q: %w", dependency, err)
		}
	}
	if strings.TrimSpace(options.Name) == "" {
		return Workstream{}, fmt.Errorf("workstream name is required")
	}

	repo, err := runner.Run(ctx, options.Repo, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return Workstream{}, err
	}
	name := strings.TrimSpace(options.Name)
	repoName := filepath.Base(repo)
	branch := options.Branch
	if branch == "" {
		branch = name
	}
	root := options.WorkspaceRoot
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Workstream{}, err
		}
		root = filepath.Join(home, "workstreams")
	}
	path := options.Path
	if path == "" {
		path = filepath.Join(root, repoName, name)
	}
	sessionName := options.SessionName
	if sessionName == "" {
		sessionName = SessionName(repoName, name)
	}
	if _, err := os.Stat(path); err == nil {
		return Workstream{}, fmt.Errorf("workstream already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return Workstream{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Workstream{}, err
	}

	args := []string{"worktree", "add", "-b", branch, path}
	if options.Base != "" {
		args = append(args, options.Base)
	}
	if _, err := runner.Run(ctx, repo, "git", args...); err != nil {
		return Workstream{}, err
	}
	createdSession := false
	rollback := func() {
		if createdSession {
			_, _ = runner.Run(ctx, repo, "tmux", "kill-session", "-t", sessionName)
		}
		_, _ = runner.Run(ctx, repo, "git", "worktree", "remove", "--force", path)
	}

	if err := copySetupFiles(repo, path); err != nil {
		rollback()
		return Workstream{}, err
	}
	if _, err := runner.Run(ctx, repo, "tmux", "has-session", "-t", sessionName); err != nil {
		piCommand := fmt.Sprintf("pi --session-id %s --name %s", shellQuote(sessionName), shellQuote(sessionName))
		if _, err := runner.Run(ctx, repo, "tmux", "new-session", "-d", "-s", sessionName, "-n", "pi", "-c", path, piCommand); err != nil {
			rollback()
			return Workstream{}, err
		}
		createdSession = true
		if _, err := runner.Run(ctx, repo, "tmux", "new-window", "-t", sessionName+":", "-n", "nvim", "-c", path, "nvim ."); err != nil {
			rollback()
			return Workstream{}, err
		}
		if _, err := runner.Run(ctx, repo, "tmux", "select-window", "-t", sessionName+":pi"); err != nil {
			rollback()
			return Workstream{}, err
		}
	}
	if options.Switch {
		if _, err := runner.Run(ctx, repo, "tmux", "switch-client", "-t", sessionName); err != nil {
			return Workstream{}, err
		}
	}

	return Workstream{Name: name, Branch: branch, Base: options.Base, Repo: repo, Path: path, SessionName: sessionName}, nil
}

func Delete(ctx context.Context, runner Runner, path string, sessionName string, force bool) (Workstream, error) {
	if strings.TrimSpace(path) == "" {
		return Workstream{}, fmt.Errorf("workstream path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return Workstream{}, err
	}
	if sessionName == "" {
		sessionName = SessionName(filepath.Base(filepath.Dir(path)), filepath.Base(path))
	}
	status, err := runner.Run(ctx, "", "git", "-C", path, "status", "--porcelain")
	if err != nil {
		return Workstream{}, err
	}
	if status != "" && !force {
		return Workstream{}, fmt.Errorf("workstream has local changes; rerun with --force to delete it")
	}
	if _, err := runner.Run(ctx, "", "tmux", "has-session", "-t", sessionName); err == nil {
		if _, err := runner.Run(ctx, "", "tmux", "kill-session", "-t", sessionName); err != nil {
			return Workstream{}, err
		}
	}
	args := []string{"-C", path, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	if _, err := runner.Run(ctx, "", "git", args...); err != nil {
		return Workstream{}, err
	}
	return Workstream{Path: path, SessionName: sessionName}, nil
}

func SessionName(repoName string, workstreamName string) string {
	name := invalidSessionCharacters.ReplaceAllString(repoName+"-"+workstreamName, "-")
	name = strings.Trim(name, "-_")
	if name == "" {
		return "workstream"
	}
	return name
}

func copySetupFiles(source string, target string) error {
	for _, name := range []string{".env", ".env.local"} {
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

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
