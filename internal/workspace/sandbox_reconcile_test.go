package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"radar/internal/workspacegroup"
)

func TestNormalizeMountSetRemovesRedundantChildrenWithSameMode(t *testing.T) {
	mounts := normalizeMountSet([]string{"/work", "/work/member", "/work/read-only:ro"})
	if strings.Join(mounts, "\n") != "/work\n/work/read-only:ro" {
		t.Fatalf("mounts = %+v", mounts)
	}
}

func TestReconcileSandboxRetriesTransientCreateFailuresWithCleanup(t *testing.T) {
	mount := t.TempDir()
	runner := &sandboxRetryRunner{
		name:           "retry-sandbox",
		exists:         true,
		mounts:         []string{"/old-mount"},
		createFailures: 2,
		lingerChecks:   2,
	}
	group := workspacegroup.Workspace{
		ID: "workspace-id", Path: mount,
		Sandbox: &workspacegroup.Sandbox{Name: runner.name, Agent: "shell", Mounts: []string{mount}},
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	policy := sandboxReconcilePolicy{createAttempts: 3, removalChecks: 5}

	if err := reconcileSandboxWithPolicy(context.Background(), runner, group, logger, policy); err != nil {
		t.Fatal(err)
	}
	if runner.createCalls != 3 {
		t.Fatalf("create calls = %d, want 3", runner.createCalls)
	}
	if runner.removeCalls != 3 {
		t.Fatalf("remove calls = %d, want initial removal plus two failed-attempt cleanups", runner.removeCalls)
	}
	if runner.removalChecks < 9 {
		t.Fatalf("removal checks = %d, want runtime disappearance verified after every removal", runner.removalChecks)
	}
	for _, expected := range []string{"sandbox remove attempt", "sandbox removal completed", "attempt=1", "attempt=2", "attempt=3", "max_attempts=3", "effective_mount_count=1"} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("sandbox reconciliation log is missing %q:\n%s", expected, logs.String())
		}
	}
	if strings.Contains(logs.String(), mount) {
		t.Fatalf("sandbox reconciliation log contains the mount command: %s", logs.String())
	}
}

func TestReconcileSandboxStopsAfterBoundedTransientFailures(t *testing.T) {
	mount := t.TempDir()
	runner := &sandboxRetryRunner{name: "retry-sandbox", mounts: []string{"/old-mount"}, createFailures: 4}
	group := workspacegroup.Workspace{
		ID: "workspace-id", Path: mount,
		Sandbox: &workspacegroup.Sandbox{Name: runner.name, Agent: "shell", Mounts: []string{mount}},
	}
	policy := sandboxReconcilePolicy{createAttempts: 3, removalChecks: 1}

	err := reconcileSandboxWithPolicy(context.Background(), runner, group, nil, policy)
	if err == nil || !strings.Contains(err.Error(), "after 3 attempt(s)") {
		t.Fatalf("error = %v", err)
	}
	if runner.createCalls != 3 {
		t.Fatalf("create calls = %d, want 3", runner.createCalls)
	}
	if runner.removeCalls != 4 {
		t.Fatalf("remove calls = %d, want initial removal plus cleanup after every failed create", runner.removeCalls)
	}
	if strings.Contains(err.Error(), mount) {
		t.Fatalf("create error contains the complete mount command: %v", err)
	}
}

func TestReconcileSandboxDoesNotRetryPermanentCreateFailure(t *testing.T) {
	mount := t.TempDir()
	runner := &sandboxRetryRunner{name: "retry-sandbox", mounts: []string{"/old-mount"}, permanentFailure: true}
	group := workspacegroup.Workspace{
		ID: "workspace-id", Path: mount,
		Sandbox: &workspacegroup.Sandbox{Name: runner.name, Agent: "shell", Mounts: []string{mount}},
	}
	policy := sandboxReconcilePolicy{createAttempts: 3, removalChecks: 1}

	err := reconcileSandboxWithPolicy(context.Background(), runner, group, nil, policy)
	if err == nil || !strings.Contains(err.Error(), "invalid sandbox kit") {
		t.Fatalf("error = %v", err)
	}
	if runner.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", runner.createCalls)
	}
	if runner.removeCalls != 2 {
		t.Fatalf("remove calls = %d, want initial removal and failed-runtime cleanup", runner.removeCalls)
	}
}

type sandboxRetryRunner struct {
	name             string
	exists           bool
	mounts           []string
	createFailures   int
	permanentFailure bool
	lingerChecks     int
	remainingLinger  int
	createCalls      int
	removeCalls      int
	removalChecks    int
	removing         bool
}

func (r *sandboxRetryRunner) LookPath(string) error { return nil }

func (r *sandboxRetryRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	if name != "sbx" || len(args) == 0 {
		return "", fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
	}
	switch args[0] {
	case "ls":
		if r.removing {
			r.removalChecks++
			if r.remainingLinger > 0 {
				r.remainingLinger--
			} else {
				r.exists = false
				r.removing = false
			}
		}
		sandboxes := []any{}
		if r.exists {
			sandboxes = append(sandboxes, map[string]any{
				"name": r.name, "agent": "shell", "status": "running", "workspaces": r.mounts,
			})
		}
		data, _ := json.Marshal(map[string]any{"sandboxes": sandboxes})
		return string(data), nil
	case "rm":
		r.removeCalls++
		r.removing = true
		r.remainingLinger = r.lingerChecks
		return "", nil
	case "create":
		r.createCalls++
		if r.permanentFailure {
			return "", fmt.Errorf("sbx %s failed: invalid sandbox kit", strings.Join(args, " "))
		}
		if r.createCalls <= r.createFailures {
			r.exists = true
			r.removing = false
			return "", fmt.Errorf("sbx %s failed: [POST /sandboxes][500 Internal Server Error] failed to run sandbox container", strings.Join(args, " "))
		}
		r.exists = true
		r.removing = false
		r.mounts = append([]string(nil), args[4:]...)
		return "", nil
	default:
		return "", fmt.Errorf("unexpected sbx command: %s", strings.Join(args, " "))
	}
}
