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

func (r *fakeRunner) Run(_ context.Context, cwd string, name string, args ...string) (string, error) {
	r.calls = append(r.calls, fakeCall{cwd: cwd, name: name, args: append([]string(nil), args...)})
	if name == "tmux" && len(args) >= 1 && args[0] == "has-session" {
		return "", r.hasSessionErr
	}
	return "", nil
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
