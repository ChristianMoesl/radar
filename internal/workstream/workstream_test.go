package workstream

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type call struct {
	cwd  string
	name string
	args []string
}

type fakeRunner struct {
	repo       string
	hasSession bool
	calls      []call
}

func (f *fakeRunner) LookPath(string) error { return nil }

func (f *fakeRunner) Run(_ context.Context, cwd string, name string, args ...string) (string, error) {
	f.calls = append(f.calls, call{cwd: cwd, name: name, args: args})
	if name == "git" && strings.Join(args, " ") == "rev-parse --show-toplevel" {
		return f.repo, nil
	}
	if name == "tmux" && len(args) > 0 && args[0] == "has-session" {
		if !f.hasSession {
			return "", errors.New("missing")
		}
		return "", nil
	}
	if name == "git" && len(args) > 4 && args[0] == "worktree" && args[1] == "add" {
		return "", os.MkdirAll(args[4], 0o755)
	}
	return "", nil
}

func TestCreateBuildsWorktreeAndTmuxSession(t *testing.T) {
	repo := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{repo: repo}

	workstream, err := Create(context.Background(), runner, CreateOptions{
		Repo:          repo,
		Name:          "small fix",
		Base:          "origin/main",
		WorkspaceRoot: root,
		Switch:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if workstream.Branch != "small fix" || workstream.SessionName != filepath.Base(repo)+"-small-fix" {
		t.Fatalf("unexpected workstream: %#v", workstream)
	}
	data, err := os.ReadFile(filepath.Join(workstream.Path, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "SECRET=local\n" {
		t.Fatalf("copied .env = %q", data)
	}
	assertCalled(t, runner.calls, "git", "worktree add -b small fix "+workstream.Path+" origin/main")
	assertCalled(t, runner.calls, "tmux", "new-session -d -s "+workstream.SessionName)
	assertCalled(t, runner.calls, "tmux", "new-window -t "+workstream.SessionName+":")
	assertCalled(t, runner.calls, "tmux", "switch-client -t "+workstream.SessionName)
}

func TestDeleteKillsSessionAndRemovesWorktree(t *testing.T) {
	runner := &fakeRunner{hasSession: true}
	path := filepath.Join(t.TempDir(), "repo", "small-fix")
	if _, err := Delete(context.Background(), runner, path, "repo-small-fix", false); err != nil {
		t.Fatal(err)
	}
	assertCalled(t, runner.calls, "tmux", "kill-session -t repo-small-fix")
	assertCalled(t, runner.calls, "git", "-C "+path+" worktree remove "+path)
}

func TestDeleteRefusesDirtyWorktreeBeforeKillingSession(t *testing.T) {
	runner := &dirtyRunner{fakeRunner: fakeRunner{hasSession: true}}
	path := filepath.Join(t.TempDir(), "repo", "small-fix")
	if _, err := Delete(context.Background(), runner, path, "repo-small-fix", false); err == nil {
		t.Fatal("Delete() error = nil, want dirty worktree error")
	}
	for _, call := range runner.calls {
		if call.name == "tmux" && len(call.args) > 0 && call.args[0] == "kill-session" {
			t.Fatalf("Delete() killed session before refusing dirty worktree: %#v", runner.calls)
		}
	}
}

func TestSessionNameSanitizesNames(t *testing.T) {
	if got, want := SessionName("my.repo", "small fix"), "my-repo-small-fix"; got != want {
		t.Fatalf("SessionName() = %q, want %q", got, want)
	}
}

func assertCalled(t *testing.T, calls []call, name string, argsPrefix string) {
	t.Helper()
	for _, call := range calls {
		if call.name == name && strings.HasPrefix(strings.Join(call.args, " "), argsPrefix) {
			return
		}
	}
	t.Fatalf("%s %s was not called; calls: %#v", name, argsPrefix, calls)
}

type dirtyRunner struct {
	fakeRunner
}

func (r *dirtyRunner) Run(ctx context.Context, cwd string, name string, args ...string) (string, error) {
	if name == "git" && len(args) > 3 && args[len(args)-2] == "status" && args[len(args)-1] == "--porcelain" {
		r.calls = append(r.calls, call{cwd: cwd, name: name, args: args})
		return "?? .env", nil
	}
	return r.fakeRunner.Run(ctx, cwd, name, args...)
}
