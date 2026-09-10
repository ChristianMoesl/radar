package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

// Keep managed resources allocated by all workspace tests out of the real
// per-user shared-directory root, including fake-runtime creation tests.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "radar-workspace-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv(hostTempDirEnv, root); err != nil {
		_ = os.RemoveAll(root)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

func sharedDirectoryFixture(t *testing.T, root, name string) workspacegroup.Workspace {
	t.Helper()
	anchor := filepath.Join(root, name)
	path, err := newSharedDirectory(anchor)
	if err != nil {
		t.Fatal(err)
	}
	return workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: name, Path: anchor, Sandbox: &workspacegroup.Sandbox{Name: name, Agent: "shell", SharedDirectory: path, Mounts: []string{anchor, path}}}
}

func TestSharedDirectoryIsolationPersistenceAndCleanup(t *testing.T) {
	root, temp := t.TempDir(), t.TempDir()
	t.Setenv(hostTempDirEnv, temp)
	first, second := sharedDirectoryFixture(t, root, "first"), sharedDirectoryFixture(t, root, "second")
	for _, group := range []workspacegroup.Workspace{first, second} {
		if err := ensureSharedDirectory(group); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{filepath.Dir(group.Sandbox.SharedDirectory), group.Sandbox.SharedDirectory} {
			info, err := os.Stat(dir)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("permissions for %s: %v, %v", dir, info, err)
			}
		}
	}
	image := filepath.Join(first.Sandbox.SharedDirectory, "clipboard.png")
	if err := os.WriteFile(image, []byte("image fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Shell environment changes must not move a recorded resource on reopen.
	t.Setenv("TMPDIR", second.Sandbox.SharedDirectory)
	t.Setenv(hostTempDirEnv, t.TempDir())
	if err := ensureSharedDirectory(first); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(image); err != nil || string(data) != "image fixture" {
		t.Fatalf("reopen lost files: %q, %v", data, err)
	}
	// A link stored by a sandbox must never make cleanup follow it.
	if err := os.Symlink(second.Sandbox.SharedDirectory, filepath.Join(first.Sandbox.SharedDirectory, "other")); err != nil {
		t.Fatal(err)
	}
	if err := removeSharedDirectory(first); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Sandbox.SharedDirectory); !os.IsNotExist(err) {
		t.Fatalf("not removed: %v", err)
	}
	if _, err := os.Stat(second.Sandbox.SharedDirectory); err != nil {
		t.Fatalf("removed sibling: %v", err)
	}
	if err := removeSharedDirectory(first); err != nil {
		t.Fatalf("cleanup is not idempotent: %v", err)
	}
}

func TestSharedDirectoryRejectsSymlinksAndWrongOwnershipPaths(t *testing.T) {
	for _, parentLink := range []bool{false, true} {
		t.Run(map[bool]string{false: "child", true: "parent"}[parentLink], func(t *testing.T) {
			t.Setenv(hostTempDirEnv, t.TempDir())
			group := sharedDirectoryFixture(t, t.TempDir(), "work")
			outside := t.TempDir()
			link := group.Sandbox.SharedDirectory
			if parentLink {
				link = filepath.Dir(link)
			} else if err := os.Mkdir(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			if err := ensureSharedDirectory(group); err == nil {
				t.Fatal("accepted symlink")
			}
			if err := removeSharedDirectory(group); err == nil {
				t.Fatal("cleaned through symlink")
			}
			if _, err := os.Stat(outside); err != nil {
				t.Fatal(err)
			}
		})
	}
	group := workspacegroup.Workspace{Path: "/work/first", Sandbox: &workspacegroup.Sandbox{SharedDirectory: "/tmp/radar-workspaces/other"}}
	if err := ensureSharedDirectory(group); err == nil {
		t.Fatal("accepted another workspace's directory")
	}
	if err := removeSharedDirectory(group); err == nil {
		t.Fatal("removed another workspace's directory")
	}
}

func TestSharedDirectoryAllocationUsesOriginalHostTempRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv(hostTempDirEnv, root)
	t.Setenv("TMPDIR", filepath.Join(root, "radar-workspaces", "current"))
	path, err := newSharedDirectory("/work/next")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "radar-workspaces", workspacegroup.ID("/work/next"))
	if path != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
	t.Setenv(hostTempDirEnv, "relative")
	if _, err := newSharedDirectory("/work/next"); err == nil {
		t.Fatal("accepted relative root")
	}
}

