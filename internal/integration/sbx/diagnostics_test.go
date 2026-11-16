package sbx

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"radar/internal/protocol"
)

func TestDiagnosticRuntimeUsesNativeVerifiableListingWithoutWritesOrPayloads(t *testing.T) {
	for _, tc := range []struct{ name, listing, presence, id, state string }{
		{"present", `[{"name":"registered","id":"lifetime","status":"running","environment":{"token":"private-secret"},"mounts":["private-mount"]}]`, "present", "lifetime", "running"},
		{"stopped", `[{"name":"registered","id":"lifetime","status":"stopped"}]`, "present", "lifetime", "stopped"},
		{"unknown state", `[{"name":"registered","id":"lifetime","status":"private-status"}]`, "present", "lifetime", "unknown"},
		{"absent", "[]", "absent", "", "unknown"},
		{"foreign", `[{"name":"foreign","id":"foreign-id"}]`, "absent", "", "unknown"},
		{"unidentified foreign", `[{"name":"foreign"}]`, "unknown", "", "unknown"},
		{"duplicate", `[{"name":"registered","id":"one"},{"name":"registered","id":"two"}]`, "unknown", "", "unknown"},
		{"null", "null", "unknown", "", "unknown"},
		{"malformed", "private-error", "unknown", "", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &diagnosticFixtureRunner{output: tc.listing}
			view := diagnosticRuntime(context.Background(), "registered", runner)
			if view.Presence != tc.presence || view.ID != tc.id || view.State != tc.state {
				t.Fatalf("view=%+v", view)
			}
			if !reflect.DeepEqual(runner.calls, [][]string{{"sbx", "ls", "--json"}}) {
				t.Fatalf("unexpected native operations: %v", runner.calls)
			}
			if view.Presence == "unknown" && view.Problem != "probe_failed" {
				t.Fatal("unavailable evidence reported as absence")
			}
		})
	}
	runner := &diagnosticFixtureRunner{err: errors.New("private-provider-secret")}
	view := diagnosticRuntime(context.Background(), "registered", runner)
	if view != (protocol.DiagnosticRuntimeObservation{Presence: "unknown", State: "unknown", Problem: "probe_failed"}) {
		t.Fatalf("raw error escaped: %+v", view)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner.calls = nil
	view = diagnosticRuntime(ctx, "registered", runner)
	if view.Problem != "cancelled_or_timed_out" || len(runner.calls) != 0 {
		t.Fatal("cancelled probe contacted runtime")
	}
}

type diagnosticFixtureRunner struct {
	output string
	err    error
	calls  [][]string
}

func (*diagnosticFixtureRunner) LookPath(name string) error {
	if name != "sbx" {
		return errors.New("not installed")
	}
	return nil
}
func (r *diagnosticFixtureRunner) Run(_ context.Context, cwd, name string, args ...string) (string, error) {
	if cwd != "" {
		return "", errors.New("unexpected cwd")
	}
	r.calls = append(r.calls, append([]string{name}, args...))
	return r.output, r.err
}
