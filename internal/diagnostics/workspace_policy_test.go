package diagnostics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestWorkspacePolicyExactRecordedRecipe(t *testing.T) {
	for _, attached := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_sandbox", true: "recorded_sandbox"}[attached], func(t *testing.T) {
			store, root, path := fixture(t)
			private := "private-recipe-ABC-123"
			group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "completed", Path: path, Model: private, Thinking: "high", Members: []workspacegroup.Member{{Repository: filepath.Join(root, private), Path: filepath.Join(path, "member"), Branch: private}}}
			if attached {
				group.Sandbox = &workspacegroup.Sandbox{Name: private, Agent: private, KitPath: filepath.Join(root, private), EnvFile: filepath.Join(root, private), ReadyCommand: []string{private}, Mounts: []string{path}, AdditionalMounts: []workspacegroup.SandboxMount{{Path: filepath.Join(root, "additional"), ReadOnly: true}}, Ports: []workspacegroup.SandboxPort{{HostPort: 8000, SandboxPort: 9000}}}
			}
			if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(workspacegroup.Path(root))
			revision := store.Revision()
			req := protocol.DiagnosticsRequest{Section: "workspace_policy", ParentID: group.ID, Workspace: path, Limit: 1}
			page, err := read(store, req, true)
			if err != nil || page.Total != 1 || len(page.Items) != 1 || !page.Complete || page.NextCursor != "" || !page.ResourceRefreshPending {
				t.Fatalf("missing exact recipe: %+v %v", page, err)
			}
			encoded, _ := json.Marshal(page)
			if strings.Contains(string(encoded), private) || strings.Contains(string(encoded), path) || strings.Contains(string(encoded), "high") {
				t.Fatalf("private recipe leaked: %s", encoded)
			}
			var payload struct {
				Items []struct {
					ID     string `json:"id"`
					Policy struct {
						ID                string         `json:"id"`
						Origin            string         `json:"origin"`
						SandboxRegistered bool           `json:"sandbox_registered"`
						Sandbox           map[string]any `json:"sandbox"`
					} `json:"workspace_policy"`
				} `json:"items"`
			}
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatal(err)
			}
			view := payload.Items[0].Policy
			if payload.Items[0].ID != group.ID || view.ID != group.ID || view.Origin != "recorded_workspace_registration" || view.SandboxRegistered != attached {
				t.Fatalf("wrong evidence: %s", encoded)
			}
			if attached {
				expected := map[string]any{"kit_configured": true, "kit_path_configured": true, "env_file_configured": true, "ready_command_configured": true, "managed_mount_count": float64(1), "additional_mount_count": float64(1), "port_count": float64(1)}
				if !reflect.DeepEqual(view.Sandbox, expected) {
					t.Fatalf("wrong recipe: %+v", view.Sandbox)
				}
			} else if view.Sandbox != nil {
				t.Fatal("absent registered sandbox fabricated a recipe")
			}
			again, err := read(store, req, true)
			if err != nil || again.Snapshot != page.Snapshot {
				t.Fatal("public snapshot unstable")
			}
			after, _ := os.ReadFile(workspacegroup.Path(root))
			if string(before) != string(after) || revision != store.Revision() {
				t.Fatal("read mutated registration or state")
			}
			if _, err := os.Stat(group.Members[0].Repository); !os.IsNotExist(err) {
				t.Fatal("read touched missing repository")
			}
			for name, changed := range map[string]protocol.DiagnosticsRequest{
				"missing_parent": {Section: "workspace_policy"},
				"wrong_parent":   {Section: "workspace_policy", ParentID: "unknown"},
				"wrong_scope":    {Section: "workspace_policy", ParentID: group.ID, Workspace: path + "-other"},
				"relative_scope": {Section: "workspace_policy", ParentID: group.ID, Workspace: "relative"},
				"state":          {Section: "workspace_policy", ParentID: group.ID, State: "done"},
				"row":            {Section: "workspace_policy", ParentID: group.ID, ID: group.ID},
				"cursor":         {Section: "workspace_policy", ParentID: group.ID, Cursor: "opaque"},
				"limit":          {Section: "workspace_policy", ParentID: group.ID, Limit: 51},
				"cleanup":        {Section: "workspace_policy", ParentID: group.ID, CleanupMode: "expired"},
			} {
				t.Run(name, func(t *testing.T) {
					if _, err := read(store, changed, false); err == nil {
						t.Fatal("inapplicable or out-of-scope selector accepted")
					}
				})
			}
			for name, providers := range map[string]integration.Registry{"missing": integration.NewRegistry(), "ambiguous": integration.NewRegistry(workspace.Source{}, workspace.Source{})} {
				t.Run(name, func(t *testing.T) {
					if _, err := Read(store, req, false, providers); err == nil {
						t.Fatal("unavailable evidence reported a recipe")
					}
				})
			}
		})
	}
}

