package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/agentservice"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func recreationOwner(t *testing.T, path string, desired bool) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "private-owner")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(agentservice.Registration{Version: 1, ID: "private-owner", Desired: desired, Entrypoint: "PRIVATE-command", Workspace: protocol.AgentWorkspaceBinding{ID: "private-backing", Path: path, Mode: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "registration.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestDiagnosticRecreationFreshOwnershipAndLeaseEvidence(t *testing.T) {
	for _, owner := range []string{"none", "stopped", "desired", "nested", "sibling", "unavailable"} {
		for _, lease := range []string{"missing", "available", "contended", "unavailable"} {
			t.Run(owner+"/"+lease, func(t *testing.T) {
				withWorkspaceGOOS(t, "darwin")
				t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
				socket := filepath.Join(t.TempDir(), "run", "radar.sock")
				t.Setenv("RADAR_SOCKET", socket)
				req, groups, runner := recreationFixture(t, 1)
				g := groups[0]
				count := 0
				if owner != "none" {
					path := g.Path
					if owner == "nested" {
						path = filepath.Join(path, "nested")
					}
					if owner == "sibling" {
						path += "-sibling"
					} else {
						count = 1
					}
					file := recreationOwner(t, path, owner == "desired")
					if owner == "unavailable" {
						if err := os.WriteFile(file, []byte("PRIVATE-invalid"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				if lease == "available" || lease == "contended" {
					release, err := workspacelease.Acquire(g.Path, false)
					if err != nil {
						t.Fatal(err)
					}
					if lease == "available" {
						release()
					} else {
						defer release()
					}
				}
				if lease == "unavailable" {
					if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(filepath.Dir(socket), "leases"), []byte("PRIVATE"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				registryBefore, _ := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
				view, err := diagnosticRecreationPreview(context.Background(), runner, req.WorkspaceRoot, g.ID, g.Path)
				if err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(view)
				var fields map[string]any
				if err := json.Unmarshal(data, &fields); err != nil {
					t.Fatal(err)
				}
				if owner == "unavailable" {
					if fields["ownership_evidence"] != "unavailable" || fields["agent_owner_count"] != nil {
						t.Errorf("unavailable ownership became empty: %s", data)
					}
				} else if fields["ownership_evidence"] != "fresh_bounded_registration" || fields["agent_owner_count"] != float64(count) {
					t.Errorf("missing native owner evidence: %s", data)
				}
				wantLease := map[string]string{"missing": "unknown_no_lock_file", "available": "exclusive_available_at_probe", "contended": "contended_at_probe", "unavailable": "unavailable"}[lease]
				if fields["lease_evidence"] != wantLease || view.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" || view.Status != "recreate" {
					t.Errorf("lease observation became execution policy: %s", data)
				}
				if lease == "missing" {
					if _, err := os.Lstat(filepath.Dir(socket)); !os.IsNotExist(err) {
						t.Errorf("probe created coordination directory: %v", err)
					}
				}
				if lease == "available" {
					release, err := workspacelease.Acquire(g.Path, true)
					if err != nil {
						t.Fatal("probe retained admission", err)
					}
					release()
				}
				for _, private := range []string{"PRIVATE", "private-owner", g.Path, g.Sandbox.Name} {
					if strings.Contains(string(data), private) {
						t.Fatal("private evidence leaked", string(data))
					}
				}
				registryAfter, _ := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
				if !reflect.DeepEqual(registryBefore, registryAfter) {
					t.Fatal("registration changed")
				}
				assertNoRecreationMutation(t, runner)
			})
		}
	}
}

func TestDiagnosticRecreationRejectsObservedOwnershipOrLeaseDrift(t *testing.T) {
	for _, change := range []string{"new owner", "same count", "removed owner", "owner unavailable", "owner available", "new empty registry", "new lease", "contended lease", "released lease", "replaced lease inode", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			withWorkspaceGOOS(t, "darwin")
			t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
			socket := filepath.Join(t.TempDir(), "run", "radar.sock")
			t.Setenv("RADAR_SOCKET", socket)
			req, groups, runner := recreationFixture(t, 1)
			g := groups[0]
			ownerFile := ""
			if change == "same count" || change == "removed owner" || change == "owner unavailable" || change == "owner available" {
				ownerFile = recreationOwner(t, g.Path, false)
			}
			if change == "owner available" {
				if err := os.WriteFile(ownerFile, []byte("PRIVATE-invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var held func()
			if change == "released lease" || change == "contended lease" || change == "replaced lease inode" {
				release, err := workspacelease.Acquire(g.Path, false)
				if err != nil {
					t.Fatal(err)
				}
				if change == "released lease" {
					held = release
				} else {
					release()
				}
			}
			defer func() {
				if held != nil {
					held()
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fired := false
			runner.beforeRun = func(args []string) {
				if args[0] != "policy" || fired {
					return
				}
				fired = true
				switch change {
				case "new owner":
					recreationOwner(t, g.Path, false)
				case "same count":
					recreationOwner(t, filepath.Join(g.Path, "nested"), false)
				case "removed owner":
					if err := os.Remove(ownerFile); err != nil {
						t.Fatal(err)
					}
				case "owner unavailable":
					if err := os.WriteFile(ownerFile, []byte("PRIVATE-invalid"), 0600); err != nil {
						t.Fatal(err)
					}
				case "owner available":
					recreationOwner(t, g.Path, false)
				case "new empty registry":
					if err := os.MkdirAll(os.Getenv("RADAR_AGENT_ROOT"), 0700); err != nil {
						t.Fatal(err)
					}
				case "new lease", "contended lease":
					release, err := workspacelease.Acquire(g.Path, false)
					if err != nil {
						t.Fatal(err)
					}
					if change == "contended lease" {
						held = release
					} else {
						release()
					}
				case "released lease":
					held()
					held = nil
				case "replaced lease inode":
					files, err := filepath.Glob(filepath.Join(filepath.Dir(socket), "leases", "*.lock"))
					if err != nil || len(files) != 1 {
						t.Fatal("missing lease fixture", files, err)
					}
					if err := os.Rename(files[0], files[0]+".retained"); err != nil {
						t.Fatal(err)
					}
					release, err := workspacelease.Acquire(g.Path, false)
					if err != nil {
						t.Fatal(err)
					}
					release()
				case "cancelled":
					cancel()
				}
			}
			view, err := diagnosticRecreationPreview(ctx, runner, req.WorkspaceRoot, g.ID, "")
			if !fired || err == nil || view.ID != "" || strings.Contains(err.Error(), "PRIVATE") {
				t.Errorf("drift accepted: %+v %v", view, err)
			}
			assertNoRecreationMutation(t, runner)
		})
	}
}

func TestDiagnosticRecreationDisplayIdentityBindsPrivateOwnershipEvidence(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "run", "radar.sock"))
	req, groups, runner := recreationFixture(t, 1)
	g := groups[0]
	recreationOwner(t, g.Path, false)
	read := func() protocol.DiagnosticRecreationPreview {
		v, err := diagnosticRecreationPreview(context.Background(), runner, req.WorkspaceRoot, g.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before, again := read(), read()
	if !reflect.DeepEqual(before, again) {
		t.Fatal("stable observations changed proposal")
	}
	recreationOwner(t, filepath.Join(g.Path, "nested"), false)
	after := read()
	if before.PreviewID == after.PreviewID {
		t.Error("same-count private owner change did not change display identity")
	}
	after.PreviewID = before.PreviewID
	if !reflect.DeepEqual(before, after) {
		t.Fatal("private owner payload escaped in public shape")
	}
	assertNoRecreationMutation(t, runner)
}

func TestDiagnosticRecreationDisplayIdentityBindsLeaseInode(t *testing.T) {
	withWorkspaceGOOS(t, "darwin")
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
	socket := filepath.Join(t.TempDir(), "run", "radar.sock")
	t.Setenv("RADAR_SOCKET", socket)
	req, groups, runner := recreationFixture(t, 1)
	g := groups[0]
	createLease := func() {
		release, err := workspacelease.Acquire(g.Path, false)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	read := func() protocol.DiagnosticRecreationPreview {
		view, err := diagnosticRecreationPreview(context.Background(), runner, req.WorkspaceRoot, g.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	createLease()
	before, again := read(), read()
	if !reflect.DeepEqual(before, again) {
		t.Fatal("stable lease observations changed proposal")
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(socket), "leases", "*.lock"))
	if err != nil || len(files) != 1 {
		t.Fatal("missing lease fixture", files, err)
	}
	if err := os.Rename(files[0], files[0]+".retained"); err != nil {
		t.Fatal(err)
	}
	createLease()
	after := read()
	if before.PreviewID == after.PreviewID {
		t.Fatal("same-status inode replacement did not change display identity")
	}
	after.PreviewID = before.PreviewID
	if !reflect.DeepEqual(before, after) {
		t.Fatal("private lease identity escaped in public shape")
	}
	assertNoRecreationMutation(t, runner)
}
