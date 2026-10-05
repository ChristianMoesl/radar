package workspace

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
)

func TestSandboxPrimaryMounts(t *testing.T) {
	for _, test := range []struct {
		name, primary string
		mounts, want  []string
		invalid       bool
	}{
		{"read-only sorts first", "/work/anchor", []string{"/config/startup:ro", "/work/anchor"}, []string{"/work/anchor", "/config/startup:ro"}, false},
		{"writable sorts first", "/work/anchor", []string{"/notes", "/work/anchor"}, []string{"/work/anchor", "/notes"}, false},
		{"covered by writable ancestor", "/work/anchor", []string{"/config:ro", "/work"}, []string{"/work/anchor", "/config:ro", "/work"}, false},
		{"writable overrides read-only ancestor", "/work/private/anchor", []string{"/work:ro", "/work/private/anchor"}, []string{"/work/private/anchor", "/work:ro"}, false},
		{"writable intermediate ancestor", "/work/private/anchor", []string{"/work:ro", "/work/private"}, []string{"/work/private/anchor", "/work:ro", "/work/private"}, false},
		{"read-only descendant preserved", "/work/anchor", []string{"/work/anchor/scripts:ro", "/work/anchor"}, []string{"/work/anchor", "/work/anchor/scripts:ro"}, false},
		{"cleaned primary aliases", "/work/anchor/", []string{" /work/./anchor ", "/config:ro", "/work/anchor/../anchor"}, []string{"/work/anchor", "/config:ro"}, false},
		{"empty entries ignored", "/work/anchor", []string{"", "/work/anchor", " "}, []string{"/work/anchor"}, false},
		{"read-only primary", "/work/anchor", []string{"/work/anchor:ro"}, nil, true},
		{"mixed primary modes", "/work/anchor", []string{"/work/anchor", "/work/anchor:ro"}, nil, true},
		{"read-only primary alias", "/work/anchor", []string{"/work", "/work/anchor/../anchor:ro"}, nil, true},
		{"read-only ancestor", "/work/anchor", []string{"/work:ro"}, nil, true},
		{"intervening read-only ancestor", "/work/private/anchor", []string{"/work", "/work/private:ro"}, nil, true},
		{"ambiguous ancestor modes", "/work/anchor", []string{"/work", "/work:ro"}, nil, true},
		{"uncovered primary", "/work/anchor", []string{"/unrelated"}, nil, true},
		{"no mounts", "/work/anchor", nil, nil, true},
		{"relative primary", "work/anchor", []string{"/work/anchor"}, nil, true},
		{"relative mount", "/work/anchor", []string{"/work/anchor", "relative"}, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := append([]string(nil), test.mounts...)
			got, err := sandboxPrimaryMounts(test.primary, test.mounts)
			if test.invalid {
				if err == nil {
					t.Fatalf("accepted inconsistent primary permissions: %v", got)
				}
			} else {
				if err != nil || !reflect.DeepEqual(got, test.want) {
					t.Fatalf("got %v, %v; want %v", got, err, test.want)
				}
				if !sameMountSet(got, test.mounts) {
					t.Fatalf("CLI ordering changed effective mounts: %v vs %v", got, test.mounts)
				}
			}
			if !reflect.DeepEqual(test.mounts, before) {
				t.Fatal("modified the canonical input mount set")
			}
		})
	}
}

func TestStartSandboxKeepsPrimaryBeforeReadOnlyMount(t *testing.T) {
	primary, readOnly := t.TempDir(), t.TempDir()+":ro"
	runner := &fakeRunner{}
	if _, err := startSandboxWithMounts(context.Background(), runner, primary, "ABC-123", SandboxKitConfig{Name: "shell"}, "", []string{readOnly, primary}); err != nil {
		t.Fatal(err)
	}
	assertCalled(t, runner.calls, "sbx", "create --name ABC-123 shell "+primary+" "+readOnly)
}

func TestSandboxRecreationRetriesKeepPrimaryFirst(t *testing.T) {
	primary, readOnly := t.TempDir(), t.TempDir()+":ro"
	mounts := []string{readOnly, primary}
	runner := &sandboxRetryRunner{name: "ABC-123", exists: true, mounts: []string{"/old-mount"}, createFailures: 1}
	group := workspacegroup.Workspace{ID: "ABC-123", Path: primary, Sandbox: &workspacegroup.Sandbox{Name: runner.name, Agent: "shell", Mounts: mounts}}
	if err := reconcileSandboxWithPolicy(context.Background(), runner, group, nil, sandboxReconcilePolicy{createAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, call := range runner.calls {
		if call.name == "sbx" && len(call.args) > 0 && call.args[0] == "create" {
			got := sandboxCreateMountArgs(call.args)
			if !reflect.DeepEqual(got, []string{primary, readOnly}) {
				t.Fatalf("incorrect primary/permissions on retry: %v", got)
			}
			count++
		}
	}
	if count != 2 || !reflect.DeepEqual(group.Sandbox.Mounts, mounts) {
		t.Fatalf("calls=%d; recorded mounts=%v", count, group.Sandbox.Mounts)
	}
}

func TestInvalidPrimaryDoesNotRemoveExistingRuntime(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "anchor")
	runner := &sandboxRetryRunner{name: "ABC-123", exists: true, mounts: []string{"/old-mount"}}
	group := workspacegroup.Workspace{ID: "ABC-123", Path: primary, Sandbox: &workspacegroup.Sandbox{Name: runner.name, Agent: "shell", Mounts: []string{root + ":ro"}}}
	err := reconcileSandboxWithPolicy(context.Background(), runner, group, nil, sandboxReconcilePolicy{})
	if err == nil || !strings.Contains(err.Error(), "primary workspace") {
		t.Fatalf("error=%v", err)
	}
	if runner.removeCalls != 0 || runner.createCalls != 0 || !runner.exists {
		t.Fatalf("invalid primary touched the runtime: %+v", runner)
	}
}

func TestPrimaryOrderingDoesNotRecreateMatchingRuntime(t *testing.T) {
	primary, readOnly := t.TempDir(), t.TempDir()+":ro"
	runner := &sandboxRetryRunner{name: "ABC-123", exists: true, mounts: []string{primary, readOnly}}
	group := workspacegroup.Workspace{ID: "ABC-123", Path: primary, Sandbox: &workspacegroup.Sandbox{Name: runner.name, Agent: "shell", Mounts: []string{readOnly, primary}}}
	if err := reconcileSandboxWithPolicy(context.Background(), runner, group, nil, sandboxReconcilePolicy{}); err != nil {
		t.Fatal(err)
	}
	if runner.removeCalls != 0 || runner.createCalls != 0 {
		t.Fatalf("ordering alone recreated the runtime: %+v", runner)
	}
}
