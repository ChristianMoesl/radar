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
	"radar/internal/protocol"
)

func TestCleanupPreviewRegisteredOwnersNativeParityAcrossBundle(t *testing.T) {
	for _, scenario := range []string{"stopped", "nested", "sibling", "unavailable", "ambiguous_path", "folded_workspace", "relative", "no-path"} {
		t.Run(scenario, func(t *testing.T) {
			store, _, path := fixture(t)
			store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
			records, _, _ := store.InspectionSnapshot()
			provider, registration := manualPreviewFixture(t, 2)
			provider.targets[0].Path = path
			provider.targets[1].Path = path + "-other-workspace"
			ownerPath := provider.targets[1].Path
			if scenario == "nested" {
				ownerPath = filepath.Join(ownerPath, "nested")
			}
			if scenario == "sibling" {
				ownerPath += "-sibling"
			}
			file := retirementOwner(t, ownerPath) // Deliberately stopped; still an owner.
			if scenario == "unavailable" {
				if err := os.WriteFile(file, []byte("private-invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "ambiguous_path" || scenario == "folded_workspace" {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "ambiguous_path" {
					value, _ := json.Marshal(ownerPath)
					other, _ := json.Marshal(ownerPath + "-private-unrelated")
					data = []byte(strings.Replace(string(data), `"path":`+string(value), `"path":`+string(value)+`,"path":`+string(other), 1))
				} else {
					data = []byte(strings.Replace(string(data), `"workspace":`, `"WORKSPACE":`, 1))
				}
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "relative" {
				provider.targets[1].Path = "private-relative"
			}
			if scenario == "no-path" {
				provider.targets[1].Path = ""
			}
			page, err := Probe(context.Background(), store, protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: records[0].ID, Workspace: path}, false, integration.NewRegistry(provider, registration))
			if err != nil || len(page.Items) != 2 {
				t.Fatalf("page=%+v %v", page, err)
			}
			known := scenario != "unavailable" && scenario != "relative" && scenario != "ambiguous_path" && scenario != "folded_workspace"
			native, nativeErr := agentservice.WorkspaceRemovalOwners([]string{provider.targets[0].Path, provider.targets[1].Path})
			if known && nativeErr != nil {
				t.Fatal(nativeErr)
			}
			for _, row := range page.Items {
				v := row.CleanupPreview
				// Native target operation hashes keep private identities opaque; target
				// effects match, so collect each expected count rather than guessing order.
				if !known {
					if v.OwnershipEvidence != "unavailable" || v.AgentOwnerCount != nil || v.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" {
						t.Fatalf("unavailable became permission: %+v", v)
					}
				} else if v.OwnershipEvidence == "fresh_bounded_registration" {
					if v.AgentOwnerCount == nil {
						t.Fatal("known owner count absent")
					}
					if *v.AgentOwnerCount > 0 && v.ExecutionEligibility != "blocked_registered_agent" {
						t.Fatal("owner did not block")
					}
					if *v.AgentOwnerCount == 0 && v.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" {
						t.Fatal("known zero became execution permission")
					}
				} else if scenario != "no-path" || v.OwnershipEvidence != "not_checked_no_target_path" || v.AgentOwnerCount != nil || v.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" {
					t.Fatalf("unexpected evidence %+v", v)
				}
			}
			if known {
				expected, observed := 0, 0
				for _, target := range provider.targets {
					if target.Path != "" {
						expected += len(native[target.Path])
					}
				}
				for _, row := range page.Items {
					if row.CleanupPreview.AgentOwnerCount != nil {
						observed += *row.CleanupPreview.AgentOwnerCount
					}
				}
				if expected != observed {
					t.Fatalf("native owners=%d diagnostic owners=%d", expected, observed)
				}
			}
			raw, _ := json.Marshal(page)
			if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), path) {
				t.Fatalf("private owner data leaked: %s", raw)
			}
		})
	}
}

func TestCleanupPreviewRejectsOwnerDriftDuringInspectionAndPagination(t *testing.T) {
	for _, scenario := range []string{"new registry", "same count", "removed", "became unavailable", "became available"} {
		for _, during := range []bool{false, true} {
			t.Run(scenario+map[bool]string{false: " between pages", true: " during inspection"}[during], func(t *testing.T) {
				store, _, path := fixture(t)
				store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
				records, _, _ := store.InspectionSnapshot()
				provider, registration := manualPreviewFixture(t, 2)
				for i := range provider.targets {
					provider.targets[i].Path = path
				}
				file := ""
				if scenario != "new registry" {
					file = retirementOwner(t, path)
				}
				if scenario == "became available" {
					if err := os.WriteFile(file, []byte("private-invalid"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				mutate := func() {
					switch scenario {
					case "new registry":
						retirementOwner(t, path+"-unrelated") // Same zero count.
					case "same count":
						retirementOwner(t, filepath.Join(path, "nested"))
					case "removed":
						if err := os.Remove(file); err != nil {
							t.Fatal(err)
						}
					case "became unavailable":
						if err := os.WriteFile(file, []byte("private-invalid"), 0600); err != nil {
							t.Fatal(err)
						}
					case "became available":
						retirementOwner(t, path)
					}
				}
				req := protocol.DiagnosticsRequest{Section: "cleanup_preview", ParentID: records[0].ID, Limit: 1}
				registry := integration.NewRegistry(provider, registration)
				if during {
					n := 0
					registration.observe = func() {
						n++
						if n == 2 {
							mutate()
						}
					}
				} else {
					first, err := Probe(context.Background(), store, req, false, registry)
					if err != nil || first.NextCursor == "" {
						t.Fatal("missing first page", err)
					}
					req.Cursor = first.NextCursor
					mutate()
				}
				page, err := Probe(context.Background(), store, req, false, registry)
				if err == nil || len(page.Items) > 0 || strings.Contains(err.Error(), "private-") {
					t.Fatalf("changed owners yielded usable/private evidence: %+v %v", page, err)
				}
			})
		}
	}
}
