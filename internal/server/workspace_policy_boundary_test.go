package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

type boundaryWorkspacePolicySource struct {
	repositoryDiagnosticSource
	view protocol.DiagnosticWorkspacePolicy
}

func (p boundaryWorkspacePolicySource) DiagnosticWorkspacePolicy(parent, scope string) (protocol.DiagnosticWorkspacePolicy, error) {
	*p.calls++
	if parent != "registered" || scope != "/registered-scope" {
		panic("workspace policy target or scope widened")
	}
	return p.view, nil
}

func TestWorkspacePolicyRPCPreservesFiniteNativeBoundary(t *testing.T) {
	type policyCase struct {
		recipe *protocol.DiagnosticSandboxRecipe
		valid  bool
	}
	cases := map[string]policyCase{
		"no_recipe":  {nil, true},
		"zero_false": {&protocol.DiagnosticSandboxRecipe{}, true},
		"negative":   {&protocol.DiagnosticSandboxRecipe{ManagedMountCount: -1}, false},
	}
	if strconv.IntSize == 64 {
		firstUnsafe := int64(1) << 53
		cases["maximum_safe"] = policyCase{&protocol.DiagnosticSandboxRecipe{ManagedMountCount: int(firstUnsafe - 1), AdditionalMountCount: int(firstUnsafe - 1), PortCount: int(firstUnsafe - 1)}, true}
		for _, field := range []string{"managed", "additional", "ports"} {
			recipe := &protocol.DiagnosticSandboxRecipe{}
			switch field {
			case "managed":
				recipe.ManagedMountCount = int(firstUnsafe)
			case "additional":
				recipe.AdditionalMountCount = int(firstUnsafe)
			case "ports":
				recipe.PortCount = int(firstUnsafe)
			}
			cases[field+"_unsafe"] = policyCase{recipe, false}
		}
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			store, err := state.NewStore(logger)
			if err != nil {
				t.Fatal(err)
			}
			calls, effects := 0, 0
			view := protocol.DiagnosticWorkspacePolicy{ID: "registered", Origin: "recorded_workspace_registration", SandboxRegistered: test.recipe != nil, Sandbox: test.recipe}
			provider := boundaryWorkspacePolicySource{repositoryDiagnosticSource: repositoryDiagnosticSource{calls: &calls, effects: &effects}, view: view}
			s := New(store, logger, func() { effects++ }, func() error { effects++; return nil }, nil, integration.NewRegistry(provider), cleanup.New(nil))
			req := protocol.DiagnosticsRequest{Section: "workspace_policy", ParentID: view.ID, Workspace: "/registered-scope"}
			revision := store.Revision()
			response := managedPipeCall(t, s, protocol.Request{Method: "diagnostics", Diagnostics: &req})
			encoded, err := json.Marshal(response)
			if err != nil || calls != 1 || effects != 0 || store.Revision() != revision || strings.Contains(string(encoded), "/registered-scope") {
				t.Fatal("RPC changed state, leaked scope, fell back or retried")
			}
			if !test.valid {
				if response.OK || response.Diagnostics != nil || strings.Contains(string(encoded), "managed_mount_count") {
					t.Fatal("RPC published nonfinite provider evidence")
				}
				return
			}
			page := response.Diagnostics
			if !response.OK || page == nil || page.Schema != protocol.DiagnosticsVersion || page.Total != 1 || !page.Complete || len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0].WorkspacePolicy, &view) {
				t.Fatal("valid native policy changed in serialization")
			}
		})
	}
}
