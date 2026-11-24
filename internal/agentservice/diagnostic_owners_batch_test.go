package agentservice

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiagnosticWorkspaceOwnersBatchMatchesNativeGuard(t *testing.T) {
	root := filepath.Join(t.TempDir(), "agents")
	t.Setenv("RADAR_AGENT_ROOT", root)
	workspace := filepath.Join(t.TempDir(), "workspace")
	diagnosticRegistration(t, root, "owner", workspace)
	diagnosticRegistration(t, root, "nested", filepath.Join(workspace, "nested"))
	diagnosticRegistration(t, root, "sibling", workspace+"-sibling")
	paths := []string{workspace, filepath.Join(workspace, "nested"), workspace + "-sibling", workspace + "-absent", workspace}
	native, err := WorkspaceRemovalOwners(paths)
	if err != nil {
		t.Fatal(err)
	}
	counts, fingerprint, err := DiagnosticWorkspaceRemovalOwners(context.Background(), paths)
	if err != nil || fingerprint == [32]byte{} || len(counts) != 4 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}
	for path, owners := range native {
		if counts[path] != len(owners) {
			t.Fatalf("path count %d, native %d", counts[path], len(owners))
		}
	}
	ids, err := DiagnosticWorkspaceOwnerIDs(context.Background(), paths)
	if err != nil || !reflect.DeepEqual(ids, native) {
		t.Fatal("bounded owner IDs differ from native stopped/nested/sibling relations", err)
	}
	ids[workspace][0] = "changed-returned-id"
	fresh, err := DiagnosticWorkspaceOwnerIDs(context.Background(), paths)
	if err != nil || !reflect.DeepEqual(fresh, native) {
		t.Fatal("returned owner IDs changed later inspection", err)
	}
	many := make([]string, 512)
	for i := range many {
		many[i] = fmt.Sprintf("%s/%d", workspace, i)
	}
	bounded, same, err := DiagnosticWorkspaceRemovalOwners(context.Background(), many)
	if err != nil || len(bounded) != 512 || fingerprint != same {
		t.Fatal("batch did not retain a single stable registry fingerprint", err)
	}
	for _, count := range bounded {
		if count != 0 {
			t.Fatal("unrelated target received an owner")
		}
	}
	// A malformed selector anywhere makes the entire batch unavailable, not a
	// partial map whose absent keys could be mistaken for known zero owners.
	for _, invalid := range [][]string{nil, {workspace, "private-relative"}, append(many, workspace)} {
		counts, fingerprint, err := DiagnosticWorkspaceRemovalOwners(context.Background(), invalid)
		if err == nil || counts != nil || !reflect.DeepEqual(fingerprint, [32]byte{}) {
			t.Fatalf("invalid batch returned usable evidence: %v %v", counts, err)
		}
		ids, err := DiagnosticWorkspaceOwnerIDs(context.Background(), invalid)
		if err == nil || ids != nil {
			t.Fatal("invalid batch returned partial owner IDs")
		}
	}
}
