package git

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/linking"
	"radar/internal/protocol"
)

func TestWorktreeBindingTracksConcreteGitLifetime(t *testing.T) {
	for _, linked := range []bool{false, true} {
		name := "git-directory"
		if linked {
			name = "git-file"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			isolateGitBindingTest(t, root)
			repo := filepath.Join(root, "repo")
			runGit(t, ctx, root, "init", "--initial-branch=main", repo)
			runGit(t, ctx, repo, "config", "user.email", "radar@example.test")
			runGit(t, ctx, repo, "config", "user.name", "Radar Test")
			runGit(t, ctx, repo, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "initial")
			path := repo
			if linked {
				path = filepath.Join(root, "worktree")
				runGit(t, ctx, repo, "worktree", "add", "-b", "feature", path)
			}
			wt := worktree{Path: path, Branch: "feature", Head: "abc"}
			first := wt.SourceRef(ctx, linking.MarkMatcher{})
			assertConcreteWorktreeBinding(t, first)

			// Activity, commits, and timestamps are observations, not lifetimes.
			runGit(t, ctx, path, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "another commit")
			if err := os.WriteFile(filepath.Join(path, "working.txt"), []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitPath := filepath.Join(path, ".git")
			if linked {
				// Rewriting the locator in place does not create another worktree.
				contents, err := os.ReadFile(gitPath)
				if err != nil {
					t.Fatal(err)
				}
				gitDir := strings.TrimSpace(strings.TrimPrefix(string(contents), "gitdir: "))
				relative, err := filepath.Rel(path, gitDir)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(gitPath, []byte("gitdir: "+relative+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(root)
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(gitPath, later, later); err != nil {
				t.Fatal(err)
			}
			same := wt.SourceRef(ctx, linking.MarkMatcher{})
			if first.Binding() != same.Binding() || first.Binding().LinkingKey() != same.Binding().LinkingKey() {
				t.Fatalf("ordinary Git activity changed binding: first=%+v same=%+v", first.Binding(), same.Binding())
			}

			// Keep the old inode alive to ensure this test does not depend on the
			// allocator's choice when a .git artifact is replaced at the same path.
			oldGitPath := filepath.Join(root, "old-git")
			if err := os.Rename(gitPath, oldGitPath); err != nil {
				t.Fatal(err)
			}
			if linked {
				contents, err := os.ReadFile(oldGitPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(gitPath, contents, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				runGit(t, ctx, path, "init", "--initial-branch=main")
			}
			replacement := wt.SourceRef(ctx, linking.MarkMatcher{})
			assertConcreteWorktreeBinding(t, replacement)
			if first.BindingKey == replacement.BindingKey || first.Binding().LinkingKey() == replacement.Binding().LinkingKey() {
				t.Fatalf("replacement .git inherited old binding: first=%+v replacement=%+v", first.Binding(), replacement.Binding())
			}
			assertWorktreePublicIdentityUnchanged(t, first, replacement)
		})
	}
}

func TestRegisteredWorktreeReplacementKeepsWorkspaceAssociation(t *testing.T) {
	root := t.TempDir()
	isolateGitBindingTest(t, root)
	path := filepath.Join(root, "member")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	gitPath := filepath.Join(path, ".git")
	if err := os.WriteFile(gitPath, []byte("gitdir: /missing/test-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := workspaceGroupLink{ID: "workspace-id", TaskLinkingKey: "obsidian:task:one"}
	wt := worktree{Path: path, Branch: "feature"}
	first := wt.SourceRef(context.Background(), linking.MarkMatcher{}, link)
	assertConcreteWorktreeBinding(t, first)
	if err := os.Rename(gitPath, filepath.Join(root, "old-git")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitPath, []byte("gitdir: /missing/test-only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement := wt.SourceRef(context.Background(), linking.MarkMatcher{}, link)
	assertConcreteWorktreeBinding(t, replacement)
	if first.BindingKey == replacement.BindingKey {
		t.Fatal("replacement member inherited old .git identity")
	}
	assertWorktreePublicIdentityUnchanged(t, first, replacement)
	if first.WorkspaceID != "workspace-id" || replacement.WorkspaceID != first.WorkspaceID || replacement.ProvidesWorkspace {
		t.Fatalf("replacement changed registered workspace ownership: %+v", replacement)
	}
}

func TestWorktreeBindingDoesNotFallbackToReusablePath(t *testing.T) {
	root := t.TempDir()
	isolateGitBindingTest(t, root)
	path := filepath.Join(root, "missing")
	if key, err := worktreeBindingKey(path); key != "" || err == nil {
		t.Fatalf("missing .git binding = %q, error = %v", key, err)
	}
	if _, err := gitFileIdentity(filepath.Join(path, ".git")); err == nil {
		t.Fatal("missing .git received a concrete file identity")
	}
	ref := (worktree{Path: path, Branch: "feature"}).SourceRef(context.Background(), linking.MarkMatcher{})
	if ref.ID != "git:worktree:"+path || ref.BindingKey != "" || !strings.Contains(ref.BindingError, "could not determine concrete Git worktree lifetime") || !ref.ProvidesWorkspace || ref.Authority != protocol.SourceRefAuthorityNone {
		t.Fatalf("unavailable identity changed normal collection or lost validation error: %+v", ref)
	}
}

func isolateGitBindingTest(t *testing.T, root string) {
	t.Helper()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func assertConcreteWorktreeBinding(t *testing.T, ref protocol.SourceRef) {
	t.Helper()
	if ref.BindingKey == "" || ref.BindingError != "" {
		t.Fatalf("fixture filesystem does not provide concrete .git lifetime identity: %s", ref.BindingError)
	}
	binding := ref.Binding()
	if binding.Source != "git" || binding.Kind != "worktree" || binding.ID != ref.ID || binding.Key != ref.BindingKey || binding.WorkItem {
		t.Fatalf("binding changed local resource authority: %+v", binding)
	}
	if ref.Lifecycle != protocol.SourceRefLifecycleWorkspace || ref.Authority != protocol.SourceRefAuthorityNone {
		t.Fatalf("binding changed worktree lifecycle/authority: %+v", ref)
	}
}

func assertWorktreePublicIdentityUnchanged(t *testing.T, first, replacement protocol.SourceRef) {
	t.Helper()
	if first.ID != replacement.ID || first.EntityID != replacement.EntityID || first.CanonicalKey != replacement.CanonicalKey || !reflect.DeepEqual(first.LinkingKeys, replacement.LinkingKeys) || first.ProvidesWorkspace != replacement.ProvidesWorkspace {
		t.Fatalf("lifetime binding changed ordinary identity/workspace links: first=%+v replacement=%+v", first, replacement)
	}
}
