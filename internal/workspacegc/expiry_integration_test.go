package workspacegc_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"radar/internal/cleanup"
	"radar/internal/integration"
	gitprovider "radar/internal/integration/git"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

// Runtime removal is recorded, never run against host tmux or SBX. Git, anchor,
// registry and file deletion use the real providers in an isolated directory.
type recordedRuntime struct {
	name, path string
	calls      *[]string
}

func (p recordedRuntime) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: p.name}
}
func (p recordedRuntime) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	// Actual runtime providers identify the shared workspace by path, not ID.
	return []protocol.CleanupTarget{{Source: p.name, Path: p.path, ResourceID: p.name}}, nil
}
func (p recordedRuntime) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	*p.calls = append(*p.calls, p.name)
	return req.Target, nil
}

func TestExpiredBundleDeletesLocalWorkButKeepsExternalData(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	root, repo := filepath.Join(home, "workspaces"), filepath.Join(home, "repo")
	configHome := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("RADAR_STATE", filepath.Join(home, "state.json"))
	if err := os.MkdirAll(filepath.Join(configHome, "radar"), 0o755); err != nil {
		t.Fatal(err)
	}
	config, _ := yaml.Marshal(map[string]any{"workspace": map[string]string{"root_dir": root}})
	if err := os.WriteFile(filepath.Join(configHome, "radar", "config.yaml"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(home, "init", "repo")
	git(repo, "config", "user.email", "radar@example.test")
	git(repo, "config", "user.name", "Radar Test")
	write(filepath.Join(repo, "README.md"), "initial\n")
	git(repo, "add", ".")
	git(repo, "commit", "-m", "initial")
	git(repo, "remote", "add", "origin", filepath.Join(home, "unavailable.git"))
	anchor := filepath.Join(root, "completed")
	member := filepath.Join(anchor, "repo--work")
	git(repo, "worktree", "add", "-b", "work", member)
	write(filepath.Join(member, "README.md"), "local-only commit\n")
	git(member, "commit", "-am", "unpublished work")
	write(filepath.Join(member, "README.md"), "uncommitted changes\n")
	write(filepath.Join(member, "untracked.txt"), "untracked work\n")
	write(filepath.Join(anchor, "unknown.txt"), "unknown root content\n")
	note := filepath.Join(home, "canonical.md")
	write(note, "preserve task note\n")
	external := filepath.Join(home, "external.txt")
	write(external, "preserve external data\n")
	for link, target := range map[string]string{"notes.md": note, "external-link": external} {
		if err := os.Symlink(target, filepath.Join(anchor, link)); err != nil {
			t.Fatal(err)
		}
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "completed", Path: anchor, NotePath: note, NoteLinkingKey: "test:task", TaskLinkingKey: "test:task", Members: []workspacegroup.Member{{Repository: repo, Path: member, Branch: "work"}}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	store, err := state.NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	done := time.Date(2026, 1, 2, 10, 20, 30, 0, time.UTC)
	store.SetTasks([]protocol.Task{{Attention: "done", SourceRefs: []protocol.SourceRef{
		{ID: "test:task", EntityID: "test:task", Source: "test", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, Signal: "done", CanonicalKey: "test:task", Metadata: map[string]string{"completed_at": done.Format(time.RFC3339)}},
		{ID: "workspace:" + group.ID, Source: "workspace", Kind: "workspace", Path: anchor, WorkspaceID: group.ID, ProvidesWorkspace: true, WorkspaceEntry: true, CanonicalKey: "workspace:" + anchor},
		{ID: "git:worktree:" + member, Source: "git", Kind: "worktree", Path: member, WorkspaceID: group.ID, Repo: repo, Branch: "work", CanonicalKey: "workspace:" + member},
	}}})
	var calls []string
	service := cleanup.New([]integration.CleanupProvider{
		recordedRuntime{name: "tmux", path: anchor, calls: &calls},
		recordedRuntime{name: "sbx", path: anchor, calls: &calls},
		gitprovider.NewSource(), workspace.NewSource(nil),
	})
	before, err := workspacegc.Run(ctx, store, service, nil, done.Add(workspacegc.ExpiryRetention-time.Second), workspacegc.Options{WorkspaceRoot: root, IgnoreRetention: true})
	if err != nil || len(before.Deleted) != 0 || len(calls) != 0 {
		t.Fatalf("early deletion: %+v %v calls=%v", before, err, calls)
	}
	after, err := workspacegc.Run(ctx, store, service, nil, done.Add(workspacegc.ExpiryRetention), workspacegc.Options{WorkspaceRoot: root})
	if err != nil || len(after.Deleted) != 1 || len(after.Skipped) != 0 || !reflect.DeepEqual(calls, []string{"tmux", "sbx"}) {
		t.Fatalf("expiry: %+v %v calls=%v", after, err, calls)
	}
	if _, err := os.Lstat(anchor); !os.IsNotExist(err) {
		t.Fatalf("workspace remains: %v", err)
	}
	for path, want := range map[string]string{note: "preserve task note\n", external: "preserve external data\n", filepath.Join(repo, "README.md"): "initial\n"} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("protected data changed at %s: %q %v", path, got, err)
		}
	}
	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/work")
	cmd.Dir = repo
	if err := cmd.Run(); err == nil {
		t.Fatal("expired managed branch remains")
	}
	registry, err := workspacegroup.Load(root)
	if err != nil || len(registry.Workspaces) != 0 {
		t.Fatalf("registry = %+v, %v", registry, err)
	}
}
