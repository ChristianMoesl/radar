package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
)

type removalOutcomeRunner struct {
	ExecRunner
	removed                 bool
	deletes, listings, refs int
	remove                  func(context.Context, string, string, ...string) (string, error)
	list                    func(context.Context, string, string, ...string) (string, error)
	deleteRef               func(context.Context, string, string, ...string) (string, error)
	refList                 func(context.Context, string, string, ...string) (string, error)
}

func (r *removalOutcomeRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name == "git" {
		command := strings.Join(args, " ")
		if strings.Contains(command, " worktree remove ") {
			r.removed = true
			if r.remove != nil {
				return r.remove(ctx, cwd, name, args...)
			}
		}
		if command == "worktree list --porcelain -z" && r.removed {
			r.listings++
			if r.list != nil {
				return r.list(ctx, cwd, name, args...)
			}
		}
		if len(args) > 0 && args[0] == "update-ref" {
			r.deletes++
			if r.deleteRef != nil {
				return r.deleteRef(ctx, cwd, name, args...)
			}
		}
		if len(args) > 0 && args[0] == "for-each-ref" && r.removed {
			r.refs++
			if r.refList != nil {
				return r.refList(ctx, cwd, name, args...)
			}
		}
	}
	return r.ExecRunner.Run(ctx, cwd, name, args...)
}

func TestManagedGitRemovalRequiresObservedOutcomes(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			for _, scenario := range []string{"ignored_remove", "directory_residue", "symlink_replacement", "registration_residue", "list_error", "list_empty", "list_malformed", "list_unterminated", "list_relative_path", "list_duplicate_path", "cancel_after_remove", "cancel_after_list", "path_reappears_during_list", "remove_error", "ignored_ref_delete", "recreated_ref", "ref_list_error", "ref_list_malformed", "cancel_after_ref_list", "delete_error"} {
				t.Run(scenario, func(t *testing.T) {
					f := newBranchMutationFixture(t)
					ctx, cancel := context.WithCancel(f.ctx)
					defer cancel()
					marker := errors.New("private fixture failure")
					runner := &removalOutcomeRunner{}
					branchDeleted := false
					switch scenario {
					case "ignored_remove":
						runner.remove = func(context.Context, string, string, ...string) (string, error) { return "", nil }
					case "directory_residue", "symlink_replacement", "cancel_after_remove":
						runner.remove = func(ctx context.Context, cwd, name string, args ...string) (string, error) {
							out, err := runner.ExecRunner.Run(ctx, cwd, name, args...)
							if err != nil {
								return out, err
							}
							switch scenario {
							case "directory_residue":
								err = os.Mkdir(f.path, 0700)
							case "symlink_replacement":
								err = os.Symlink(f.linked, f.path)
							case "cancel_after_remove":
								cancel()
							}
							return out, err
						}
					case "registration_residue":
						runner.remove = func(context.Context, string, string, ...string) (string, error) { return "", os.RemoveAll(f.path) }
					case "list_error", "list_empty", "list_malformed", "list_unterminated", "list_relative_path", "list_duplicate_path", "cancel_after_list", "path_reappears_during_list":
						runner.list = func(ctx context.Context, cwd, name string, args ...string) (string, error) {
							switch scenario {
							case "list_error":
								return "", marker
							case "list_empty":
								return "", nil
							case "list_malformed":
								return "private unparsed output", nil
							case "list_unterminated":
								return "worktree " + f.repo + "\x00", nil
							case "list_relative_path":
								return "worktree relative\x00bare\x00\x00", nil
							case "list_duplicate_path":
								return "worktree " + f.repo + "\x00bare\x00\x00worktree " + f.repo + "\x00bare\x00\x00", nil
							}
							out, err := runner.ExecRunner.Run(ctx, cwd, name, args...)
							if scenario == "path_reappears_during_list" {
								if err != nil {
									return out, err
								}
								return out, os.Mkdir(f.path, 0700)
							}
							cancel()
							return out, err
						}
					case "remove_error":
						runner.remove = func(context.Context, string, string, ...string) (string, error) { return "", marker }
					case "ignored_ref_delete", "recreated_ref":
						runner.deleteRef = func(ctx context.Context, cwd, name string, args ...string) (string, error) {
							if scenario == "ignored_ref_delete" {
								return "", nil
							}
							out, err := runner.ExecRunner.Run(ctx, cwd, name, args...)
							if err != nil {
								return out, err
							}
							_, err = runner.ExecRunner.Run(ctx, f.repo, "git", "branch", "feature", "other")
							return out, err
						}
					case "ref_list_error", "ref_list_malformed", "cancel_after_ref_list":
						branchDeleted = true
						runner.refList = func(ctx context.Context, cwd, name string, args ...string) (string, error) {
							if scenario == "ref_list_error" {
								return "", marker
							}
							if scenario == "ref_list_malformed" {
								return "private unexpected output", nil
							}
							out, err := runner.ExecRunner.Run(ctx, cwd, name, args...)
							cancel()
							return out, err
						}
					case "delete_error":
						runner.deleteRef = func(context.Context, string, string, ...string) (string, error) { return "", marker }
					}
					_, err := RemoveManagedWorktree(ctx, runner, f.member(), mode, true)
					if err == nil {
						t.Fatal("unverified native removal was reported as successful", scenario)
					}
					if strings.HasSuffix(scenario, "error") && !errors.Is(err, marker) {
						t.Fatalf("native error identity lost: %v", err)
					}
					if strings.HasPrefix(scenario, "cancel_") && !errors.Is(err, context.Canceled) {
						t.Fatalf("cancellation lost: %v", err)
					}
					wantDeletes := 0
					if strings.Contains(scenario, "ref") || scenario == "delete_error" {
						wantDeletes = 1
					}
					if runner.deletes != wantDeletes {
						t.Fatalf("branch deletion calls = %d, want %d", runner.deletes, wantDeletes)
					}
					_, exists := (ExecRunner{}).Run(f.ctx, f.repo, "git", "show-ref", "--verify", "--quiet", "refs/heads/feature")
					if (exists != nil) != branchDeleted {
						t.Fatalf("branch absent = %v, want %v", exists, branchDeleted)
					}
					if scenario == "ignored_remove" {
						if _, err := os.Stat(filepath.Join(f.path, ".git")); err != nil {
							t.Fatal("ignored removal changed original worktree", err)
						}
					}
					// Verification failure must release the native branch admission, without retry.
					release, err := acquireWorktreeBranchMutation(f.ctx, ExecRunner{}, f.repo, "feature")
					if err != nil {
						t.Fatal("failed verification leaked branch admission", err)
					}
					release()
				})
			}
		})
	}
}

