package diagnostics

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func workspacePolicyBoundaryFixture() protocol.DiagnosticWorkspacePolicy {
	return protocol.DiagnosticWorkspacePolicy{
		ID: "registered", Origin: "recorded_workspace_registration", SandboxRegistered: true,
		Sandbox: &protocol.DiagnosticSandboxRecipe{
			KitConfigured: true, KitPathConfigured: true, EnvFileConfigured: true, ReadyCommandConfigured: true,
			ManagedMountCount: 1, AdditionalMountCount: 2, PortCount: 3,
		},
	}
}

func TestWorkspacePolicyBoundaryRejectsNonFiniteCounts(t *testing.T) {
	values := map[string]int{"negative": -1, "minimum_integer": -int(^uint(0)>>1) - 1}
	if strconv.IntSize == 64 {
		firstUnsafe := int64(1) << 53
		values["first_unsafe"], values["maximum_integer"] = int(firstUnsafe), int(^uint(0)>>1)
	}
	for _, field := range []string{"managed_mount_count", "additional_mount_count", "port_count"} {
		for name, value := range values {
			t.Run(field+"/"+name, func(t *testing.T) {
				store, _, _ := fixture(t)
				view := workspacePolicyBoundaryFixture()
				switch field {
				case "managed_mount_count":
					view.Sandbox.ManagedMountCount = value
				case "additional_mount_count":
					view.Sandbox.AdditionalMountCount = value
				case "port_count":
					view.Sandbox.PortCount = value
				}
				calls := 0
				revision := store.Revision()
				page, err := Read(store, protocol.DiagnosticsRequest{Section: "workspace_policy", ParentID: view.ID}, true, integration.NewRegistry(policyDiagnosticProvider{calls: &calls, view: view}))
				if err == nil {
					t.Fatal("invalid provider count reached public page")
				}
				if err.Error() != "workspace recipe evidence invalid" || !reflect.DeepEqual(page, protocol.DiagnosticsPage{}) || calls != 1 || store.Revision() != revision {
					t.Fatal("refusal changed state, exposed evidence or retried the provider")
				}
			})
		}
	}
}

func TestWorkspacePolicyBoundaryPreservesFiniteShapeAndDetachesRecipe(t *testing.T) {
	cases := map[string]func(*protocol.DiagnosticWorkspacePolicy){
		"populated":  func(*protocol.DiagnosticWorkspacePolicy) {},
		"no_recipe":  func(v *protocol.DiagnosticWorkspacePolicy) { v.SandboxRegistered, v.Sandbox = false, nil },
		"zero_false": func(v *protocol.DiagnosticWorkspacePolicy) { v.Sandbox = &protocol.DiagnosticSandboxRecipe{} },
	}
	if strconv.IntSize == 64 {
		maximumSafe := int64(1<<53 - 1)
		cases["maximum_safe"] = func(v *protocol.DiagnosticWorkspacePolicy) {
			v.Sandbox.ManagedMountCount, v.Sandbox.AdditionalMountCount, v.Sandbox.PortCount = int(maximumSafe), int(maximumSafe), int(maximumSafe)
		}
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			store, _, _ := fixture(t)
			view := workspacePolicyBoundaryFixture()
			mutate(&view)
			calls := 0
			revision := store.Revision()
			page, err := Read(store, protocol.DiagnosticsRequest{Section: "workspace_policy", ParentID: view.ID}, true, integration.NewRegistry(policyDiagnosticProvider{calls: &calls, view: view}))
			if err != nil || len(page.Items) != 1 || calls != 1 || page.Total != 1 || !page.Complete || page.NextCursor != "" || !page.ResourceRefreshPending || page.Evidence != workspacePolicyEvidence || !reflect.DeepEqual(page.Items[0].WorkspacePolicy, &view) || store.Revision() != revision {
				t.Fatalf("valid finite shape changed: %v", err)
			}
			before, _ := json.Marshal(page)
			if view.Sandbox == nil {
				if strings.Contains(string(before), `"sandbox":`) {
					t.Fatal("no recipe became a fabricated empty recipe")
				}
				return
			}
			if page.Items[0].WorkspacePolicy.Sandbox == view.Sandbox {
				t.Fatal("validated recipe still aliases provider memory")
			}
			*view.Sandbox = protocol.DiagnosticSandboxRecipe{ManagedMountCount: -1, AdditionalMountCount: -1, PortCount: -1}
			after, _ := json.Marshal(page)
			if string(before) != string(after) {
				t.Fatal("late provider write changed a published page after snapshot assembly")
			}
			page.Items[0].WorkspacePolicy.Sandbox.ManagedMountCount = 99
			if view.Sandbox.ManagedMountCount != -1 {
				t.Fatal("returned page mutation changed provider state")
			}
		})
	}
}
