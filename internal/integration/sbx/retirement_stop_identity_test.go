package sbx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"reflect"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	sbxclient "radar/internal/integration/sbx/client"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

// The backend resolves both names and IDs, and models a name being reassigned
// after inspection but before the stop executes. No real SBX command runs.
type retirementStopIdentityRunner struct {
	runtimes       map[string]*sbxclient.Runtime
	name, scenario string
	commands       []string
	stops, reads   int
	failure        error
	cancel         context.CancelFunc
}

func (*retirementStopIdentityRunner) LookPath(string) error { return nil }
func (r *retirementStopIdentityRunner) Run(_ context.Context, _, name string, args ...string) (string, error) {
	r.commands = append(r.commands, name+" "+strings.Join(args, " "))
	switch args[0] {
	case "ls":
		r.reads++
		if r.stops != 0 && r.scenario == "post-list-error" {
			return "", r.failure
		}
		rows := []sbxclient.Runtime{}
		for _, runtime := range r.runtimes {
			rows = append(rows, *runtime)
		}
		data, err := json.Marshal(rows)
		return string(data), err
	case "stop":
		r.stops++
		if len(args) != 2 {
			return "", errors.New("unexpected fixture stop arguments")
		}
		switch r.scenario {
		case "replacement", "absent":
			delete(r.runtimes, "lifetime-1")
		case "renamed-with-replacement":
			r.runtimes["lifetime-1"].Name = "renamed-original"
		}
		if r.scenario == "replacement" || r.scenario == "renamed-with-replacement" {
			r.runtimes["replacement-1"] = &sbxclient.Runtime{ID: "replacement-1", Name: r.name, Status: "running"}
		}
		if r.scenario == "stop-error" {
			return "", r.failure
		}
		runtime := r.runtimes[args[1]]
		if runtime == nil {
			for _, candidate := range r.runtimes {
				if candidate.Name == args[1] {
					runtime = candidate
					break
				}
			}
		}
		if runtime == nil {
			return "", errors.New("fixture runtime not found")
		}
		if r.scenario != "ignored-stop" {
			runtime.Status = "stopped"
		}
		if r.scenario == "cancelled-stop" {
			r.cancel()
		}
		return "", nil
	default:
		return "", fmt.Errorf("unexpected fixture command %s", args[0])
	}
}

func TestRetirementStopAddressesOnlyInspectedLifetime(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, scenario := range []string{"stable", "replacement", "renamed-with-replacement", "absent", "stop-error", "ignored-stop", "post-list-error", "cancelled-stop", "initial-stopped", "initial-missing"} {
			t.Run(fmt.Sprintf("service=%t/%s", service, scenario), func(t *testing.T) {
				root, owner, _, target := runtimeAdmissionFixture(t)
				target.Operation["runtime_retired"] = "true"
				originalOperation := maps.Clone(target.Operation)
				registryBefore, err := os.ReadFile(workspacegroup.Path(root))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r := &retirementStopIdentityRunner{name: owner.Sandbox.Name, scenario: scenario, runtimes: map[string]*sbxclient.Runtime{"lifetime-1": {ID: "lifetime-1", Name: owner.Sandbox.Name, Status: "running", Workspaces: []string{owner.Path}}}, failure: errors.New("fixture provider failure"), cancel: cancel}
				if scenario == "initial-stopped" {
					r.runtimes["lifetime-1"].Status = "stopped"
				}
				if scenario == "initial-missing" {
					delete(r.runtimes, "lifetime-1")
				}
				var got protocol.CleanupTarget
				if service {
					var result protocol.CleanupResult
					result, err = cleanup.New([]integration.CleanupProvider{admittedFixtureSource{runner: r}}).Execute(ctx, protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{Mode: integration.CleanupRetire})
					if err == nil {
						if len(result.Targets) != 1 {
							t.Fatal("successful target missing", result)
						}
						got = result.Targets[0]
					} else if len(result.Targets) != 0 {
						t.Error("failed retirement counted as completed", result)
					}
				} else {
					got, err = retireSandbox(ctx, r, target)
				}
				noOp := strings.HasPrefix(scenario, "initial-")
				success := scenario == "stable" || noOp
				if (err == nil) != success {
					t.Errorf("err=%v wantSuccess=%t commands=%v", err, success, r.commands)
				}
				if (got.Operation["runtime_retired"] == "true") != (scenario == "stable") {
					t.Error("retirement stamp not grounded in exact observed stopped lifetime", got, err)
				}
				if scenario == "stop-error" || scenario == "post-list-error" {
					if !errors.Is(err, r.failure) {
						t.Error("native error identity lost", err)
					}
				}
				if scenario == "cancelled-stop" && !errors.Is(err, context.Canceled) {
					t.Error("cancellation lost", err)
				}
				wantStops := 1
				if noOp {
					wantStops = 0
				}
				if r.stops != wantStops || r.reads > 2 {
					t.Error("stop or observation replayed", r.commands)
				}
				for _, command := range r.commands {
					if strings.HasPrefix(command, "sbx stop ") && command != "sbx stop lifetime-1" {
						t.Error("stop addressed reusable name rather than inspected lifetime", command)
					}
					if strings.HasPrefix(command, "sbx rm ") {
						t.Error("retirement removed a runtime", command)
					}
				}
				if replacement := r.runtimes["replacement-1"]; replacement != nil && replacement.Status != "running" {
					t.Error("uninspected replacement was stopped", replacement, r.commands)
				}
				if scenario == "stable" || scenario == "renamed-with-replacement" || scenario == "post-list-error" || scenario == "cancelled-stop" {
					if original := r.runtimes["lifetime-1"]; original == nil || original.Status != "stopped" {
						t.Error("fixture did not stop the inspected lifetime", original, r.commands)
					}
				}
				if !maps.Equal(originalOperation, target.Operation) {
					t.Error("preview mutated", target)
				}
				registryAfter, readErr := os.ReadFile(workspacegroup.Path(root))
				if readErr != nil || !reflect.DeepEqual(registryBefore, registryAfter) {
					t.Fatal("registry changed", readErr)
				}
				note, readErr := os.ReadFile(owner.NotePath)
				if readErr != nil || string(note) != "canonical retained\n" {
					t.Fatal("canonical note changed", readErr)
				}
				if _, statErr := os.Stat(owner.Path); statErr != nil {
					t.Fatal("workspace lost", statErr)
				}
				assertRuntimeAdmissionReleased(t, owner)
			})
		}
	}
}
