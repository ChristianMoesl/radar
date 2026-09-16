package git

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestCollectedCleanupIssuesMatchFreshPreviewAndClear(t *testing.T) {
	ctx := context.Background()
	home := cleanPhysicalPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	writeGitTestConfig(t, home)
	root, err := workspace.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "repo")
	remote := filepath.Join(home, "remote.git")
	runGit(t, ctx, home, "init", "--bare", remote)
	runGit(t, ctx, home, "clone", remote, repo)
	runGit(t, ctx, repo, "config", "user.email", "radar@example.test")
	runGit(t, ctx, repo, "config", "user.name", "Radar Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ctx, repo, "add", ".")
	runGit(t, ctx, repo, "commit", "-m", "initial")
	runGit(t, ctx, repo, "push", "origin", "HEAD:main")
	anchor := filepath.Join(root, "feature")
	path := filepath.Join(anchor, "repo--feature")
	runGit(t, ctx, repo, "worktree", "add", "-b", "feature", path)
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "feature", Path: anchor, Members: []workspacegroup.Member{{Repository: repo, Path: path, Branch: "feature"}}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	source := NewSource()
	check := func(want []string) {
		t.Helper()
		result := source.Collect(ctx, integration.CollectRequest{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if len(result.Observations) != 1 {
			t.Fatalf("observations = %+v", result.Observations)
		}
		ref := result.Observations[0].Ref
		targets, err := source.PreviewCleanup(ctx, integration.CleanupPreviewRequest{Task: protocol.Task{SourceRefs: []protocol.SourceRef{ref}}})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ref.CleanupIssues, want) || !reflect.DeepEqual(ref.CleanupIssues, cleanup.BlockingMessages(targets)) {
			t.Fatalf("collected %v, preview %v, want %v", ref.CleanupIssues, cleanup.BlockingMessages(targets), want)
		}
	}
	check(nil) // Published base is not unresolved just because its branch is local.
	if err := os.WriteFile(filepath.Join(path, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check([]string{"uncommitted changes will be discarded"})
	runGit(t, ctx, path, "add", ".")
	runGit(t, ctx, path, "commit", "-m", "feature")
	check([]string{"branch commits were not found remotely"})
	if err := os.WriteFile(filepath.Join(path, "scratch.txt"), []byte("scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check([]string{"uncommitted changes will be discarded", "branch commits were not found remotely"})
	if err := os.Remove(filepath.Join(path, "scratch.txt")); err != nil {
		t.Fatal(err)
	}
	runGit(t, ctx, path, "push", "origin", "feature")
	check(nil)
	// Expire only the observation fetch, then prove verification failures also
	// become unresolved rather than being silently treated as clean.
	runGit(t, ctx, repo, "remote", "set-url", "origin", filepath.Join(home, "missing.git"))
	source.observations.entries[repo].checked = time.Time{}
	check([]string{"branch publication could not be verified"})
	runGit(t, ctx, repo, "remote", "set-url", "origin", remote)
	source.observations.entries[repo].checked = time.Time{}
	check(nil)
}

func TestObservationKeepsOtherMemberIssuesAfterPreviewError(t *testing.T) {
	ctx := context.Background()
	home := cleanPhysicalPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	writeGitTestConfig(t, home)
	repo := filepath.Join(home, "repo")
	runGit(t, ctx, home, "init", repo)
	refs := []protocol.SourceRef{
		{ID: "main", Source: "git", Kind: "worktree", Path: repo},
		{ID: "missing", Source: "git", Kind: "worktree", Path: filepath.Join(home, "missing"), ProvidesWorkspace: true},
	}
	NewSource().collectCleanupIssues(ctx, refs)
	if !reflect.DeepEqual(refs[0].CleanupIssues, []string{"main working tree cannot be cleaned up from Radar"}) || !reflect.DeepEqual(refs[1].CleanupIssues, []string{"matching workspace cleanup target was not found"}) {
		t.Fatalf("issues = %+v", refs)
	}
}

type countingObservationRunner struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (*countingObservationRunner) LookPath(string) error { return nil }
func (r *countingObservationRunner) Run(context.Context, string, string, ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return "result", r.err
}

func TestObservationFetchCacheDeduplicatesOnlyRemoteFetches(t *testing.T) {
	for _, failure := range []error{nil, errors.New("offline")} {
		base := &countingObservationRunner{err: failure}
		runner := observationRunner{Runner: base, cache: newObservationFetchCache()}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := runner.Run(context.Background(), "/repo", "git", "fetch", "--prune", "origin")
				if err != failure {
					t.Errorf("error = %v, want %v", err, failure)
				}
			}()
		}
		wg.Wait()
		if base.calls != 1 {
			t.Fatalf("fetches = %d, want 1", base.calls)
		}
		for i := 0; i < 2; i++ {
			_, _ = runner.Run(context.Background(), "/repo", "git", "for-each-ref")
		}
		if base.calls != 3 {
			t.Fatalf("local checks were cached: %d calls", base.calls)
		}
		runner.cache.entries["/repo"].checked = time.Now().Add(-3 * time.Minute)
		_, _ = runner.Run(context.Background(), "/repo", "git", "fetch", "--prune", "origin")
		_, _ = runner.Run(context.Background(), "/other", "git", "fetch", "--prune", "origin")
		if base.calls != 5 {
			t.Fatalf("expiry or per-repo isolation failed: %d calls", base.calls)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := runner.Run(ctx, "/repo", "git", "fetch", "--prune", "origin"); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled check = %v", err)
		}
	}
}