func TestManagedGitRemovalObservedControls(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, kind := range []string{"delete", "protected", "shared", "keep"} {
			t.Run(fmt.Sprint(mode)+"/"+kind, func(t *testing.T) {
				f := newBranchMutationFixture(t)
				member := f.member()
				if kind == "protected" {
					runGitE2E(t, f.ctx, f.path, "branch", "-m", "master")
					member.Branch = "master"
				}
				if kind == "shared" {
					runGitE2E(t, f.ctx, f.repo, "worktree", "add", "--force", filepath.Join(f.root, "shared"), "feature")
				}
				runner := &removalOutcomeRunner{}
				retained := 0
				checkAdmission := func() {
					release, err := acquireWorktreeBranchMutation(f.ctx, ExecRunner{}, f.linked, member.Branch)
					if err == nil {
						release()
						t.Fatal("branch admission released before fresh outcome observation")
					}
					independent, err := acquireWorktreeBranchMutation(f.ctx, ExecRunner{}, f.repo, "unrelated")
					if err != nil {
						t.Fatal("outcome observation blocked unrelated branch", err)
					}
					independent()
					retained++
				}
				runner.list = func(ctx context.Context, cwd, name string, args ...string) (string, error) {
					checkAdmission()
					return runner.ExecRunner.Run(ctx, cwd, name, args...)
				}
				runner.refList = func(ctx context.Context, cwd, name string, args ...string) (string, error) {
					checkAdmission()
					return runner.ExecRunner.Run(ctx, cwd, name, args...)
				}
				result, err := RemoveManagedWorktree(f.ctx, runner, member, mode, kind != "keep")
				if err != nil || result.Path != f.path {
					t.Fatalf("valid removal failed: %+v, %v", result, err)
				}
				if _, err := os.Lstat(f.path); !os.IsNotExist(err) {
					t.Fatal("worktree remains", err)
				}
				wantDeletes, wantRefs := 0, 0
				if kind == "delete" {
					wantDeletes, wantRefs = 1, 1
				}
				if runner.deletes != wantDeletes || runner.listings != 1 || runner.refs != wantRefs || retained != 1+wantRefs {
					t.Fatalf("outcome observations = %+v, retained=%d", runner, retained)
				}
				_, exists := (ExecRunner{}).Run(f.ctx, f.repo, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+member.Branch)
				if (exists != nil) != (kind == "delete") {
					t.Fatal("protected/shared branch promise changed", exists)
				}
				release, err := acquireWorktreeBranchMutation(f.ctx, ExecRunner{}, f.repo, member.Branch)
				if err != nil {
					t.Fatal("successful verification leaked admission", err)
				}
				release()
			})
		}
	}
}

