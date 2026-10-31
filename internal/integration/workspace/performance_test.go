package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"radar/internal/config"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
)

// Synthetic provider latency makes command counts and scheduling reproducible.
// This is not a real-provider benchmark or a claim about SBX provisioning.
type inspectionProbeRunner struct {
	mu                sync.Mutex
	group             workspacegroup.Workspace
	repos             []string
	calls             map[string]int
	delay             bool
	listOutput        string
	listErr, portsErr error
	ports             string
}

func (r *inspectionProbeRunner) LookPath(string) error { return nil }
func (r *inspectionProbeRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	key := name + " " + args[0]
	r.mu.Lock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[key]++
	r.mu.Unlock()
	if r.delay {
		delay := time.Millisecond
		if name == "sbx" {
			delay = 50 * time.Millisecond
		}
		if name == "fd" {
			delay = 20 * time.Millisecond
		}
		if err := waitForContext(ctx, delay); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch {
	case name == "fd":
		return strings.Join(r.repos, "\n"), nil
	case name == "sbx" && args[0] == "ls":
		if r.listErr != nil || r.listOutput != "" {
			return r.listOutput, r.listErr
		}
		data, _ := json.Marshal([]listedSandbox{{Name: r.group.Sandbox.Name, Workspaces: r.group.Sandbox.Mounts}})
		return string(data), nil
	case name == "sbx" && args[0] == "ports":
		if r.ports == "" {
			return "[]", r.portsErr
		}
		return r.ports, r.portsErr
	case name == "sbx" && args[0] == "version":
		return `{"client":{"version":"v0.50.0"}}`, nil
	case name == "git" && args[0] == "-C":
		if strings.HasSuffix(args[1], "member-1") {
			return " M file\n", nil
		}
		return "", nil
	case name == "git" && args[0] == "rev-parse":
		return cwd, nil
	default:
		return "", fmt.Errorf("unexpected probe: %s %v", name, args)
	}
}

func inspectionFixture(t testing.TB) (string, workspacegroup.Workspace, *inspectionProbeRunner) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "workspaces")
	sources := filepath.Join(base, "sources")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	cfg := config.Default()
	cfg.Workspace.RootDir, cfg.RepositoryDirs = root, []string{sources}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, sources, filepath.Dir(path)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	anchor := filepath.Join(root, "ABC-123")
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "ABC-123", Path: anchor}
	group.Sandbox = &workspacegroup.Sandbox{Name: "fixture", Agent: "shell", Mounts: []string{anchor}, Ports: []workspacegroup.SandboxPort{}}
	for i := range 4 {
		path := filepath.Join(anchor, fmt.Sprintf("member-%d", i))
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		group.Members = append(group.Members, workspacegroup.Member{Repository: filepath.Join(sources, fmt.Sprintf("repo-%02d", i)), Path: path, Branch: fmt.Sprintf("feature-%d", i)})
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	r := &inspectionProbeRunner{group: group}
	for i := range 62 {
		r.repos = append(r.repos, filepath.Join(sources, fmt.Sprintf("repo-%02d", i), ".git"))
	}
	return root, group, r
}

func BenchmarkWorkspaceContext(b *testing.B) {
	root, group, runner := inspectionFixture(b)
	runner.delay = true
	b.ResetTimer()
	for b.Loop() {
		if _, err := InspectWorkspace(context.Background(), runner, group.Path, root); err != nil {
			b.Fatal(err)
		}
	}
}

func cleanupLargeFixture(b *testing.B) (string, workspacegroup.Workspace) {
	root, group, _ := inspectionFixture(b)
	for _, member := range group.Members {
		for i := range 200 {
			dir := filepath.Join(member.Path, fmt.Sprintf("dir-%03d", i))
			if err := os.Mkdir(dir, 0700); err != nil {
				b.Fatal(err)
			}
			for j := range 100 {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%03d", j)), nil, 0600); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	return root, group
}

func BenchmarkCleanupMemberValidation(b *testing.B) {
	root, group := cleanupLargeFixture(b)
	b.ResetTimer()
	for b.Loop() {
		if err := ValidateDiscardingWorkspace(root, group.ID, integration.CleanupConfirmed); err != nil {
			b.Fatal(err)
		}
		for _, member := range group.Members {
			if err := ValidateDiscardingWorkspaceMember(root, group.ID, member, integration.CleanupConfirmed); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkWorkspacePlanning(b *testing.B) {
	root, group, runner := inspectionFixture(b)
	previous := workspaceGOOS
	workspaceGOOS = "darwin"
	b.Cleanup(func() { workspaceGOOS = previous })
	group.Members = nil
	shared, err := newSharedDirectory(group.Path)
	if err != nil {
		b.Fatal(err)
	}
	group.Sandbox.SharedDirectory = shared
	group.Sandbox.Mounts = []string{group.Path, shared}
	runner.group = group
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		b.Fatal(err)
	}
	revision, err := workspaceRevision(group, nil)
	if err != nil {
		b.Fatal(err)
	}
	request := ReconcileWorkspaceRequest{Workspace: group.Path, WorkspaceRoot: root, Revision: revision, prepareOnly: true, Desired: DesiredWorkspaceDescription{Worktrees: []DesiredWorkspaceWorktree{}, Sandbox: &DesiredWorkspaceSandbox{Ports: []workspacegroup.SandboxPort{}}}}
	runner.delay = true
	b.ResetTimer()
	for b.Loop() {
		plan, err := PreviewReconcileWorkspace(context.Background(), runner, request)
		if err != nil {
			b.Fatal(err)
		}
		if len(plan.Changes) != 0 {
			b.Fatalf("not a zero-change plan: %+v", plan.Changes)
		}
	}
}

// Model the diminishing remaining workspace without timing deletion: move each
// validated member out of the anchor with the benchmark timer stopped. Restore
// the fixture between iterations. No Git/runtime deletion is measured here.
func BenchmarkCleanupProgressiveValidation(b *testing.B) {
	root, group := cleanupLargeFixture(b)
	removed := b.TempDir()
	b.ResetTimer()
	for b.Loop() {
		if err := ValidateDiscardingWorkspace(root, group.ID, integration.CleanupConfirmed); err != nil {
			b.Fatal(err)
		}
		for _, member := range group.Members {
			if err := ValidateDiscardingWorkspaceMember(root, group.ID, member, integration.CleanupConfirmed); err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
			if err := os.Rename(member.Path, filepath.Join(removed, filepath.Base(member.Path))); err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
		}
		b.StopTimer()
		for _, member := range group.Members {
			if err := os.Rename(filepath.Join(removed, filepath.Base(member.Path)), member.Path); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()
	}
}