func TestWorkspacePolicyUnavailableDoesNotLeakOrInferAbsence(t *testing.T) {
	store, root, path := fixture(t)
	private := "private-broken-registration-ABC-123"
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"{bad " + private, `{"version":2,"workspaces":[{"id":"duplicate","path":"` + path + `"},{"id":"duplicate","path":"` + path + `"}]}`} {
		if err := os.WriteFile(workspacegroup.Path(root), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		page, err := read(store, protocol.DiagnosticsRequest{Section: "workspace_policy", ParentID: workspacegroup.ID(path)}, false)
		if err == nil || len(page.Items) != 0 || strings.Contains(err.Error(), private) || strings.Contains(err.Error(), path) {
			t.Fatalf("unsafe unavailable evidence: %+v %v", page, err)
		}
	}
}

// This provider deliberately panics if any fallback inventory or production
// callback is used. Only its exact registered-policy capability is allowed.
type policyDiagnosticProvider struct {
	workspace.Source
	calls *int
	view  protocol.DiagnosticWorkspacePolicy
	err   error
}

func (p policyDiagnosticProvider) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	panic("policy read must not enumerate fallback inventory")
}
func (p policyDiagnosticProvider) DiagnosticWorkspacePolicy(id, scope string) (protocol.DiagnosticWorkspacePolicy, error) {
	*p.calls++
	return p.view, p.err
}

func TestWorkspacePolicyCapabilityAndEvidenceBoundaries(t *testing.T) {
	store, _, _ := fixture(t)
	calls := 0
	provider := policyDiagnosticProvider{calls: &calls, view: protocol.DiagnosticWorkspacePolicy{ID: "registered", Origin: "recorded_workspace_registration"}}
	req := protocol.DiagnosticsRequest{Section: "workspace_policy", ParentID: "registered"}
	page, err := Read(store, req, false, integration.NewRegistry(provider))
	if err != nil || len(page.Items) != 1 || calls != 1 {
		t.Fatalf("exact capability unavailable: %+v %v calls=%d", page, err, calls)
	}
	valid := provider.view
	for name, mutate := range map[string]func(*protocol.DiagnosticWorkspacePolicy){
		"wrong_identity":      func(v *protocol.DiagnosticWorkspacePolicy) { v.ID = "other" },
		"wrong_origin":        func(v *protocol.DiagnosticWorkspacePolicy) { v.Origin = "guessed_repository_file" },
		"missing_recipe":      func(v *protocol.DiagnosticWorkspacePolicy) { v.SandboxRegistered = true },
		"unregistered_recipe": func(v *protocol.DiagnosticWorkspacePolicy) { v.Sandbox = &protocol.DiagnosticSandboxRecipe{} },
		"negative_count": func(v *protocol.DiagnosticWorkspacePolicy) {
			v.SandboxRegistered = true
			v.Sandbox = &protocol.DiagnosticSandboxRecipe{PortCount: -1}
		},
	} {
		t.Run(name, func(t *testing.T) {
			provider.view = valid
			mutate(&provider.view)
			if _, err := Read(store, req, false, integration.NewRegistry(provider)); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
	provider.view = valid
	calls = 0
	for _, changed := range []protocol.DiagnosticsRequest{
		{Section: "workspace_policy"}, {Section: "workspace_policy", ParentID: "registered", State: "done"},
		{Section: "workspace_policy", ParentID: "registered", ID: "row"}, {Section: "workspace_policy", ParentID: "registered", Cursor: "opaque"},
		{Section: "workspace_policy", ParentID: "registered", Limit: -1}, {Section: "workspace_policy", ParentID: "registered", Limit: 51},
		{Section: "workspace_policy", ParentID: strings.Repeat("x", 513)}, {Section: "workspace_policy", ParentID: "registered", Workspace: "relative"},
		{Section: "workspace_policy", ParentID: "registered", Workspace: "/" + strings.Repeat("x", 4096)},
		{Section: "workspace_policy", ParentID: "registered", CleanupMode: "expired"}, {Section: "workspace_policy", ParentID: "registered", WorkspaceID: "other"},
	} {
		if _, err := Read(store, changed, false, integration.NewRegistry(provider)); err == nil {
			t.Fatal("selector accepted")
		}
	}
	if calls != 0 {
		t.Fatalf("bad selector invoked reader: %d", calls)
	}
}

func TestWorkspacePolicySnapshotIsOnlyPublicShape(t *testing.T) {
	store, root, path := fixture(t)
	group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "completed", Path: path, Sandbox: &workspacegroup.Sandbox{Name: "private-runtime", Agent: "shell", EnvFile: filepath.Join(root, "private-env-a"), ReadyCommand: []string{"private-command-a"}}}
	save := func() {
		t.Helper()
		if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
			t.Fatal(err)
		}
	}
	save()
	req := protocol.DiagnosticsRequest{Section: "workspace_policy", ParentID: group.ID}
	first, err := read(store, req, false)
	if err != nil {
		t.Fatal(err)
	}
	group.Sandbox.EnvFile = filepath.Join(root, "private-env-b")
	group.Sandbox.ReadyCommand = []string{"private-command-b"}
	save()
	second, err := read(store, req, false)
	if err != nil || second.Snapshot != first.Snapshot {
		t.Fatal("private-only values became a public recipe attestation")
	}
	group.Sandbox.ReadyCommand = nil
	save()
	third, err := read(store, req, false)
	if err != nil || third.Snapshot == second.Snapshot {
		t.Fatal("public recipe drift not reflected")
	}
}
