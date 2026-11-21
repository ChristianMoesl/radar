package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
)

type outcomeBoundaryProvider struct {
	name, path string
	calls      int
}

func (p *outcomeBoundaryProvider) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: p.name}
}
func (p *outcomeBoundaryProvider) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	return []protocol.CleanupTarget{{Source: p.name, Kind: "fixture", Path: p.path}}, nil
}
func (p *outcomeBoundaryProvider) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	p.calls++
	return req.Target, os.Remove(p.path)
}

func TestGitRemovalOutcomeFailureRetainsRegistrationAndStopsBundle(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed, integration.CleanupExpired} {
		for _, service := range []bool{false, true} {
			for _, scenario := range []string{"ignored_worktree", "ignored_branch", "path_residue"} {
				t.Run(fmt.Sprintf("%d/service=%t/%s", mode, service, scenario), func(t *testing.T) {
					f := newCleanupExpiryFixture(t, "feature")
					t.Setenv("RADAR_SOCKET", filepath.Join(f.home, "run", "radar.sock"))
					t.Setenv("RADAR_AGENT_ROOT", filepath.Join(f.home, "agents"))
					note := filepath.Join(f.home, "canonical.md")
					if err := os.WriteFile(note, []byte("preserved canonical note\n"), 0600); err != nil {
						t.Fatal(err)
					}
					f.group.NotePath = note
					f.save(t)
					target := f.preview(t, mode)
					if target.Operation["delete_branch"] != "feature" {
						t.Fatal("fixture did not enter managed branch remover", target)
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
					case "ignored_worktree":
						script += "case \"$*\" in *'worktree remove '*) exit 0;; esac\n"
					case "ignored_branch":
						script += "case \"$*\" in 'update-ref -d refs/heads/feature '*) exit 0;; esac\n"
					case "path_residue":
						script += fmt.Sprintf("case \"$*\" in *'worktree remove '*) %q \"$@\" || exit $?; mkdir %q; exit $?;; esac\n", real, f.path)
					}
					script += fmt.Sprintf("exec %q \"$@\"\n", real)
					if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
					t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
					before, later := &outcomeBoundaryProvider{name: "before", path: filepath.Join(f.home, "before")}, &outcomeBoundaryProvider{name: "later", path: filepath.Join(f.home, "later")}
					for _, p := range []*outcomeBoundaryProvider{before, later} {
						if err := os.WriteFile(p.path, []byte("intact"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if service {
						result, executeErr := cleanup.New([]integration.CleanupProvider{before, Source{}, later}).Execute(f.ctx, protocol.CleanupPreview{TaskID: 7, Targets: []protocol.CleanupTarget{{Source: "before", Kind: "fixture", Path: before.path}, target, {Source: "later", Kind: "fixture", Path: later.path}}}, cleanup.ExecuteOptions{Mode: mode})
						if executeErr == nil || len(result.Targets) != 1 || result.Targets[0].Source != "before" || before.calls != 1 || later.calls != 0 || result.ExecutionObservation == nil || result.ExecutionObservation.Stage != "provider_call" || result.ExecutionObservation.AttemptedTargets != 2 {
							t.Fatalf("unverified Git result escaped ordered partial failure: %+v, %v; calls=%d/%d", result, executeErr, before.calls, later.calls)
						}
					} else {
						result, executeErr := (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: mode})
						if executeErr == nil || result.Source != "" {
							t.Fatalf("unverified Git removal reported success: %+v, %v", result, executeErr)
						}
					}
					after, gotHead := f.snapshot(t)
					if string(after) != string(registry) || gotHead != head {
						t.Fatal("failed observation removed registration or changed retained branch", gotHead, head)
					}
					if data, err := os.ReadFile(note); err != nil || string(data) != "preserved canonical note\n" {
						t.Fatal("canonical note changed", err)
					}
					if data, err := os.ReadFile(later.path); err != nil || string(data) != "intact" {
						t.Fatal("later resource changed", err)
					}
					if _, err := os.Stat(f.repo); err != nil {
						t.Fatal("primary repository removed", err)
					}
					if scenario == "ignored_worktree" {
						if _, err := os.Stat(filepath.Join(f.path, "README.md")); err != nil {
							t.Fatal("original worktree changed", err)
						}
					}
					data, err := os.ReadFile(calls)
					if err != nil {
						t.Fatal(err)
					}
					deletes := strings.Count(string(data), "update-ref -d refs/heads/feature ")
					want := 0
					if scenario == "ignored_branch" {
						want = 1
					}
					if deletes != want || strings.Count(string(data), "worktree remove ") != 1 {
						t.Fatal("mutation retried or branch deleted without absent worktree", string(data))
					}
				})
			}
		}
	}
}
