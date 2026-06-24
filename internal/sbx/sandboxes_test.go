package sbx

import (
	"reflect"
	"testing"
)

func TestParseSandboxes(t *testing.T) {
	sandboxes, err := parseSandboxes(`{
  "sandboxes": [
    {
      "name": "radar-rb3ca-experience-center-DPSCAP-600-Integrate-generated-links",
      "id": "c048feba-a578-492b-baf0-895b04b6e1b3",
      "agent": "shell",
      "status": "running",
      "workspaces": [
        "/Users/me/radar/rb3ca-experience-center/DPSCAP-600-Integrate-generated-links",
        "/Users/me/.pi/agent",
        "/Users/me/.pi/agent/sessions"
      ]
    }
  ]
}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(sandboxes) != 1 {
		t.Fatalf("sandboxes = %d, want 1", len(sandboxes))
	}
	if sandboxes[0].Name != "radar-rb3ca-experience-center-DPSCAP-600-Integrate-generated-links" {
		t.Fatalf("sandbox name = %q", sandboxes[0].Name)
	}
}

func TestSandboxSourceRef(t *testing.T) {
	s := sandbox{
		Name:   "radar-rb3ca-experience-center-DPSCAP-600-Integrate-generated-links",
		ID:     "c048feba-a578-492b-baf0-895b04b6e1b3",
		Agent:  "shell",
		Status: "running",
		Workspaces: []string{
			"/Users/me/radar/rb3ca-experience-center/DPSCAP-600-Integrate-generated-links",
			"/Users/me/.pi/agent",
			"/Users/me/.pi/agent/sessions",
		},
	}

	ref := s.SourceRef()
	if ref.ID != "sbx:sandbox:"+s.Name || ref.Source != "sbx" || ref.Kind != "sandbox" {
		t.Fatalf("unexpected source ref identity: %+v", ref)
	}
	if ref.SourceLabel != "Docker sbx" || ref.Title != s.Name || ref.Status != "running" {
		t.Fatalf("unexpected source ref display fields: %+v", ref)
	}
	wantPath := "/Users/me/radar/rb3ca-experience-center/DPSCAP-600-Integrate-generated-links"
	if ref.Path != wantPath {
		t.Fatalf("path = %q, want %q", ref.Path, wantPath)
	}
	if ref.CanonicalKey != "workspace:"+wantPath {
		t.Fatalf("canonical key = %q", ref.CanonicalKey)
	}
	wantKeys := []string{"ticket:DPSCAP-600", "workspace:" + wantPath}
	if !reflect.DeepEqual(ref.LinkingKeys, wantKeys) {
		t.Fatalf("linking keys = %+v, want %+v", ref.LinkingKeys, wantKeys)
	}
	if ref.Metadata["id"] != s.ID || ref.Metadata["agent"] != "shell" || ref.Metadata["workspace_count"] != "3" {
		t.Fatalf("metadata = %+v", ref.Metadata)
	}
}

func TestPrimarySandboxWorkspaceSkipsPiAgentMount(t *testing.T) {
	workspace := primarySandboxWorkspace([]string{"/Users/me/.pi/agent", "/Users/me/.pi/agent/sessions", "/repo/worktree"})
	if workspace != "/repo/worktree" {
		t.Fatalf("workspace = %q, want /repo/worktree", workspace)
	}
}

func TestSandboxWithoutWorkspaceUsesSourceRefCanonicalKey(t *testing.T) {
	ref := sandbox{Name: "sandbox-conn-test", ID: "f263b19b"}.SourceRef()
	if ref.CanonicalKey != "sbx:sandbox:sandbox-conn-test" {
		t.Fatalf("canonical key = %q", ref.CanonicalKey)
	}
	if len(ref.LinkingKeys) != 0 {
		t.Fatalf("linking keys = %+v, want none", ref.LinkingKeys)
	}
}