func TestManagedGitRemovalRejectsReplacedRepository(t *testing.T) {
	for _, kind := range []string{"repository", "common_directory"} {
		t.Run(kind, func(t *testing.T) {
			f := newBranchMutationFixture(t)
			backup := filepath.Join(filepath.Dir(f.repo), "original")
			runner := &removalOutcomeRunner{}
			runner.remove = func(ctx context.Context, cwd, name string, args ...string) (string, error) {
				out, err := runner.ExecRunner.Run(ctx, cwd, name, args...)
				if err != nil {
					return out, err
				}
				path := f.repo
				if kind == "common_directory" {
					path = filepath.Join(f.repo, ".git")
				}
				if err := os.Rename(path, backup); err != nil {
					return out, err
				}
				if err := os.Mkdir(path, 0700); err != nil {
					return out, err
				}
				// Both pinned paths exist with replacement identities. The test
				// must not depend on map iteration choosing mismatch over ENOENT.
				if kind == "repository" {
					if err := os.Mkdir(filepath.Join(path, ".git"), 0700); err != nil {
						return out, err
					}
				}
				return out, nil
			}
			_, err := RemoveManagedWorktree(f.ctx, runner, f.member(), integration.CleanupConfirmed, true)
			if err == nil || !strings.Contains(err.Error(), "repository changed") || runner.deletes != 0 || runner.listings != 0 {
				t.Fatalf("replaced repository supplied absence evidence: %v, %+v", err, runner)
			}
			path := f.repo
			if kind == "common_directory" {
				path = filepath.Join(f.repo, ".git")
			}
			if kind == "repository" {
				if err := os.Remove(filepath.Join(path, ".git")); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(backup, path); err != nil {
				t.Fatal(err)
			}
			if _, err := (ExecRunner{}).Run(f.ctx, f.repo, "git", "show-ref", "--verify", "--quiet", "refs/heads/feature"); err != nil {
				t.Fatal("unverified removal deleted original branch", err)
			}
			release, err := acquireWorktreeBranchMutation(f.ctx, ExecRunner{}, f.repo, "feature")
			if err != nil {
				t.Fatal("repository drift leaked original admission", err)
			}
			release()
		})
	}
}

func TestManagedGitRemovalUsesNativeUnquotedPaths(t *testing.T) {
	for _, suffix := range []string{" space", "\ttab", "\nnewline", `"quote`} {
		t.Run(fmt.Sprintf("%q", suffix), func(t *testing.T) {
			f := newBranchMutationFixture(t)
			next := f.path + suffix
			runGitE2E(t, f.ctx, f.repo, "worktree", "move", f.path, next)
			f.path = next
			// An unrelated unusual path must not corrupt the complete native listing.
			runGitE2E(t, f.ctx, f.repo, "worktree", "move", f.linked, f.linked+suffix)
			plan, err := PlanManagedWorktreeRemoval(f.ctx, ExecRunner{}, f.member())
			if err != nil {
				t.Fatal(err)
			}
			// The existing line-based branch-sharing planner conservatively keeps
			// newline-path branches. Outcome observation must not override it.
			if plan.DeleteBranch != (suffix != "\nnewline") {
				t.Fatal("native branch-preservation precondition changed", plan)
			}
			runner := &removalOutcomeRunner{}
			if _, err := RemoveManagedWorktree(f.ctx, runner, f.member(), integration.CleanupConfirmed, true); err != nil {
				t.Fatal(err)
			}
			wantDeletes := 0
			if plan.DeleteBranch {
				wantDeletes = 1
			}
			if runner.listings != 1 || runner.refs != wantDeletes || runner.deletes != wantDeletes {
				t.Fatal("native outcome verification or branch-preservation promise changed", runner)
			}
			if _, err := os.Lstat(f.path); !os.IsNotExist(err) {
				t.Fatal("unquoted target path remains", err)
			}
			_, branchErr := (ExecRunner{}).Run(f.ctx, f.repo, "git", "show-ref", "--verify", "--quiet", "refs/heads/feature")
			if (branchErr != nil) != plan.DeleteBranch {
				t.Fatal("observed ref outcome differs from native plan", branchErr)
			}
		})
	}
}
