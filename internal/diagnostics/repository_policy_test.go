package diagnostics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestRepositoryPolicyExplicitExactMemberOnly(t *testing.T) {
	store, root, path := fixture(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "private-repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "fixture", Path: path, Members: []workspacegroup.Member{{Repository: repo, Path: filepath.Join(path, "member"), Branch: "fixture"}}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	resources, err := Read(store, protocol.DiagnosticsRequest{Section: "workspace_resources", ParentID: group.ID}, false, integration.NewRegistry(workspace.Source{}))
	if err != nil || len(resources.Items) != 1 {
		t.Fatalf("resources=%+v %v", resources, err)
	}
	id := resources.Items[0].ID
	req := protocol.DiagnosticsRequest{Section: "repository_policy", ParentID: group.ID, ID: id, Workspace: path}
	if _, err := Read(store, req, false, integration.NewRegistry(workspace.Source{})); err == nil {
		t.Fatal("ordinary read accepted repository file access")
	}
	page, err := Probe(context.Background(), store, req, true, integration.NewRegistry(workspace.Source{}))
	if err != nil || page.Total != 1 || !page.Complete || page.NextCursor != "" || !page.ResourceRefreshPending || page.Items[0].ID != id || page.Evidence != repositoryPolicyEvidence || !page.Items[0].RepositoryPolicy.Available || page.Items[0].RepositoryPolicy.Evidence != "repository_configuration_absent" {
		t.Fatalf("wrong explicit evidence: %+v %v", page, err)
	}
	if page.Schema != protocol.DiagnosticsVersion || len(page.Limitations) != 7 {
		t.Fatal("missing version/limitations")
	}
	for name, mutate := range map[string]func(*protocol.DiagnosticsRequest){
		"parent": func(r *protocol.DiagnosticsRequest) { r.ParentID = "" }, "member": func(r *protocol.DiagnosticsRequest) { r.ID = "" }, "not_member": func(r *protocol.DiagnosticsRequest) { r.ID = "arbitrary-path" }, "scope": func(r *protocol.DiagnosticsRequest) { r.Workspace = "relative" }, "state": func(r *protocol.DiagnosticsRequest) { r.State = "done" }, "cursor": func(r *protocol.DiagnosticsRequest) { r.Cursor = "opaque" }, "limit": func(r *protocol.DiagnosticsRequest) { r.Limit = 51 }, "cleanup": func(r *protocol.DiagnosticsRequest) { r.CleanupMode = "manual" }, "workspace_id": func(r *protocol.DiagnosticsRequest) { r.WorkspaceID = "unrelated" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := req
			mutate(&changed)
			if _, err := Probe(context.Background(), store, changed, false, integration.NewRegistry(workspace.Source{})); err == nil {
				t.Fatal("selector accepted")
			}
		})
	}
}

type repositoryPolicyProvider struct {
	workspace.Source
	calls *int
	view  protocol.DiagnosticRepositoryPolicy
	err   error
}

func (p repositoryPolicyProvider) DiagnosticRepositoryPolicy(context.Context, string, string, string) (protocol.DiagnosticRepositoryPolicy, error) {
	*p.calls++
	return p.view, p.err
}
func (repositoryPolicyProvider) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	panic("inventory fallback")
}
func TestRepositoryPolicySanitizesUnsupportedAndProviderErrors(t *testing.T) {
	store, _, _ := fixture(t)
	id := "workspace-resource:" + strings.Repeat("a", 64)
	req := protocol.DiagnosticsRequest{Section: "repository_policy", ParentID: "registered", ID: id}
	calls := 0
	provider := repositoryPolicyProvider{calls: &calls, err: fmt.Errorf("private-parser-path-and-command")}
	for _, registry := range []integration.Registry{integration.NewRegistry(), integration.NewRegistry(workspace.Source{}, workspace.Source{}), integration.NewRegistry(provider)} {
		if _, err := Probe(context.Background(), store, req, false, registry); err == nil || strings.Contains(err.Error(), "private-parser") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	if calls != 1 {
		t.Fatal("provider fallback or call missing")
	}
	provider.err = nil
	provider.view = protocol.DiagnosticRepositoryPolicy{ID: "wrong", WorkspaceID: "registered", Origin: "current_registered_member_repository"}
	if _, err := Probe(context.Background(), store, req, false, integration.NewRegistry(provider)); err == nil {
		t.Fatal("wrong identity accepted")
	}
}

func TestRepositoryPolicyFullMemberIdentityNotDisplayPrefix(t *testing.T) {
	store, root, path := fixture(t)
	prefix := "/" + strings.Repeat("p", 600)
	members := []workspacegroup.Member{{Repository: prefix + "-one", Path: filepath.Join(path, "one"), Branch: "one"}, {Repository: prefix + "-two", Path: filepath.Join(path, "two"), Branch: "two"}}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "fixture", Path: path, Members: members}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	providers := integration.NewRegistry(workspace.Source{})
	resources, err := Read(store, protocol.DiagnosticsRequest{Section: "workspace_resources", ParentID: group.ID}, false, providers)
	if err != nil || len(resources.Items) != 2 || resources.Items[0].ID == resources.Items[1].ID {
		t.Fatalf("clipped identity collision: %+v %v", resources, err)
	}
	for _, row := range resources.Items {
		page, err := Probe(context.Background(), store, protocol.DiagnosticsRequest{Section: "repository_policy", ParentID: group.ID, ID: row.ID}, false, providers)
		if err != nil || page.Items[0].RepositoryPolicy.Reason != "repository_unavailable" {
			t.Fatalf("full member identity not resolved: %+v %v", page, err)
		}
	}
}