func TestSharedDirectoryReadinessRequiresWritableObservedMount(t *testing.T) {
	path := t.TempDir()
	for _, tc := range []struct {
		name   string
		mounts []string
		ready  bool
	}{
		{"absent", nil, false}, {"readonly", []string{path + ":ro"}, false}, {"writable", []string{path}, true},
		{"readonly-child", []string{filepath.Dir(path), path + ":ro"}, false},
		{"writable-child", []string{filepath.Dir(path) + ":ro", path}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sharedDirectoryReady(path, tc.mounts); got != tc.ready {
				t.Fatalf("ready = %v", got)
			}
		})
	}
	if sharedDirectoryReady(filepath.Join(path, "missing"), []string{path}) {
		t.Fatal("advertised missing directory")
	}
}

func TestWorkspaceCleanupRemovesRecordedSharedDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv(hostTempDirEnv, t.TempDir())
	group := sharedDirectoryFixture(t, root, "cleanup")
	if err := os.Mkdir(group.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureSharedDirectory(group); err != nil {
		t.Fatal(err)
	}
	if err := registerWorkspace(root, group); err != nil {
		t.Fatal(err)
	}
	if _, err := removeWorkspaceAnchor(root, protocol.CleanupTarget{SourceRefID: "workspace:" + group.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(group.Sandbox.SharedDirectory); !os.IsNotExist(err) {
		t.Fatalf("shared directory remains: %v", err)
	}
}

func TestReconcileExistingRuntimeRecreatesMissingSharedDirectory(t *testing.T) {
	t.Setenv(hostTempDirEnv, t.TempDir())
	group := sharedDirectoryFixture(t, t.TempDir(), "existing")
	runner := &sandboxRetryRunner{name: group.Sandbox.Name, exists: true, mounts: group.Sandbox.Mounts}
	if err := reconcileSandbox(context.Background(), runner, group, nil); err != nil {
		t.Fatal(err)
	}
	if !sharedDirectoryReady(group.Sandbox.SharedDirectory, runner.mounts) {
		t.Fatal("shared directory was not prepared")
	}
	if runner.createCalls != 0 {
		t.Fatal("unchanged runtime was unnecessarily recreated")
	}
}

func TestSharedDirectoryRejectsReadOnlyConfigurationConflict(t *testing.T) {
	t.Setenv(hostTempDirEnv, t.TempDir())
	group := sharedDirectoryFixture(t, t.TempDir(), "readonly-conflict")
	if _, err := desiredReconciledSandboxMounts(context.Background(), &fakeRunner{}, group, nil, []string{group.Sandbox.SharedDirectory + ":ro"}, nil); err == nil {
		t.Fatal("accepted a read-only override for the shared directory")
	}
}

func TestUnprovisionedWorkspaceDoesNotGuessOrCreateSharedDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv(hostTempDirEnv, root)
	group := workspacegroup.Workspace{Path: "/work/unprovisioned", Sandbox: &workspacegroup.Sandbox{Name: "unprovisioned", Agent: "shell"}}
	if err := ensureSharedDirectory(group); err != nil {
		t.Fatal(err)
	}
	if err := removeSharedDirectory(group); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unprovisioned workspace touched filesystem: %v, %v", entries, err)
	}
	group.Sandbox = nil
	if err := ensureSharedDirectory(group); err != nil {
		t.Fatal(err)
	}
}
