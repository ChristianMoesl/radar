package protocol

import (
	"fmt"
	"testing"
	"time"
)

func TestDiagnosticExpiryDestructiveDisclosureAndOrderedMemberContract(t *testing.T) {
	fixture := func() (DiagnosticExpiry, DiagnosticMaintenanceRequest) {
		v, q := expiryContractFixture()
		native := v.Proposal.Targets
		v.Proposal.Targets = append([]DiagnosticExpiryTarget{}, native[:2]...)
		v.Proposal.Targets = append(v.Proposal.Targets,
			DiagnosticExpiryTarget{ID: fmt.Sprintf("expiry-target:%064x", 1), Provider: "git", Effect: "remove_worktree_and_local_changes", BranchEffect: "delete_local_branch"},
			DiagnosticExpiryTarget{ID: fmt.Sprintf("expiry-target:%064x", 2), Provider: "git", Effect: "remove_worktree_and_local_changes", BranchEffect: "retain_local_branch"},
			DiagnosticExpiryTarget{ID: fmt.Sprintf("expiry-target:%064x", 3), Provider: "workspace", Effect: "forget_missing_worktree_registration", BranchEffect: "retain_local_branch"}, native[2])
		return v, q
	}
	v, q := fixture()
	if !ValidDiagnosticExpiry(v, q) {
		t.Fatal("complete mixed member contract refused")
	}
	changes := map[string]func(*DiagnosticExpiry){
		"missing_loss": func(v *DiagnosticExpiry) { v.Proposal.DataLoss = nil },
		"altered_loss": func(v *DiagnosticExpiry) { v.Proposal.DataLoss[0] = "files_preserved" },
		"future_deadline": func(v *DiagnosticExpiry) {
			v.Proposal.ExpiryAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
		},
		"missing_deadline":       func(v *DiagnosticExpiry) { v.Proposal.ExpiryAt = "" },
		"offset_deadline":        func(v *DiagnosticExpiry) { v.Proposal.ExpiryAt = "2026-01-01T01:00:00+01:00" },
		"zero_deadline":          func(v *DiagnosticExpiry) { v.Proposal.ExpiryAt = "0000-01-01T00:00:00Z" },
		"missing_branch_effect":  func(v *DiagnosticExpiry) { v.Proposal.Targets[2].BranchEffect = "" },
		"invented_branch_effect": func(v *DiagnosticExpiry) { v.Proposal.Targets[2].BranchEffect = "delete_remote_branch" },
		"delete_missing_branch":  func(v *DiagnosticExpiry) { v.Proposal.Targets[4].BranchEffect = "delete_local_branch" },
		"branch_on_anchor":       func(v *DiagnosticExpiry) { v.Proposal.Targets[5].BranchEffect = "retain_local_branch" },
		"duplicate_runtime": func(v *DiagnosticExpiry) {
			v.Proposal.Targets[1].Provider = "tmux"
			v.Proposal.Targets[1].Effect = v.Proposal.Targets[0].Effect
		},
		"runtime_after_git": func(v *DiagnosticExpiry) {
			v.Proposal.Targets[1], v.Proposal.Targets[2] = v.Proposal.Targets[2], v.Proposal.Targets[1]
		},
		"git_after_missing_member": func(v *DiagnosticExpiry) {
			v.Proposal.Targets[3], v.Proposal.Targets[4] = v.Proposal.Targets[4], v.Proposal.Targets[3]
		},
		"missing_after_anchor": func(v *DiagnosticExpiry) {
			v.Proposal.Targets[4], v.Proposal.Targets[5] = v.Proposal.Targets[5], v.Proposal.Targets[4]
		},
		"retirement_anchor": func(v *DiagnosticExpiry) { v.Proposal.Targets[5].Effect = "retain_workspace_files_and_registration" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			v, q := fixture()
			change(&v)
			if ValidDiagnosticExpiry(v, q) {
				t.Fatal("inconsistent destructive disclosure accepted")
			}
		})
	}
}
