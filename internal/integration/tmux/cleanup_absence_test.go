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

func TestOrdinaryTmuxCleanupRequiresObservedAbsence(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, managed := range []bool{false, true} {
			for _, nameOnly := range []bool{false, true} {
				scenarios := []string{"absent", "no-server", "ignored-kill", "replacement-id", "replacement-created", "replacement-server", "renamed-original", "malformed-identity", "missing-name", "native-error", "cancelled", "kill-error", "initially-missing"}
				if managed {
					scenarios = append(scenarios, "owner-drift", "anchor-replaced", "unrelated-update")
				}
				for _, scenario := range scenarios {
					t.Run(fmt.Sprintf("service=%t/managed=%t/name-only=%t/%s", service, managed, nameOnly, scenario), func(t *testing.T) {
						root, owner, other, target, runner := managedSessionFixture(t)
						if !managed {
							if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
								registry.Workspaces = []workspacegroup.Workspace{other}
								return nil
							}); err != nil {
								t.Fatal(err)
							}
						}
						if nameOnly {
							target.SourceRefID, target.ResourceID, target.Title = "", owner.SessionName, ""
						}
						preview := maps.Clone(target.Operation)
						original := runner.base.sessions["$7"]
						unrelated := runner.base.sessions["$8"]
						runner.preserveRuntime = true
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
						failure := errors.New("fixture native observation failure")
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
							admissions := []func() (func(), error){
								func() (func(), error) { return workspacelease.AcquireTmux("name:"+owner.SessionName, false) },
								func() (func(), error) { return workspacelease.AcquireTmux("runtime:"+original.Key(), false) },
							}
							if managed {
								admissions = append(admissions, func() (func(), error) { return workspacelease.Acquire(owner.Path, false) })
							}
							for _, acquire := range admissions {
								release, err := acquire()
								if err == nil {
									release()
									t.Error("post-removal verification escaped retained admission")
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
						result, err := executeManagedSession(ctx, runner, target, service)
						noOp := scenario == "initially-missing"
						success := scenario == "absent" || scenario == "no-server" || scenario == "unrelated-update" || noOp
						if (err == nil) != success {
							t.Fatal("unverified ordinary cleanup result", result, err)
						}
						if (scenario == "native-error" || scenario == "kill-error") && !errors.Is(err, failure) {
							t.Fatal("native failure identity lost", err)
						}
						if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
							t.Fatal("fresh cancellation lost", err)
						}
						if (postReads > 0) != (!noOp && scenario != "kill-error") {
							t.Fatal("command exit substituted for fresh observation", postReads, err)
						}
						wantKills := 1
						if noOp {
							wantKills = 0
						}
						if len(runner.base.killed) != wantKills || wantKills == 1 && runner.base.killed[0] != original.ID {
							t.Fatal("kill retried or reusable/foreign identity used", runner.base.killed)
						}
						if !maps.Equal(target.Operation, preview) {
							t.Fatal("native outcome mutated preview")
						}
						if runner.base.sessions["$8"] != unrelated {
							t.Fatal("unrelated runtime changed")
						}
						registry, loadErr := workspacegroup.Load(root)
						wantRegistrations := 1
						if managed {
							wantRegistrations = 2
						}
						if loadErr != nil || len(registry.Workspaces) != wantRegistrations {
							t.Fatal("registration removed", registry, loadErr)
						}
						note, readErr := os.ReadFile(owner.NotePath)
						if readErr != nil || string(note) != "canonical retained\n" {
							t.Fatal("canonical note changed", readErr)
						}
						for _, path := range []string{owner.Path, other.Path} {
							if _, err := os.Stat(path); err != nil {
								t.Fatal("workspace removed", err)
							}
						}
						for _, key := range []string{"name:" + owner.SessionName, "runtime:" + original.Key()} {
							release, err := workspacelease.AcquireTmux(key, true)
							if err != nil {
								t.Fatal("native admission leaked", err)
							}
							release()
						}
						release, leaseErr := workspacelease.Acquire(owner.Path, true)
						if leaseErr != nil {
							t.Fatal("path admission leaked", leaseErr)
						}
						release()
					})
				}
			}
		}
	}
}

func TestUnverifiedOrdinaryTmuxCleanupStopsLaterTargets(t *testing.T) {
	_, _, _, target, runner := managedSessionFixture(t)
	runner.preserveRuntime = true
	calls := 0
	result, err := cleanup.New([]integration.CleanupProvider{retirementOutcomeProvider{runner: runner}, effectProvider{calls: &calls}}).Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "fixture", ResourceID: "before"}, target, {Source: "fixture", ResourceID: "after"}}}, cleanup.ExecuteOptions{})
	if err == nil || !strings.Contains(err.Error(), "1 of 3 resources") || calls != 1 || len(result.Targets) != 1 || result.Targets[0].ResourceID != "before" || len(runner.base.killed) != 1 || result.ExecutionObservation == nil || result.ExecutionObservation.Stage != "provider_call" || result.ExecutionObservation.AttemptedTargets != 2 {
		t.Fatal("unverified tmux cleanup counted or later target dispatched", result, err, calls, runner.base.killed)
	}
}
