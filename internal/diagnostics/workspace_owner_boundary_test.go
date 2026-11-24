package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"radar/internal/agentservice"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func workspaceInventoryOwner(t *testing.T, id, workspace string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reg := agentservice.Registration{Version: 1, ID: id, Entrypoint: "private-command", Workspace: protocol.AgentWorkspaceBinding{ID: "registered", Path: workspace, Mode: "local"}}
	data, _ := json.Marshal(reg)
	file := filepath.Join(dir, "registration.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"snapshot.json", "collection.json", "schedules.json", "ingress.token"} {
		if err := syscall.Mkfifo(filepath.Join(dir, name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return file
}

func TestWorkspaceInventoryOwnerReadBoundary(t *testing.T) {
	for _, scenario := range []string{"valid", "missing_root", "unregistered_directory", "exact_bound", "over_bound", "symlink", "dangling_symlink", "root_symlink", "relative", "malformed", "aggregate", "entries"} {
		t.Run(scenario, func(t *testing.T) {
			store, root, path := fixture(t)
			group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "retained", Path: path, Members: []workspacegroup.Member{{Path: path + "/repo", Repository: root + "/source", Branch: "main"}}}
			if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
				t.Fatal(err)
			}
			file := ""
			known, count := true, 0
			if scenario != "missing_root" {
				workspaceInventoryOwner(t, "owner-a", path)
				file = workspaceInventoryOwner(t, "owner-z", path+"/nested")
				count = 2
			}
			switch scenario {
			case "unregistered_directory":
				if err := os.Mkdir(filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "removed"), 0700); err != nil {
					t.Fatal(err)
				}
			case "exact_bound", "over_bound":
				data, _ := os.ReadFile(file)
				bound := 64 * 1024
				if scenario == "over_bound" {
					bound++
					known = false
				}
				if err := os.WriteFile(file, append(data, bytes.Repeat([]byte(" "), bound-len(data))...), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink", "dangling_symlink":
				known = false
				target := filepath.Join(t.TempDir(), "private-registration")
				if err := os.Rename(file, target); err != nil {
					t.Fatal(err)
				}
				if scenario == "dangling_symlink" {
					if err := os.Remove(target); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, file); err != nil {
					t.Fatal(err)
				}
			case "root_symlink":
				known = false
				agents := os.Getenv("RADAR_AGENT_ROOT")
				if err := os.Rename(agents, agents+"-target"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(agents+"-target", agents); err != nil {
					t.Fatal(err)
				}
			case "relative":
				known = false
				data, _ := json.Marshal(agentservice.Registration{Version: 1, ID: "owner-z", Workspace: protocol.AgentWorkspaceBinding{ID: "registered", Path: "private-relative"}})
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				known = false
				if err := os.WriteFile(file, []byte("private-invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "aggregate":
				known = false
				for i := range 20 {
					f := workspaceInventoryOwner(t, fmt.Sprintf("other-%02d", i), path+"-unrelated")
					data, _ := os.ReadFile(f)
					if err := os.WriteFile(f, append(data, bytes.Repeat([]byte(" "), 60*1024-len(data))...), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "entries":
				known = false
				for i := range 1024 {
					if err := os.Mkdir(filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), fmt.Sprintf("removed-%04d", i)), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			var before []byte
			if info, err := os.Lstat(file); err == nil && info.Mode().IsRegular() {
				before, _ = os.ReadFile(file)
			}
			revision := store.Revision()
			for _, section := range []string{"workspaces", "workspace_resources"} {
				page, err := read(store, protocol.DiagnosticsRequest{Section: section, Workspace: path}, true)
				if err != nil || !page.ResourceRefreshPending || !page.Complete {
					t.Fatal("ownership refusal lost inventory", err)
				}
				if strings.HasSuffix(page.Evidence, "agent_ownership_unavailable") == known {
					t.Error("unsafe registration or exceeded budget became known owner evidence")
				}
				if section == "workspaces" {
					if len(page.Items) != 1 {
						t.Fatal("workspace lost")
					}
					v := page.Items[0].Workspace
					if known {
						if v.AgentOwnerCount == nil || *v.AgentOwnerCount != count || v.AgentOwnershipEvidence != "fresh_registration" || len(v.AgentOwners) != count {
							t.Error("valid stopped owners/default zero changed")
						}
						if count > 0 && v.ExecutionEligibility != "blocked_registered_agent" {
							t.Error("stopped owner lost hold")
						}
					} else if v.AgentOwnerCount != nil || v.AgentOwnershipEvidence != "unavailable" || len(v.AgentOwners) != 0 || v.ExecutionEligibility != "unknown_planner_not_run" {
						t.Error("unavailable ownership became zero, partial owners or permission")
					}
				} else {
					want := 1
					if known {
						want += count
					}
					if page.Total != want {
						t.Error("unsafe owner rows exposed or unrelated member lost")
					}
				}
				raw, _ := json.Marshal(page)
				if strings.Contains(string(raw), "private-") {
					t.Error("private registration detail exposed")
				}
			}
			if store.Revision() != revision {
				t.Fatal("inventory mutated store")
			}
			if before != nil {
				after, _ := os.ReadFile(file)
				if !bytes.Equal(before, after) {
					t.Fatal("registration changed")
				}
			}
			if scenario == "missing_root" {
				if _, err := os.Stat(os.Getenv("RADAR_AGENT_ROOT")); !os.IsNotExist(err) {
					t.Fatal("inventory created absent registry")
				}
			}
		})
	}
}

func TestWorkspaceInventoryOwnersDoNotWaitForFIFO(t *testing.T) {
	if scenario := os.Getenv("RADAR_TEST_OWNER_INVENTORY_FIFO"); scenario != "" {
		store, root, path := fixture(t)
		group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "one", Path: path}
		if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
			t.Fatal(err)
		}
		file := workspaceInventoryOwner(t, "owner", path)
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(file, 0600); err != nil {
			t.Fatal(err)
		}
		req := protocol.DiagnosticsRequest{Section: scenario, Workspace: path}
		if scenario == "outside_scope" {
			req.Section, req.Workspace = "workspaces", path+"-other"
		}
		if scenario == "unknown_parent" {
			req.Section, req.ParentID = "workspace_resources", "missing"
		}
		// Store initialization under race instrumentation is unrelated to owner
		// inspection. Signal readiness before applying the exact read deadline.
		if err := os.WriteFile(os.Getenv("RADAR_TEST_OWNER_INVENTORY_READY"), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		page, err := read(store, req, false)
		if err != nil {
			t.Fatal("FIFO refusal lost registration", err)
		}
		if scenario == "outside_scope" || scenario == "unknown_parent" {
			if page.Total != 0 || strings.HasSuffix(page.Evidence, "agent_ownership_unavailable") {
				t.Fatal("unselected workspace triggered owner scan")
			}
		} else if !strings.HasSuffix(page.Evidence, "agent_ownership_unavailable") {
			t.Fatal("FIFO became usable owner evidence")
		}
		return
	}
	for _, scenario := range []string{"workspaces", "workspace_resources", "outside_scope", "unknown_parent"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ready := filepath.Join(t.TempDir(), "ready")
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkspaceInventoryOwnersDoNotWaitForFIFO$", "-test.count=1")
			cmd.Env = append(os.Environ(), "RADAR_TEST_OWNER_INVENTORY_FIFO="+scenario, "RADAR_TEST_OWNER_INVENTORY_READY="+ready)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			var deadline <-chan time.Time
			for {
				select {
				case err := <-done:
					if ctx.Err() != nil {
						t.Fatalf("child fixture startup exceeded its bound; killed/collected: %s", output.Bytes())
					}
					if err != nil {
						t.Fatalf("child inventory: %s", output.Bytes())
					}
					if _, err := os.Stat(ready); err != nil {
						t.Fatal("child did not reach owner read")
					}
					return
				case <-tick.C:
					if deadline == nil {
						if _, err := os.Stat(ready); err == nil {
							timer := time.NewTimer(4 * time.Second)
							defer timer.Stop()
							deadline = timer.C
						}
					}
				case <-deadline:
					cancel()
					<-done
					t.Fatal("workspace ownership inventory blocked on FIFO; child killed and collected")
				}
			}
		})
	}
}

func TestWorkspaceInventoryOwnerEvidenceChangeInvalidatesCursor(t *testing.T) {
	store, root, path := fixture(t)
	group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "one", Path: path, Members: []workspacegroup.Member{{Path: path + "/repo", Repository: root + "/source", Branch: "main"}}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	file := workspaceInventoryOwner(t, "owner", path)
	req := protocol.DiagnosticsRequest{Section: "workspace_resources", Workspace: path, ParentID: group.ID, Limit: 1}
	page, err := read(store, req, false)
	if err != nil || page.NextCursor == "" || page.Total != 2 {
		t.Fatal("valid owners not paginated", err)
	}
	data, _ := os.ReadFile(file)
	if err := os.WriteFile(file, append(data, bytes.Repeat([]byte(" "), 64*1024)...), 0600); err != nil {
		t.Fatal(err)
	}
	req.Cursor = page.NextCursor
	if _, err := read(store, req, false); err == nil {
		t.Fatal("unsafe owner evidence reused previous cursor")
	}
}
