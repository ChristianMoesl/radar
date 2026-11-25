package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

// A keep-branch preview previously bypassed the managed remover's independent
// outcome observation, even though its caller still dropped member registration.
func TestKeepBranchGitRemovalRequiresObservedOutcome(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, service := range []bool{false, true} {
			for _, policy := range []string{"protected", "sharing-ended"} {
				for _, scenario := range []string{"ignored-remove", "path-residue", "registered-residue", "unavailable-list", "malformed-list", "success"} {
					t.Run(fmt.Sprintf("%d/service=%t/%s/%s", mode, service, policy, scenario), func(t *testing.T) {
						branch := "feature"
						if policy == "protected" {
							branch = "master"
						}
						f := newCleanupExpiryFixture(t, branch)
						t.Setenv("RADAR_SOCKET", filepath.Join(f.home, "run", "radar.sock"))
						t.Setenv("RADAR_AGENT_ROOT", filepath.Join(f.home, "agents"))
						if policy == "sharing-ended" {
							runGit(t, f.ctx, f.repo, "worktree", "add", "--force", filepath.Join(f.home, "other"), branch)
						}
						target := f.preview(t, mode)
						if target.Operation["delete_branch"] != "" {
							t.Fatal("fixture did not preserve branch", target)
						}
						if policy == "sharing-ended" {
							runGit(t, f.ctx, f.repo, "worktree", "remove", filepath.Join(f.home, "other"))
						}
						registry, head := f.snapshot(t)
						real, err := exec.LookPath("git")
						if err != nil {
							t.Fatal(err)
						}
						bin := t.TempDir()
						calls := filepath.Join(bin, "calls")
						script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\n", calls)
						switch scenario {
						case "ignored-remove":
							script += "case \"$*\" in *'worktree remove '*) exit 0;; esac\n"
						case "path-residue":
							script += fmt.Sprintf("case \"$*\" in *'worktree remove '*) %q \"$@\" || exit $?; mkdir %q; exit $?;; esac\n", real, f.path)
						case "registered-residue":
							script += fmt.Sprintf("case \"$*\" in *'worktree remove '*) rm -rf -- %q; exit $?;; esac\n", f.path)
						case "unavailable-list":
							script += "case \"$*\" in 'worktree list --porcelain -z') exit 19;; esac\n"
						case "malformed-list":
							script += "case \"$*\" in 'worktree list --porcelain -z') printf 'private invalid evidence'; exit 0;; esac\n"
						}
						script += fmt.Sprintf("exec %q \"$@\"\n", real)
						if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
							t.Fatal(err)
						}
						t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
						before, later := &outcomeBoundaryProvider{name: "before", path: filepath.Join(f.home, "before")}, &outcomeBoundaryProvider{name: "later", path: filepath.Join(f.home, "later")}
						for _, p := range []*outcomeBoundaryProvider{before, later} {
							if err := os.WriteFile(p.path, []byte("intact"), 0o600); err != nil {
								t.Fatal(err)
							}
						}
						allow := scenario == "success"
						if service {
							result, executeErr := cleanup.New([]integration.CleanupProvider{before, Source{}, later}).Execute(f.ctx, protocol.CleanupPreview{TaskID: 7, Targets: []protocol.CleanupTarget{{Source: "before", Kind: "fixture", Path: before.path}, target, {Source: "later", Kind: "fixture", Path: later.path}}}, cleanup.ExecuteOptions{Mode: mode})
							wantCompleted, wantLater, wantAttempts := 1, 0, 2
							if allow {
								wantCompleted, wantLater, wantAttempts = 3, 1, 3
							}
							if (executeErr == nil) != allow || len(result.Targets) != wantCompleted || before.calls != 1 || later.calls != wantLater || result.ExecutionObservation == nil || result.ExecutionObservation.AttemptedTargets != wantAttempts || (!allow && result.ExecutionObservation.Stage != "provider_call") {
								t.Fatalf("unverified keep-branch cleanup advanced bundle: result=%+v err=%v calls=%d/%d", result, executeErr, before.calls, later.calls)
							}
						} else {
							result, executeErr := (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: mode})
							if (executeErr == nil) != allow || (!allow && result.Source != "") {
								t.Fatalf("unverified keep-branch cleanup returned success: %+v err=%v", result, executeErr)
							}
						}
						after, gotHead := f.snapshot(t)
						if gotHead != head {
							t.Fatal("preserved branch changed", gotHead, head)
						}
						if !allow && string(after) != string(registry) {
							t.Fatal("unverified keep-branch cleanup dropped registration")
						}
						if allow {
							f.assertRemoved(t)
						} else {
							if data, err := os.ReadFile(later.path); err != nil || string(data) != "intact" {
								t.Fatal("later resource changed", err)
							}
						}
						data, err := os.ReadFile(calls)
						if err != nil {
							t.Fatal(err)
						}
						if strings.Contains(string(data), "update-ref -d") || strings.Count(string(data), "worktree remove ") != 1 {
							t.Fatal("branch deleted or removal replayed", string(data))
						}
						listings := strings.Count(string(data), "worktree list --porcelain -z")
						wantListings := 1
						if scenario == "ignored-remove" || scenario == "path-residue" {
							wantListings = 0
						}
						if listings > 1 || (!allow && listings != wantListings) {
							t.Fatal("native observation repeated or skipped after removal", string(data))
						}
						// Both success and observation failure must release original branch admission.
						release, err := workspacelease.AcquireGitBranchMutation(filepath.Join(f.repo, ".git"), branch)
						if err != nil {
							t.Fatal("native outcome leaked branch admission", err)
						}
						release()
					})
				}
			}
		}
	}
}
