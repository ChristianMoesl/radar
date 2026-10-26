package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
)

type cloneFixture struct {
	root, sources, anchor, destination string
	req                                integration.RepositoryCloneRequest
	cfg                                config.Config
	runner                             *fixtureCloneRunner
}
type fixtureCloneRunner struct {
	sources []string
	calls   []string
	clone   func(context.Context, string, []string) (string, error)
}

func (r *fixtureCloneRunner) LookPath(string) error { return nil }
func (r *fixtureCloneRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if name == "fd" {
		var found []string
		for _, source := range r.sources {
			_ = filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() && entry.Name() == ".git" {
					found = append(found, path)
					return filepath.SkipDir
				}
				return nil
			})
		}
		return strings.Join(found, "\n"), nil
	}
	for _, arg := range args {
		if arg == "clone" {
			if r.clone != nil {
				return r.clone(ctx, cwd, args)
			}
			return "", errors.New("unexpected clone")
		}
	}
	return (cloneRunner{}).Run(ctx, cwd, name, args...)
}

func newCloneFixture(t *testing.T) *cloneFixture {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	sources, managed := filepath.Join(root, "sources"), filepath.Join(root, "workspaces")
	anchor := filepath.Join(managed, "task")
	for _, path := range []string{sources, anchor} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.RepositoryDirs = []string{sources}
	cfg.Workspace.RootDir = managed
	cfg.Workspace.AutoConfirm = true
	f := &cloneFixture{root: root, sources: sources, anchor: anchor, destination: filepath.Join(sources, "app"), cfg: cfg}
	f.req = integration.RepositoryCloneRequest{Repository: "https://github.com/acme/app.git", Destination: f.destination, Workspace: anchor}
	f.runner = &fixtureCloneRunner{sources: []string{sources}}
	f.save(t)
	g := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "task", Path: anchor, Members: []workspacegroup.Member{}}
	if err := workspacegroup.Save(managed, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{g}}); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *cloneFixture) save(t *testing.T) {
	t.Helper()
	data, err := yaml.Marshal(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, "config", "radar", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
func cloneGit(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func fixtureCheckout(t *testing.T, path, remote string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	cloneGit(t, path, "init", "-b", "feature/work")
	cloneGit(t, path, "config", "user.name", "Test User")
	cloneGit(t, path, "config", "user.email", "test@example.test")
	cloneGit(t, path, "remote", "add", "origin", remote)
	cloneGit(t, path, "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "fixture")
}

func TestClonePreviewIsReadOnlyAndApprovalIndependentOfAutoConfirm(t *testing.T) {
	f := newCloneFixture(t)
	plan, err := previewRepositoryClone(context.Background(), f.runner, f.req)
	if err != nil || !plan.RequiresConfirmation || plan.Status != "clone" || plan.Destination != f.destination || plan.PlanID == "" {
		t.Fatalf("%#v %v", plan, err)
	}
	if _, err := os.Lstat(f.destination); !os.IsNotExist(err) {
		t.Fatal("preview created destination")
	}
	for _, call := range f.runner.calls {
		for _, effect := range []string{"clone ", "fetch ", "pull ", "ls-remote", "checkout ", "reset "} {
			if strings.Contains(call, effect) {
				t.Fatalf("effect in preview: %s", call)
			}
		}
	}
	if _, err := applyRepositoryClone(context.Background(), f.runner, f.req, ""); err == nil {
		t.Fatal("missing plan accepted")
	}
	if _, err := os.Lstat(f.destination); !os.IsNotExist(err) {
		t.Fatal("unapproved apply mutated destination")
	}
}

func TestCloneApprovedOfflineCloneAndDiscovery(t *testing.T) {
	f := newCloneFixture(t)
	fixture := filepath.Join(f.root, "fixture")
	fixtureCheckout(t, fixture, "https://github.com/acme/fixture.git")
	plan, err := previewRepositoryClone(context.Background(), f.runner, f.req)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	f.runner.clone = func(ctx context.Context, cwd string, args []string) (string, error) {
		attempts++
		expected := []string{"-c", "core.hooksPath=/dev/null", "clone", "--", "https://github.com/acme/app.git", f.destination}
		if !reflect.DeepEqual(args, expected) || cwd != f.sources {
			t.Fatalf("%s %#v", cwd, args)
		}
		// Offline fixture substitution is confined to the injected transport. The
		// production validator still sees/requires the canonical GitHub remote.
		output, err := (cloneRunner{}).Run(ctx, cwd, "git", "-c", "core.hooksPath=/dev/null", "clone", "--", fixture, f.destination)
		if err == nil {
			cloneGit(t, f.destination, "remote", "set-url", "origin", plan.Remote)
		}
		return output, err
	}
	result, err := applyRepositoryClone(context.Background(), f.runner, f.req, plan.PlanID)
	if err != nil || !result.OK || result.Status != "cloned" || result.Repository != f.destination || attempts != 1 {
		t.Fatalf("%#v %d %v", result, attempts, err)
	}
	next, err := previewRepositoryClone(context.Background(), f.runner, f.req)
	if err != nil || next.Status != "reused" || next.RequiresConfirmation {
		t.Fatalf("%#v %v", next, err)
	}
	if got := cloneGit(t, f.destination, "branch", "--show-current"); got != "feature/work" {
		t.Fatal(got)
	}
}

func TestCloneReusesDirtyMatchingCheckoutWithoutMutation(t *testing.T) {
	f := newCloneFixture(t)
	existing := filepath.Join(f.sources, "custom-name")
	fixtureCheckout(t, existing, "git@github.com:Acme/App.git")
	dirty := filepath.Join(existing, "untracked")
	if err := os.WriteFile(dirty, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := cloneGit(t, existing, "status", "--porcelain")
	head := cloneGit(t, existing, "rev-parse", "HEAD")
	plan, err := previewRepositoryClone(context.Background(), f.runner, f.req)
	if err != nil || plan.RequiresConfirmation || plan.Repository != existing || plan.Status != "reused" {
		t.Fatalf("%#v %v", plan, err)
	}
	result, err := applyRepositoryClone(context.Background(), f.runner, f.req, plan.PlanID)
	if err != nil || !result.OK || result.Status != "reused" {
		t.Fatalf("%#v %v", result, err)
	}
	if got := cloneGit(t, existing, "status", "--porcelain"); got != before {
		t.Fatal("dirty state changed")
	}
	if got := cloneGit(t, existing, "rev-parse", "HEAD"); got != head {
		t.Fatal("HEAD changed")
	}
	if got := cloneGit(t, existing, "remote", "get-url", "origin"); got != "git@github.com:Acme/App.git" {
		t.Fatal("remote changed")
	}
	if _, err := os.Lstat(f.destination); !os.IsNotExist(err) {
		t.Fatal("duplicate checkout created")
	}
}

func TestCloneDestinationConflictsPreserveData(t *testing.T) {
	for _, kind := range []string{"directory", "file", "symlink", "unrelated", "linked", "bare", "parent-repo", "multiple-origins", "symlink-gitdir"} {
		t.Run(kind, func(t *testing.T) {
			f := newCloneFixture(t)
			switch kind {
			case "directory":
				_ = os.Mkdir(f.destination, 0o755)
			case "file":
				_ = os.WriteFile(f.destination, []byte("keep"), 0o600)
			case "symlink":
				_ = os.Symlink(f.anchor, f.destination)
			case "unrelated":
				fixtureCheckout(t, f.destination, "https://github.com/acme/other.git")
			case "linked":
				primary := filepath.Join(f.root, "primary")
				fixtureCheckout(t, primary, "https://github.com/acme/app.git")
				cloneGit(t, primary, "worktree", "add", "-b", "another", f.destination)
			case "bare":
				_ = os.Mkdir(f.destination, 0o755)
				cloneGit(t, f.destination, "init", "--bare")
			case "parent-repo":
				fixtureCheckout(t, f.sources, "https://github.com/acme/app.git")
				_ = os.Mkdir(f.destination, 0o755)
			case "multiple-origins":
				fixtureCheckout(t, f.destination, "https://github.com/acme/app.git")
				cloneGit(t, f.destination, "config", "--add", "remote.origin.url", "https://github.com/acme/other.git")
			case "symlink-gitdir":
				fixtureCheckout(t, f.destination, "https://github.com/acme/app.git")
				actual := filepath.Join(f.root, "gitdir")
				_ = os.Rename(filepath.Join(f.destination, ".git"), actual)
				_ = os.Symlink(actual, filepath.Join(f.destination, ".git"))
			}
			before, err := os.Lstat(f.destination)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := previewRepositoryClone(context.Background(), f.runner, f.req); err == nil {
				t.Fatal("conflict accepted")
			}
			after, err := os.Lstat(f.destination)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("destination changed")
			}
		})
	}
}

func TestCloneRejectsAmbiguityAndUnsafeDestinations(t *testing.T) {
	t.Run("ambiguous", func(t *testing.T) {
		f := newCloneFixture(t)
		for _, leaf := range []string{"one", "two"} {
			fixtureCheckout(t, filepath.Join(f.sources, leaf), f.req.Repository)
		}
		if _, err := previewRepositoryClone(context.Background(), f.runner, f.req); err == nil {
			t.Fatal("ambiguous checkout accepted")
		}
	})
	for _, kind := range []string{"relative", "root", "outside", "sibling-prefix", "traversal", "managed", "symlink-escape", "unregistered", "unsupported-remote", "git-metadata-name"} {
		t.Run(kind, func(t *testing.T) {
			f := newCloneFixture(t)
			switch kind {
			case "relative":
				f.req.Destination = "app"
			case "root":
				f.req.Destination = f.sources
			case "outside":
				f.req.Destination = filepath.Join(f.root, "app")
			case "sibling-prefix":
				f.req.Destination = filepath.Join(f.sources+"-other", "app")
			case "traversal":
				f.req.Destination = f.sources + "/nested/../app"
			case "managed":
				f.cfg.RepositoryDirs = append(f.cfg.RepositoryDirs, filepath.Dir(f.anchor))
				f.save(t)
				f.req.Destination = filepath.Join(filepath.Dir(f.anchor), "other")
			case "symlink-escape":
				_ = os.Symlink(f.anchor, f.destination)
			case "unregistered":
				f.req.Workspace = f.root
			case "git-metadata-name":
				f.req.Destination = filepath.Join(f.sources, ".git")
			case "unsupported-remote":
				f.req.Repository = "ext::sh -c secret"
			}
			if _, err := previewRepositoryClone(context.Background(), f.runner, f.req); err == nil {
				t.Fatalf("accepted %#v", f.req)
			}
		})
	}
}

func TestCloneRechecksDestinationRootAndConfigBeforeApply(t *testing.T) {
	for _, kind := range []string{"destination", "symlink", "root-replaced", "root-symlink", "configuration"} {
		t.Run(kind, func(t *testing.T) {
			f := newCloneFixture(t)
			plan, err := previewRepositoryClone(context.Background(), f.runner, f.req)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "destination":
				_ = os.Mkdir(f.destination, 0o755)
			case "symlink":
				_ = os.Symlink(f.anchor, f.destination)
			case "root-replaced":
				_ = os.Rename(f.sources, f.sources+"-old")
				_ = os.Mkdir(f.sources, 0o755)
			case "root-symlink":
				_ = os.Rename(f.sources, f.sources+"-old")
				_ = os.Symlink(f.sources+"-old", f.sources)
			case "configuration":
				f.cfg.RepositoryDirs = []string{f.root}
				f.save(t)
			}
			f.runner.clone = func(context.Context, string, []string) (string, error) { t.Fatal("clone after drift"); return "", nil }
			if _, err := applyRepositoryClone(context.Background(), f.runner, f.req, plan.PlanID); err == nil {
				t.Fatal("changed plan accepted")
			}
		})
	}
}

func TestCloneFailureAndCancellationKeepOwnedPartialPath(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{true: "cancelled", false: "failed"}[cancelled], func(t *testing.T) {
			f := newCloneFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			plan, err := previewRepositoryClone(ctx, f.runner, f.req)
			if err != nil {
				t.Fatal(err)
			}
			f.runner.clone = func(context.Context, string, []string) (string, error) {
				if cancelled {
					cancel()
				}
				_ = os.WriteFile(filepath.Join(f.destination, "partial"), []byte("preserve"), 0o600)
				return "", errors.New("helper secret credential=do-not-leak")
			}
			result, err := applyRepositoryClone(ctx, f.runner, f.req, plan.PlanID)
			if err != nil || result.OK || result.PartialPath != f.destination || strings.Contains(result.Error, "do-not-leak") {
				t.Fatalf("%#v %v", result, err)
			}
			if _, err := os.Stat(filepath.Join(f.destination, "partial")); err != nil {
				t.Fatal("partial data deleted")
			}
		})
	}
}

func TestCloneOutputBounded(t *testing.T) {
	b := &cloneOutput{}
	input := []byte(strings.Repeat("x", 100000))
	n, err := b.Write(input)
	if err != nil || n != len(input) || b.value.Len() != 64*1024 || !b.truncated {
		t.Fatalf("%d %d %v", n, b.value.Len(), err)
	}
}

type concurrentDestinationRunner struct {
	*fixtureCloneRunner
	destination string
	created     bool
}

func (r *concurrentDestinationRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	output, err := r.fixtureCloneRunner.Run(ctx, cwd, name, args...)
	if name == "fd" && !r.created {
		r.created = true
		if err := os.Mkdir(r.destination, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(r.destination, "other-process"), []byte("preserve"), 0o600); err != nil {
			return "", err
		}
	}
	return output, err
}
func TestCloneExclusiveReservationDoesNotOverwriteConcurrentDestination(t *testing.T) {
	f := newCloneFixture(t)
	plan, err := previewRepositoryClone(context.Background(), f.runner, f.req)
	if err != nil {
		t.Fatal(err)
	}
	runner := &concurrentDestinationRunner{fixtureCloneRunner: f.runner, destination: f.destination}
	if _, err := applyRepositoryClone(context.Background(), runner, f.req, plan.PlanID); err == nil {
		t.Fatal("concurrent destination accepted")
	}
	data, err := os.ReadFile(filepath.Join(f.destination, "other-process"))
	if err != nil || string(data) != "preserve" {
		t.Fatalf("%q %v", data, err)
	}
}
