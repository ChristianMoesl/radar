package sbx

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type fakeRunner struct {
	missing       map[string]bool
	hasSessionErr error
	calls         []fakeCall
}

type fakeCall struct {
	cwd  string
	name string
	args []string
}

func (r *fakeRunner) LookPath(name string) error {
	if r.missing[name] {
		return fmt.Errorf("missing %s", name)
	}
	return nil
}

func (r *fakeRunner) Run(ctx context.Context, cwd string, name string, args ...string) (string, error) {
	r.calls = append(r.calls, fakeCall{cwd: cwd, name: name, args: append([]string(nil), args...)})
	if name == "tmux" && len(args) >= 1 && args[0] == "has-session" {
		return "", r.hasSessionErr
	}
	return "", nil
}

func TestOpenShellCreatesSessionWhenMissing(t *testing.T) {
	runner := &fakeRunner{hasSessionErr: fmt.Errorf("no session")}
	ref := sandbox{
		Name:       "radar-repo-DPSCAP-600-shell",
		Workspaces: []string{"/work/repo/DPSCAP-600-shell"},
	}.SourceRef()

	result, err := OpenShell(context.Background(), runner, ref, OpenShellOptions{SwitchClient: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.CreatedSession || result.SessionName != "repo-DPSCAP-600-shell" {
		t.Fatalf("result = %+v", result)
	}
	assertCallContains(t, runner.calls, "tmux", "new-session -d -s repo-DPSCAP-600-shell -n sbx -c /work/repo/DPSCAP-600-shell sbx run --name 'radar-repo-DPSCAP-600-shell'")
	assertCallContains(t, runner.calls, "tmux", "switch-client -t repo-DPSCAP-600-shell")
}

func TestOpenShellOpensWindowInExistingSession(t *testing.T) {
	runner := &fakeRunner{}
	ref := sandbox{Name: "radar-repo-shell", Workspaces: []string{"/work/repo/shell"}}.SourceRef()

	result, err := OpenShell(context.Background(), runner, ref, OpenShellOptions{SessionTarget: "$3"})
	if err != nil {
		t.Fatal(err)
	}
	if result.CreatedSession || result.SessionName != "$3" {
		t.Fatalf("result = %+v", result)
	}
	assertCallContains(t, runner.calls, "tmux", "has-session -t $3")
	assertCallContains(t, runner.calls, "tmux", "new-window -t $3: -n sbx -c /work/repo/shell sbx run --name 'radar-repo-shell'")
}

func assertCallContains(t *testing.T, calls []fakeCall, name string, text string) {
	t.Helper()
	for _, call := range calls {
		if call.name == name && strings.Contains(strings.Join(call.args, " "), text) {
			return
		}
	}
	t.Fatalf("missing %s call containing %q in %+v", name, text, calls)
}
