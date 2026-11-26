package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspacegc"
)

// The executor and lifecycle guard are production code; providers here are
// controlled observations, not a claim of live resource removal/absence.
type expiryMemberProvider struct {
	fakeCleanupSource
	targets         []protocol.CleanupTarget
	seen            []protocol.CleanupTarget
	previews, calls int
	failAt          int
	hook            func()
}

func (p *expiryMemberProvider) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "git", CleanupOrder: 2}
}
func (p *expiryMemberProvider) PreviewCleanup(_ context.Context, q integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	if q.Mode != integration.CleanupExpired {
		panic("expiry mode replaced")
	}
	p.previews++
	return p.targets, nil
}
func (p *expiryMemberProvider) AdmitCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	return p, func() {}, nil
}
func (p *expiryMemberProvider) PreflightCleanup(context.Context, []protocol.CleanupTarget, integration.CleanupMode) error {
	return nil
}
func (p *expiryMemberProvider) Cleanup(_ context.Context, q integration.CleanupRequest) (protocol.CleanupTarget, error) {
	if q.Mode != integration.CleanupExpired {
		panic("expiry mode replaced")
	}
	p.calls++
	p.seen = append(p.seen, q.Target)
	if p.hook != nil {
		p.hook()
	}
	if p.calls == p.failAt {
		return q.Target, errors.New("PRIVATE member failure")
	}
	return q.Target, nil
}
func expiryMembersFixture(t *testing.T) (*Server, *expiryAdapterSource, *expiryMemberProvider, protocol.DiagnosticMaintenanceRequest, *bool) {
	t.Helper()
	s, p, q, pending := expiryAdapterFixture(t)
	plan, e := workspacegc.BuildPlan(s.store, time.Now(), workspacegc.Options{WorkspaceRoot: p.root})
	if e != nil {
		t.Fatal(e)
	}
	task := plan.Candidates[0].Task
	git := &expiryMemberProvider{}
	for i := 0; i < 3; i++ {
		path := filepath.Join(p.groups[0].Path, fmt.Sprintf("member-%d", i))
		repo := filepath.Join(p.root, fmt.Sprintf("PRIVATE-repository-%d", i))
		p.groups[0].Members = append(p.groups[0].Members, protocol.DiagnosticMember{Path: path, Repository: repo})
		if i == 2 {
			continue
		}
		ref := protocol.SourceRef{ID: "git:worktree:" + path, EntityID: "git:worktree:" + path, Source: "git", Kind: "worktree", Path: path, Repo: repo, Branch: fmt.Sprintf("PRIVATE-branch-%d", i), WorkspaceID: "origin", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkspace, LinkingKeys: []string{"mark:ABC-123"}}
		task.SourceRefs = append(task.SourceRefs, ref)
		operation := map[string]string{"repository": repo}
		if i == 0 {
			operation["delete_branch"] = ref.Branch
		}
		git.targets = append(git.targets, protocol.CleanupTarget{Source: "git", Kind: "worktree", SourceRefID: ref.ID, WorkspaceID: "origin", Path: path, ResourceID: path, Branch: ref.Branch, Operation: operation, Safety: []protocol.CleanupSafety{{Kind: "local_changes", Message: "PRIVATE dirty data", BlocksAutomatic: true, Expires: true}}})
	}
	missing := p.groups[0].Members[2]
	p.targetsHook = func(targets []protocol.CleanupTarget) []protocol.CleanupTarget {
		return append([]protocol.CleanupTarget{{Source: "workspace", Kind: "missing_member", SourceRefID: "workspace:origin", WorkspaceID: "origin", Path: missing.Path, ResourceID: missing.Path}}, targets...)
	}
	s.store.SetTasks([]protocol.Task{task})
	s.integrations = integration.NewRegistry(git, p)
	s.cleanupService = cleanup.New(s.integrations.CleanupProviders())
	return s, p, git, q, pending
}
func TestDiagnosticExpiryCompleteOrderedMembersAndReturnedPrefix(t *testing.T) {
	for _, failAt := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			s, p, git, q, pending := expiryMembersFixture(t)
			git.failAt = failAt
			v, e := s.diagnosticExpiry(context.Background(), q)
			if e != nil || !protocol.ValidDiagnosticExpiry(v, q) || len(v.Proposal.Targets) != 4 || p.calls != 0 || git.calls != 0 || p.guards != 0 || *pending {
				t.Fatal(v, e)
			}
			if got := []string{v.Proposal.Targets[0].BranchEffect, v.Proposal.Targets[1].BranchEffect, v.Proposal.Targets[2].BranchEffect}; !reflect.DeepEqual(got, []string{"delete_local_branch", "retain_local_branch", "retain_local_branch"}) {
				t.Fatal(got)
			}
			if v.Proposal.Targets[2].Effect != "forget_missing_worktree_registration" || v.Proposal.Targets[0].AutomaticBlockerCount != 0 || v.Proposal.Targets[0].SafetyNoticeCount != 1 {
				t.Fatal(v.Proposal)
			}
			raw, _ := json.Marshal(v)
			if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), p.root) || strings.Contains(string(raw), "delete_branch") {
				t.Fatal("private member evidence escaped")
			}
			// Later provider buffers and returned disclosure cannot rewrite the retained
			// full bundle or skip one of its native calls.
			original := git.targets[0].Operation["delete_branch"]
			git.targets[0].Operation["delete_branch"] = "PRIVATE replacement"
			v.Proposal.DataLoss[0] = "PRIVATE replacement"
			q.Mode, q.PlanID = "apply", v.PlanID
			result, e := s.diagnosticExpiry(context.Background(), q)
			completed, calls, wantWorkspace := 4, 2, 2
			if failAt > 0 {
				completed = failAt - 1
				calls = failAt
				wantWorkspace = 0
			}
			if e != nil || !protocol.ValidDiagnosticExpiry(result, q) || result.CompletedTargets == nil || *result.CompletedTargets != completed || git.calls != calls || p.calls != wantWorkspace || p.guards != 1 || !*pending || git.seen[0].Operation["delete_branch"] != original || git.previews != 1 || p.previews != 1 {
				t.Fatal(result, e, git.calls, p.calls, p.guards)
			}
			if _, e := s.diagnosticExpiry(context.Background(), q); e == nil {
				t.Fatal("member bundle replayed")
			}
		})
	}
}
func TestDiagnosticExpiryRefusesIncompleteOrUnregisteredMemberDisclosure(t *testing.T) {
	for _, kind := range []string{"missing_git", "duplicate_git", "foreign_path", "foreign_repository", "foreign_ref", "branch_drift", "delete_branch_drift", "missing_member_duplicate", "missing_member_ref", "registered_member_outside_anchor", "unregistered_runtime", "missing_runtime", "structural_blocker", "automatic_notice_unknown", "too_many_notices"} {
		t.Run(kind, func(t *testing.T) {
			s, p, git, q, pending := expiryMembersFixture(t)
			switch kind {
			case "missing_git":
				git.targets = git.targets[1:]
			case "duplicate_git":
				git.targets = append(git.targets, git.targets[0])
			case "foreign_path":
				git.targets[0].Path = filepath.Join(p.root, "foreign")
			case "foreign_repository":
				git.targets[0].Operation["repository"] = filepath.Join(p.root, "foreign")
			case "foreign_ref":
				git.targets[0].SourceRefID = "foreign"
			case "branch_drift":
				git.targets[0].Branch = "foreign"
			case "delete_branch_drift":
				git.targets[0].Operation["delete_branch"] = "foreign"
			case "missing_member_duplicate":
				old := p.targetsHook
				p.targetsHook = func(v []protocol.CleanupTarget) []protocol.CleanupTarget {
					v = old(v)
					return append([]protocol.CleanupTarget{v[0]}, v...)
				}
			case "missing_member_ref":
				old := p.targetsHook
				p.targetsHook = func(v []protocol.CleanupTarget) []protocol.CleanupTarget {
					v = old(v)
					v[0].SourceRefID = "foreign"
					return v
				}
			case "registered_member_outside_anchor":
				p.groups[0].Members[0].Path = filepath.Join(p.root, "foreign")
			case "unregistered_runtime":
				old := p.targetsHook
				p.targetsHook = func(v []protocol.CleanupTarget) []protocol.CleanupTarget {
					return append([]protocol.CleanupTarget{{Source: "tmux", Kind: "session", Path: p.groups[0].Path, ResourceID: "foreign"}}, old(v)...)
				}
			case "missing_runtime":
				p.groups[0].SandboxName = "PRIVATE missing"
			case "structural_blocker":
				git.targets[0].Safety[0].Expires = false
				git.targets[0].Safety[0].Kind = "protected_checkout"
			case "automatic_notice_unknown":
				git.targets[0].Safety[0] = protocol.CleanupSafety{Kind: "private_structural", Message: "PRIVATE unknown", BlocksAutomatic: true}
			case "too_many_notices":
				git.targets[0].Safety = make([]protocol.CleanupSafety, 1025)
			}
			if _, e := s.diagnosticExpiry(context.Background(), q); e == nil || len(s.cleanupPlans) != 0 || git.calls != 0 || p.calls != 0 || p.guards != 0 || *pending {
				t.Fatal("incomplete unsafe bundle issued/executed", e)
			}
		})
	}
}
func TestDiagnosticExpiryNeverEscalatesEarlierStageOrIncompleteNoop(t *testing.T) {
	for _, age := range []time.Duration{0, 2 * 24 * time.Hour, 6 * 24 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			s, p, q, _ := expiryAdapterFixture(t)
			plan, _ := workspacegc.BuildPlan(s.store, time.Now(), workspacegc.Options{WorkspaceRoot: p.root})
			task := plan.Candidates[0].Task
			task.SourceRefs[0].Metadata["completed_at"] = time.Now().Add(-age).UTC().Format(time.RFC3339)
			task.DoneAt = task.SourceRefs[0].Metadata["completed_at"]
			s.store.SetTasks([]protocol.Task{task})
			if _, e := s.diagnosticExpiry(context.Background(), q); e == nil || p.previews != 0 || p.guards != 0 || p.calls != 0 || len(s.cleanupPlans) != 0 {
				t.Fatal("expiry escalated retention stage", e)
			}
		})
	}
}
func TestDiagnosticExpirySharesLatestPerTaskPlanWithManualAndRetirement(t *testing.T) {
	for _, replacement := range []string{"manual", "retirement"} {
		t.Run(replacement, func(t *testing.T) {
			s, p, q, _ := expiryAdapterFixture(t)
			v, e := s.diagnosticExpiry(context.Background(), q)
			if e != nil {
				t.Fatal(e)
			}
			plan := s.cleanupPlans[v.Proposal.TaskID]
			var preview protocol.CleanupPreview
			if json.Unmarshal(plan.payload, &preview) != nil {
				t.Fatal("native plan")
			}
			if replacement == "manual" {
				preview.PlanID = ""
				if _, e = s.issueCleanupPlan(context.Background(), preview, s.store.Revision()); e != nil {
					t.Fatal(e)
				}
			} else {
				// A different operation sent with the expiry identity consumes but cannot
				// substitute the native mode. No secondary cache or expiry retry survives.
				other := q
				other.Operation, other.Mode, other.PlanID = "retire", "apply", v.PlanID
				if _, e = s.diagnosticRetirement(context.Background(), other); e == nil {
					t.Fatal("retirement substituted expiry")
				}
			}
			q.Mode, q.PlanID = "apply", v.PlanID
			if _, e = s.diagnosticExpiry(context.Background(), q); e == nil || p.calls != 0 || p.guards != 0 {
				t.Fatal("superseded expiry retained")
			}
		})
	}
}
func TestDiagnosticExpiryFailedPublicBudgetDoesNotSupersedeLatest(t *testing.T) {
	s, p, q, _ := expiryAdapterFixture(t)
	v, e := s.diagnosticExpiry(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	plan := s.cleanupPlans[v.Proposal.TaskID]
	native := *plan.expiry
	var binding diagnosticExpiryBinding
	if json.Unmarshal(plan.diagnostic, &binding) != nil {
		t.Fatal("binding")
	}
	native.binding = binding
	native.binding.Request.Mode = ""
	var preview protocol.CleanupPreview
	if json.Unmarshal(plan.payload, &preview) != nil {
		t.Fatal("native payload")
	}
	targets := native.binding.View.Proposal.Targets
	native.binding.View.Proposal.Targets = nil
	for i := 0; i < 200; i++ {
		target := targets[0]
		target.ID = fmt.Sprintf("expiry-target:%064x", i+1)
		target.Provider = "git"
		target.Effect = "remove_worktree_and_local_changes"
		target.BranchEffect = "retain_local_branch"
		native.binding.View.Proposal.Targets = append(native.binding.View.Proposal.Targets, target)
	}
	native.binding.View.Proposal.Targets = append(native.binding.View.Proposal.Targets, targets[0])
	if _, e = s.issueBoundNativeCleanupPlan(context.Background(), preview, s.store.Revision(), nil, nil, &native); e == nil || s.cleanupPlans[v.Proposal.TaskID].id != v.PlanID || p.guards != 0 || p.calls != 0 {
		t.Fatal("oversized public bundle superseded latest plan", e)
	}
	native.binding = binding
	native.privateBytes = maxCleanupPlanBytes
	if _, e = s.issueBoundNativeCleanupPlan(context.Background(), preview, s.store.Revision(), nil, nil, &native); e == nil || s.cleanupPlans[v.Proposal.TaskID].id != v.PlanID {
		t.Fatal("private candidate budget ignored")
	}
}

func TestDiagnosticExpiryConsumesBeforeFreshAdmissionEvenWhenUnavailable(t *testing.T) {
	for _, kind := range []string{"pre_cancelled", "missing_guard", "missing_publication", "missing_invalidation"} {
		t.Run(kind, func(t *testing.T) {
			s, p, q, _ := expiryAdapterFixture(t)
			v, e := s.diagnosticExpiry(context.Background(), q)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "pre_cancelled":
				cancel()
			case "missing_guard":
				s.gcGuard = nil
			case "missing_publication":
				s.resourceSnapshot = nil
			case "missing_invalidation":
				s.markResourcesPending = nil
			}
			q.Mode, q.PlanID = "apply", v.PlanID
			if _, e = s.diagnosticExpiry(ctx, q); e == nil || len(s.cleanupPlans) != 0 || p.calls != 0 || p.guards != 0 {
				t.Fatal("unavailable apply retained retry authority", e)
			}
		})
	}
}
