package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/protocol"
)

// Inspect the wire property so an exact previous runtime/protocol source overlay
// can run these tests without a new field or API scaffold.
func runtimeRegisteredOwnerCount(t *testing.T, view *protocol.DiagnosticRuntime) (float64, bool) {
	t.Helper()
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	count, ok := object["agent_owner_count"].(float64)
	return count, ok
}

func TestRuntimeRegisteredOwnersFreshNativeParityAndPrivacy(t *testing.T) {
	for _, scenario := range []string{"none", "stopped", "nested", "sibling", "multiple", "invalid", "symlink", "oversized_registry", "owner_appeared", "same_count_changed", "became_invalid", "became_valid"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, path := fixture(t)
			reg := &probeRegistrationFixture{registrations: []protocol.DiagnosticWorkspace{{ID: "registered", Path: path, SandboxName: "sandbox", SessionName: "session"}}}
			ownerPath := path
			if scenario == "nested" {
				ownerPath = filepath.Join(path, "nested")
			} else if scenario == "sibling" {
				ownerPath += "-sibling"
			}
			file := ""
			if scenario != "none" && scenario != "owner_appeared" {
				file = retirementOwner(t, ownerPath) // Desired=false must still count.
			}
			if scenario == "invalid" || scenario == "became_valid" {
				if err := os.WriteFile(file, []byte("private-provider-body token=private-token"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "symlink" {
				if err := os.Rename(file, file+".preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(file+".preserved", file); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "multiple" {
				dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "second")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(agentservice.Registration{Version: 1, ID: "second", Entrypoint: "private-entrypoint", Desired: true, Workspace: protocol.AgentWorkspaceBinding{ID: "private-workspace", Path: path, Mode: "local"}})
				if err := os.WriteFile(filepath.Join(dir, "registration.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "oversized_registry" {
				for i := range 1024 {
					if err := os.Mkdir(filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), fmt.Sprintf("extra-%04d", i)), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			calls := []string{}
			sbx := runtimeProbeFixture{source: "sbx", calls: &calls, view: protocol.DiagnosticRuntimeObservation{Presence: "present", State: "running", ID: "private-lifetime"}}
			switch scenario {
			case "owner_appeared", "became_valid":
				sbx.run = func(context.Context) { retirementOwner(t, path) }
			case "same_count_changed":
				sbx.run = func(context.Context) {
					data, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, []byte(strings.Replace(string(data), "private-command", "changed-private-command", 1)), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "became_invalid":
				sbx.run = func(context.Context) {
					if err := os.WriteFile(file, []byte("private-invalid"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			tmux := runtimeProbeFixture{source: "tmux", calls: &calls, view: protocol.DiagnosticRuntimeObservation{Presence: "absent"}}
			beforeRecords, beforeRefs, revision := store.InspectionSnapshot()
			page, err := Probe(context.Background(), store, protocol.DiagnosticsRequest{Section: "runtime_probe", ParentID: "registered"}, false, integration.NewRegistry(reg, sbx, tmux))
			if err != nil || len(page.Items) != 2 || len(calls) != 2 {
				t.Fatal("native runtime probe unavailable", err, calls)
			}
			known := scenario == "none" || scenario == "stopped" || scenario == "nested" || scenario == "sibling" || scenario == "multiple"
			for _, row := range page.Items {
				view := row.Runtime
				count, present := runtimeRegisteredOwnerCount(t, view)
				if known {
					native, _, err := agentservice.DiagnosticWorkspaceRemovalOwners(context.Background(), []string{path})
					if err != nil || !present || count != float64(native[path]) || view.OwnershipEvidence != "fresh_bounded_registration" {
						t.Fatal("fresh registered owners differ from native stopped-owner predicate", scenario, view.OwnershipEvidence, count, present)
					}
				} else if present || view.OwnershipEvidence != "unavailable" {
					t.Fatal("unavailable, partial or changed registrations became a fresh owner count", scenario)
				}
				if view.LeaseEvidence != "unknown_not_checked" || (view.Provider == "sbx" && view.Observation.Presence != "present") || (view.Provider == "tmux" && view.Observation.Presence != "absent") {
					t.Fatal("registered-owner evidence invented lease safety or rewrote independent runtime presence")
				}
			}
			raw, _ := json.Marshal(page)
			for _, private := range []string{path, "private-", "changed-private", "registration.json", "entrypoint"} {
				if strings.Contains(string(raw), private) {
					t.Fatal("raw registration, native lifetime or private evidence escaped")
				}
			}
			afterRecords, afterRefs, afterRevision := store.InspectionSnapshot()
			if revision != afterRevision || !reflect.DeepEqual(beforeRecords, afterRecords) || !reflect.DeepEqual(beforeRefs, afterRefs) {
				t.Fatal("explicit read changed task publication")
			}
		})
	}
}

func TestRuntimeRegisteredOwnersCancellationAndPagination(t *testing.T) {
	for _, scenario := range []string{"provider_cancellation", "final_registration_cancellation", "owner_count_changes_between_pages", "no_matching_runtime", "final_registration_owner_drift"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, path := fixture(t)
			reg := &finalRuntimeRegistrationFixture{probeRegistrationFixture: &probeRegistrationFixture{registrations: []protocol.DiagnosticWorkspace{{ID: "registered", Path: path, SandboxName: "sandbox", SessionName: "session"}}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := []string{}
			sbx := runtimeProbeFixture{source: "sbx", calls: &calls, view: protocol.DiagnosticRuntimeObservation{Presence: "present", ID: "private-lifetime"}}
			tmux := runtimeProbeFixture{source: "tmux", calls: &calls, view: protocol.DiagnosticRuntimeObservation{Presence: "absent"}}
			if scenario == "provider_cancellation" {
				sbx.run = func(context.Context) { cancel() }
			} else if scenario == "final_registration_cancellation" {
				reg.final = cancel
			} else if scenario == "final_registration_owner_drift" {
				reg.final = func() { retirementOwner(t, path) }
			}
			request := protocol.DiagnosticsRequest{Section: "runtime_probe", ParentID: "registered", Limit: 1}
			if scenario == "no_matching_runtime" {
				request.ID = "runtime:" + strings.Repeat("0", 64)
			}
			providers := integration.NewRegistry(reg, sbx, tmux)
			page, err := Probe(ctx, store, request, false, providers)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "no_matching_runtime" {
				if page.Total != 0 || len(calls) != 0 {
					t.Fatal("unknown runtime selector expanded native scope")
				}
				return
			}
			if len(page.Items) != 1 || page.NextCursor == "" {
				t.Fatal("paged fixture unavailable")
			}
			if scenario == "owner_count_changes_between_pages" {
				retirementOwner(t, path)
				request.Cursor = page.NextCursor
				if _, err := Probe(ctx, store, request, false, providers); err == nil {
					t.Fatal("changed public registered-owner count reused a stale page")
				}
				return
			}
			for _, row := range page.Items {
				if _, present := runtimeRegisteredOwnerCount(t, row.Runtime); present || row.Runtime.OwnershipEvidence != "unavailable" || (scenario != "final_registration_owner_drift" && row.Runtime.Observation.Presence != "unknown") {
					t.Fatal("late cancellation retained ownership or runtime observations")
				}
			}
		})
	}
}
