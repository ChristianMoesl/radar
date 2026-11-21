package tmux

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestNativeRetirementRequiresObservedAbsence(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, scenario := range []string{"absent", "no-server", "ignored-kill", "replacement-id", "replacement-created", "replacement-server", "renamed-original", "malformed-identity", "missing-name", "native-error", "cancelled", "owner-drift", "anchor-replaced", "unrelated-update", "kill-error", "initially-missing"} {
			t.Run(fmt.Sprintf("service=%t/%s", service, scenario), func(t *testing.T) {
				root, owner, other, target, runner := retirementOverlapFixture(t)
				runner.preserveRuntime = true // This matrix controls actual post-kill observations itself.
				target.Operation["runtime_retired"] = "true"
				originalOperation := maps.Clone(target.Operation)
				original := runner.base.sessions["$7"]
				unrelated := runner.base.sessions["$8"]
				remove := func() {
					for alias, current := range runner.base.sessions {
						if current.Key() == original.Key() {
							delete(runner.base.sessions, alias)
						}
					}
				}
				if scenario == "initially-missing" {
					remove()
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				failure := errors.New("fixture native verification failure")
				killed, changed, postReads := false, false, 0
				runner.hook = func(command string) error {
					if command == "kill-session" {
						killed = true
						if scenario == "kill-error" {
							return failure
						}
						if scenario == "ignored-kill" {
							return nil
						}
						remove()
						replacement := original
						switch scenario {
						case "replacement-id":
							replacement.ID = "$9"
						case "replacement-created":
							replacement.Created = "35"
						case "replacement-server":
							replacement.ServerPID = "99"
						case "renamed-original":
							replacement.Name = "renamed"
						case "malformed-identity":
							replacement.ServerPID = "unverifiable"
						case "missing-name":
							replacement.Name = ""
						default:
							return nil
						}
						runner.base.sessions[replacement.ID] = replacement
						runner.base.sessions[replacement.Name] = replacement
					}
					if command != "display-message" || !killed {
						return nil
					}
					postReads++
					// Verification is native I/O inside the original canonical path,
					// reusable name and exact incarnation admission, not after release.
					for _, acquire := range []func() (func(), error){
						func() (func(), error) { return workspacelease.Acquire(owner.Path, false) },
						func() (func(), error) { return workspacelease.AcquireTmux("name:"+owner.SessionName, false) },
						func() (func(), error) { return workspacelease.AcquireTmux("runtime:"+original.Key(), false) },
					} {
						release, err := acquire()
						if err == nil {
							release()
							t.Error("fresh native verification escaped retained admission")
						}
					}
					switch scenario {
					case "no-server":
						return errors.New("no server running on fixture socket")
					case "native-error":
						return failure
					case "cancelled":
						cancel()
					case "anchor-replaced":
						if !changed {
							changed = true
							if err := os.Rename(owner.Path, owner.Path+".old"); err != nil {
								t.Fatal(err)
							}
							if err := os.Mkdir(owner.Path, 0755); err != nil {
								t.Fatal(err)
							}
						}
					case "owner-drift", "unrelated-update":
						if !changed {
							changed = true
							if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
								for i := range registry.Workspaces {
									g := &registry.Workspaces[i]
									if scenario == "owner-drift" && g.ID == owner.ID || scenario == "unrelated-update" && g.ID == other.ID {
										g.Model = "changed"
									}
								}
								return nil
							}); err != nil {
								t.Fatal(err)
							}
						}
					}
					return nil
				}
				got, err := executeRetirement(ctx, runner, target, service)
				noOp := scenario == "initially-missing"
				wantSuccess := scenario == "absent" || scenario == "no-server" || scenario == "unrelated-update" || noOp
				if (err == nil) != wantSuccess {
					t.Fatal("unverified native retirement outcome", got, err)
				}
				if (scenario == "kill-error" || scenario == "native-error") && !errors.Is(err, failure) {
					t.Fatal("native failure identity lost", err)
				}
				if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal("fresh cancellation lost", err)
				}
				if (postReads > 0) != (!noOp && scenario != "kill-error") {
					t.Fatal("successful command exit substituted for fresh evidence", postReads, err)
				}
				wantKills := 1
				if noOp {
					wantKills = 0
				}
				if len(runner.base.killed) != wantKills || wantKills == 1 && runner.base.killed[0] != original.ID {
					t.Fatal("kill retried or addressed a reusable/foreign target", runner.base.killed)
				}
				if (got.Operation["runtime_retired"] == "true") != (wantSuccess && !noOp) || !maps.Equal(target.Operation, originalOperation) {
					t.Fatal("retirement stamp/preview mutation not grounded in observation", got, err)
				}
				if runner.base.sessions["$8"] != unrelated {
					t.Fatal("unrelated session changed")
				}
				registry, err := workspacegroup.Load(root)
				if err != nil || len(registry.Workspaces) != 2 {
					t.Fatal("registration removed", registry, err)
				}
				for _, retained := range []string{owner.Path, other.Path} {
					if _, err := os.Stat(retained); err != nil {
						t.Fatal("workspace removed", err)
					}
				}
				note, err := os.ReadFile(owner.NotePath)
				if err != nil || string(note) != "canonical retained\n" {
					t.Fatal("canonical note changed", err)
				}
				for _, acquire := range []func() (func(), error){
					func() (func(), error) { return workspacelease.Acquire(owner.Path, true) },
					func() (func(), error) { return workspacelease.AcquireTmux("name:"+owner.SessionName, true) },
					func() (func(), error) { return workspacelease.AcquireTmux("runtime:"+original.Key(), true) },
				} {
					release, err := acquire()
					if err != nil {
						t.Fatal("admission leaked", err)
					}
					release()
				}
			})
		}
	}
}

func TestUnverifiedRetirementStopsLaterProviderDispatch(t *testing.T) {
	_, _, _, target, runner := retirementOverlapFixture(t)
	calls := 0
	service := cleanup.New([]integration.CleanupProvider{fixtureAdmissionProvider{runner: runner.base}, effectProvider{calls: &calls}})
	result, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{
		{Source: "fixture", ResourceID: "before"}, target, {Source: "fixture", ResourceID: "after"},
	}}, cleanup.ExecuteOptions{Mode: integration.CleanupRetire})
	if err == nil || !strings.Contains(err.Error(), "1 of 3 resources") || calls != 1 || len(result.Targets) != 1 || result.Targets[0].ResourceID != "before" || len(runner.base.killed) != 1 {
		t.Fatal("unverified retirement counted or later provider dispatched", result, err, calls, runner.base.killed)
	}
}
