package diagnostics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
)

func retirementOwner(t *testing.T, path string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "private-owner")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reg := agentservice.Registration{Version: 1, ID: "private-owner", Desired: false, Entrypoint: "private-command", Workspace: protocol.AgentWorkspaceBinding{ID: "private-workspace", Path: path, Mode: "local"}}
	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "registration.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestRetirementPreviewRegisteredOwnersRemainBlockingAndUnavailableStaysUnknown(t *testing.T) {
	for _, scenario := range []string{"stopped", "nested", "sibling", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, group, _ := retirementFixture(t)
			path := group.Path
			if scenario == "nested" {
				path = filepath.Join(path, "nested")
			} else if scenario == "sibling" {
				path += "-sibling"
			}
			file := retirementOwner(t, path)
			if scenario == "unavailable" {
				if err := os.WriteFile(file, []byte("private-invalid-registration"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			page, err := Probe(context.Background(), store, protocol.DiagnosticsRequest{Section: "retirement_preview", ParentID: group.ID}, false, integration.NewRegistry(workspace.Source{}))
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("preview=%+v %v", page, err)
			}
			view := page.Items[0].RetirementPreview
			if scenario == "unavailable" {
				if view.AgentOwnerCount != nil || view.OwnershipEvidence != "unavailable" || view.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" {
					t.Fatalf("unavailable ownership became empty or permission: %+v", view)
				}
			} else {
				native, err := agentservice.WorkspaceRemovalOwners([]string{group.Path})
				if err != nil || view.AgentOwnerCount == nil || *view.AgentOwnerCount != len(native[group.Path]) || view.OwnershipEvidence != "fresh_bounded_registration" {
					t.Fatalf("native owner parity lost: %+v %v", view, err)
				}
				want := "blocked_registered_agent"
				if scenario == "sibling" {
					want = "unknown_fresh_execution_guards_not_run"
				}
				if view.ExecutionEligibility != want {
					t.Fatalf("wrong ownership block: %+v", view)
				}
			}
			data, _ := json.Marshal(page)
			if strings.Contains(string(data), "private-") || (scenario == "unavailable" && strings.Contains(string(data), "agent_owner_count")) {
				t.Fatalf("private or unavailable count escaped: %s", data)
			}
		})
	}
}

func TestRetirementPreviewRejectsRegisteredOwnerDriftIncludingSameCount(t *testing.T) {
	for _, scenario := range []string{"new registry", "same count", "removed", "became unavailable", "became available"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, group, _ := retirementFixture(t)
			file := ""
			if scenario != "new registry" {
				file = retirementOwner(t, group.Path)
			}
			if scenario == "became available" {
				if err := os.WriteFile(file, []byte("private-invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			provider := &retirementPlannerFixture{}
			provider.run = func(_ context.Context, call int, targets []protocol.CleanupTarget) ([]protocol.CleanupTarget, error) {
				if call == 2 {
					switch scenario {
					case "new registry":
						retirementOwner(t, group.Path+"-sibling") // Zero owners before and after.
					case "same count":
						retirementOwner(t, filepath.Join(group.Path, "nested"))
					case "removed":
						if err := os.Remove(file); err != nil {
							t.Fatal(err)
						}
					case "became unavailable":
						if err := os.WriteFile(file, []byte("private-invalid"), 0600); err != nil {
							t.Fatal(err)
						}
					case "became available":
						retirementOwner(t, group.Path)
					}
				}
				return targets, nil
			}
			page, err := Probe(context.Background(), store, protocol.DiagnosticsRequest{Section: "retirement_preview", ParentID: group.ID}, false, integration.NewRegistry(provider))
			if err == nil || len(page.Items) != 0 || strings.Contains(err.Error(), "private-") {
				t.Fatalf("owner drift returned usable/private evidence: %+v %v", page, err)
			}
		})
	}
}
