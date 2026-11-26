package sbx

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

// A successful stop exit is deliberately independent of the observed runtime.
// These fixtures never invoke a real sandbox backend.
type retirementOutcomeRunner struct {
	initial, after string
	commands       []string
	stops, reads   int
	stopErr        error
	afterRead      func() error
}

func (*retirementOutcomeRunner) LookPath(string) error { return nil }
func (r *retirementOutcomeRunner) Run(_ context.Context, _, name string, args ...string) (string, error) {
	r.commands = append(r.commands, name+" "+strings.Join(args, " "))
	switch args[0] {
	case "ls":
		r.reads++
		if r.stops == 0 {
			return r.initial, nil
		}
		if r.afterRead != nil {
			if err := r.afterRead(); err != nil {
				return "", err
			}
		}
		return r.after, nil
	case "stop":
		r.stops++
		return "", r.stopErr
	default:
		return "", fmt.Errorf("unexpected fixture command %q", args[0])
	}
}

func TestRegisteredSandboxRetirementRequiresObservedOutcome(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, scenario := range []string{
			"stopped", "uppercase-stopped", "still-running", "unknown-state", "missing-state",
			"replacement", "missing", "duplicate", "aliased-target-id", "duplicate-foreign-id", "duplicate-foreign-name", "malformed", "missing-identity", "inspect-error",
			"foreign-mount", "read-only-mount", "cancelled", "owner-drift", "anchor-replaced",
			"unrelated-update", "stop-error", "initial-stopped", "initial-missing",
		} {
			t.Run(fmt.Sprintf("service=%t/%s", service, scenario), func(t *testing.T) {
				root, owner, other, target := runtimeAdmissionFixture(t)
				target.Operation["runtime_retired"] = "true" // Never trust an input result stamp.
				original := maps.Clone(target.Operation)
				before, err := workspacegroup.Load(root)
				if err != nil {
					t.Fatal(err)
				}
				row := func(id, status, mount string) string {
					return fmt.Sprintf(`[{"id":%q,"name":%q,"status":%q,"workspaces":[%q]}]`, id, owner.Sandbox.Name, status, mount)
				}
				runner := &retirementOutcomeRunner{initial: row("lifetime-1", "running", owner.Path), after: row("lifetime-1", "stopped", owner.Path)}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				failure := errors.New("fixture native failure")
				verified := false
				runner.afterRead = func() error {
					verified = true
					// The fresh observation is inside both direct and Service's retained
					// path/name bundle, not after releasing native execution admission.
					for _, acquire := range []func() (func(), error){
						func() (func(), error) { return workspacelease.Acquire(owner.Path, false) },
						func() (func(), error) { return workspacelease.AcquireSandbox(owner.Sandbox.Name, false) },
					} {
						release, err := acquire()
						if err == nil {
							release()
							t.Error("retirement observation escaped retained admission")
						}
					}
					switch scenario {
					case "inspect-error":
						return failure
					case "cancelled":
						cancel()
					case "anchor-replaced":
						if err := os.Rename(owner.Path, owner.Path+".old"); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(owner.Path, 0755); err != nil {
							t.Fatal(err)
						}
					case "owner-drift", "unrelated-update":
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
					return nil
				}
				switch scenario {
				case "uppercase-stopped":
					runner.after = row("lifetime-1", "STOPPED", owner.Path)
				case "still-running":
					runner.after = row("lifetime-1", "running", owner.Path)
				case "unknown-state":
					runner.after = row("lifetime-1", "stopping", owner.Path)
				case "missing-state":
					runner.after = row("lifetime-1", "", owner.Path)
				case "replacement":
					runner.after = row("lifetime-2", "stopped", owner.Path)
				case "missing":
					runner.after = "[]"
				case "duplicate":
					runner.after = strings.TrimSuffix(runner.after, "]") + "," + strings.TrimPrefix(runner.after, "[")
				case "aliased-target-id":
					runner.after = strings.TrimSuffix(runner.after, "]") + `,{"name":"private-alias","id":"lifetime-1"}]`
				case "duplicate-foreign-id":
					runner.after = strings.TrimSuffix(runner.after, "]") + `,{"name":"private-other","id":"two"},{"name":"private-another","id":"two"}]`
				case "duplicate-foreign-name":
					runner.after = strings.TrimSuffix(runner.after, "]") + `,{"name":"private-other","id":"two"},{"name":"private-other","id":"three"}]`
				case "malformed":
					runner.after = "unparseable"
				case "missing-identity":
					runner.after = row("", "stopped", owner.Path)
				case "foreign-mount":
					runner.after = row("lifetime-1", "stopped", other.Path)
				case "read-only-mount":
					runner.after = row("lifetime-1", "stopped", owner.Path+":ro")
				case "stop-error":
					runner.stopErr = failure
				case "initial-stopped":
					runner.initial = runner.after
				case "initial-missing":
					runner.initial = "[]"
				}
				var got protocol.CleanupTarget
				if service {
					var result protocol.CleanupResult
					result, err = cleanup.New([]integration.CleanupProvider{admittedFixtureSource{runner: runner}}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: integration.CleanupRetire})
					if err == nil {
						if len(result.Targets) != 1 {
							t.Fatal("successful provider result missing", result)
						}
						got = result.Targets[0]
					} else if len(result.Targets) != 0 {
						t.Fatal("unverified runtime reported completed", result)
					}
				} else {
					got, err = retireSandbox(ctx, runner, target)
				}
				noOp := strings.HasPrefix(scenario, "initial-")
				wantSuccess := scenario == "stopped" || scenario == "uppercase-stopped" || scenario == "unrelated-update" || noOp
				if (err == nil) != wantSuccess {
					t.Fatalf("outcome err=%v, wantSuccess=%t, commands=%v", err, wantSuccess, runner.commands)
				}
				if (scenario == "inspect-error" || scenario == "stop-error") && !errors.Is(err, failure) {
					t.Fatal("native error identity lost", err)
				}
				if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal("cancelled observation reported success", err)
				}
				wantVerified := !noOp && scenario != "stop-error"
				wantCommands := []string{"sbx ls --json"}
				if !noOp {
					wantCommands = append(wantCommands, "sbx stop lifetime-1")
				}
				if wantVerified {
					wantCommands = append(wantCommands, "sbx ls --json")
				}
				if verified != wantVerified || !reflect.DeepEqual(runner.commands, wantCommands) {
					t.Fatal("missing/broadened native observation or effect", verified, runner.commands, wantCommands)
				}
				if (got.Operation["runtime_retired"] == "true") != (wantSuccess && !noOp) {
					t.Fatal("retirement stamp not grounded in exact stopped runtime", got, err)
				}
				if !maps.Equal(original, target.Operation) {
					t.Fatal("provider mutated caller's preview")
				}
				after, err := workspacegroup.Load(root)
				if err != nil {
					t.Fatal(err)
				}
				if scenario != "owner-drift" && scenario != "unrelated-update" && !reflect.DeepEqual(before, after) {
					t.Fatal("retirement changed registry")
				}
				for _, retained := range []string{owner.Path, other.Path, owner.NotePath} {
					if _, err := os.Stat(retained); err != nil {
						t.Fatal("preserved resource lost", retained, err)
					}
				}
				note, err := os.ReadFile(owner.NotePath)
				if err != nil || string(note) != "canonical retained\n" {
					t.Fatal("canonical note changed", err)
				}
				assertRuntimeAdmissionReleased(t, owner)
				if matches, _ := filepath.Glob(owner.Path + "/*"); len(matches) != 0 {
					t.Fatal("retirement recreated workspace contents", matches)
				}
			})
		}
	}
}
