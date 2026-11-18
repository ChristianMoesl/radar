package sessionlease

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

type nativeRunner struct {
	binary string
	socket string
}

func (r nativeRunner) Run(ctx context.Context, cwd, _ string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.binary, append([]string{"-S", r.socket, "-f", "/dev/null"}, args...)...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestNative(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("native tmux executable is not installed")
	}
	root := t.TempDir()
	t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
	runner := nativeRunner{binary: binary, socket: filepath.Join(root, "tmux.sock")}
	ctx := context.Background()
	defer func() {
		_, _ = runner.Run(ctx, "", "tmux", "kill-server")
		if _, err := runner.Run(ctx, "", "tmux", "list-sessions"); err == nil {
			t.Error("private fixture server did not stop")
		}
	}()
	if output, err := runner.Run(ctx, root, "tmux", "new-session", "-d", "-s", "fixture-alpha", "sleep 120"); err != nil {
		t.Fatal(output, err)
	}
	if output, err := runner.Run(ctx, root, "tmux", "new-session", "-d", "-s", "keeper", "sleep 120"); err != nil {
		t.Fatal(output, err)
	}
	first := New(false)
	defer first.Close()
	id, err := first.Admit(ctx, runner, Request{Selector: "=fixture-alpha"})
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"fixture-alpha", "=fixture-alpha", "fixture-a", "fixture-al*", id.ID} {
		other := New(true)
		_, err := other.Admit(ctx, runner, Request{Selector: alias})
		other.Close()
		if err == nil {
			t.Fatal("native alias bypassed shared admission", alias)
		}
	}
	if output, err := runner.Run(ctx, "", "tmux", "rename-session", "-t", id.ID, "fixture-beta"); err != nil {
		t.Fatal(output, err)
	}
	other := New(true)
	if _, err := other.Admit(ctx, runner, Request{Selector: "=fixture-beta"}); err == nil {
		other.Close()
		t.Fatal("native rename bypassed incarnation protection")
	}
	first.Close()
	admitted := New(true)
	defer admitted.Close()
	renamed, err := admitted.Admit(ctx, runner, Request{Selector: "=fixture-beta", Expected: id.Key()})
	if err != nil || renamed.Key() != id.Key() {
		t.Fatal("rename changed stable incarnation", renamed, err)
	}
	missing := New(false)
	defer missing.Close()
	if _, err := missing.Admit(ctx, runner, Request{Selector: "=missing-fixture"}); err == nil {
		t.Fatal("native exit0 without session fields was accepted")
	}
}
