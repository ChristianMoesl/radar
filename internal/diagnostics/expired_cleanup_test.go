package diagnostics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/tmux"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/operationlock"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
	"radar/internal/workspacelease"
	"reflect"
	"strings"
	"testing"
	"time"
)

func expiredFixture(t *testing.T) (*state.Store, string, []workspacegroup.Workspace, protocol.Task) {
	t.Helper()
	store, root, path := fixture(t)
	groups := []workspacegroup.Workspace{}
	current := task(path, "test:ABC-123")
	for i, anchor := range []string{path, filepath.Join(root, "other")} {
		if err := os.MkdirAll(anchor, 0700); err != nil {
			t.Fatal(err)
		}
		group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: filepath.Base(anchor), Path: anchor, SessionName: "private-session-" + filepath.Base(anchor)}
		groups = append(groups, group)
		if i > 0 {
			current.SourceRefs = append(current.SourceRefs, protocol.SourceRef{ID: "workspace:" + group.ID, Source: "workspace", Kind: "workspace", WorkspaceID: group.ID, Path: anchor, ProvidesWorkspace: true})
		}
		current.SourceRefs = append(current.SourceRefs, protocol.SourceRef{ID: "tmux:" + group.SessionName, Source: "tmux", Kind: "session", Title: group.SessionName, Path: anchor})
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: groups}); err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{current})
	return store, root, groups, current
}

func TestExpiredCleanupPreviewUsesProductionCandidateAndExactNativeResourceBoundary(t *testing.T) {
	store, root, groups, _ := expiredFixture(t)
	providers := integration.NewRegistry(workspace.Source{}, tmux.Source{})
	records, refs, revision := store.InspectionSnapshot()
	plan, err := workspacegc.InspectPlan(records, refs, time.Now(), workspacegc.Options{WorkspaceRoot: root})
	if err != nil || len(plan.Candidates) != 2 {
		t.Fatalf("plan=%+v %v", plan, err)
	}
	var candidate workspacegc.Candidate
	for _, entry := range plan.Candidates {
		if entry.WorkspaceID == groups[0].ID {
			candidate = entry
		}
	}
	if candidate.WorkspaceID == "" {
		t.Fatal("exact selected native candidate missing")
	}
	native, err := cleanup.New(providers.CleanupProviders()).Preview(context.Background(), candidate.Task, integration.CleanupExpired)
	if err != nil {
		t.Fatal(err)
	}
	selected, anchor := workspacegc.SelectTargetsForCandidate(native, candidate)
	if anchor == nil || len(selected.Targets) != 2 || len(native.Targets) != 4 {
		t.Fatalf("wrong native fixture %+v", native)
	}
	retirementOwner(t, groups[1].Path)
	release, err := operationlock.Acquire(false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	lease, err := workspacelease.Acquire(candidate.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease()
	before, _ := os.ReadFile(workspacegroup.Path(root))
	req := protocol.DiagnosticsRequest{Section: "cleanup_preview", CleanupMode: "expired", WorkspaceID: candidate.WorkspaceID, ParentID: candidate.RecordID, Workspace: candidate.Path, Limit: 1}
	count := 0
	for {
		page, err := Probe(context.Background(), store, req, false, providers)
		if err != nil || page.Total != len(selected.Targets) || page.Evidence != "explicit_native_expired_cleanup_preview_not_execution_plan" {
			t.Fatalf("page=%+v %v", page, err)
		}
		for _, row := range page.Items {
			v := row.CleanupPreview
			expiry, ok := workspacegc.ExpiryAt(candidate.Task)
			if !ok || v.CleanupMode != "expired" || v.WorkspaceID != candidate.WorkspaceID || v.ExpiryAt != expiry.UTC().Format(time.RFC3339Nano) || v.OwnershipEvidence != "fresh_bounded_registration" || v.AgentOwnerCount == nil || *v.AgentOwnerCount != 0 || v.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" {
				t.Fatalf("native candidate projection=%+v", v)
			}
			count++
		}
		raw, _ := json.Marshal(page)
		if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), candidate.Path) {
			t.Fatal("private resource evidence escaped")
		}
		if page.Complete {
			break
		}
		req.Cursor = page.NextCursor
	}
	after, _ := os.ReadFile(workspacegroup.Path(root))
	afterRecords, afterRefs, afterRevision := store.InspectionSnapshot()
	if count != 2 || afterRevision != revision || !reflect.DeepEqual(records, afterRecords) || !reflect.DeepEqual(refs, afterRefs) || string(before) != string(after) {
		t.Fatal("preview mutated or lost bundle state")
	}
	for _, group := range groups {
		if _, err := os.Stat(group.Path); err != nil {
			t.Fatal("workspace removed", err)
		}
	}
	req.Workspace = groups[1].Path
	if _, err := Probe(context.Background(), store, req, false, providers); err == nil {
		t.Fatal("same-task other workspace bypassed expiry candidate scope")
	}
}

func TestExpiredCleanupPreviewRejectsNonCandidatesBeforeProviderReads(t *testing.T) {
	for _, scenario := range []string{"recent", "retirement-stage", "active", "review", "unregistered", "wrong-parent", "missing-workspace", "manual-with-workspace", "unsupported-mode", "pending"} {
		t.Run(scenario, func(t *testing.T) {
			store, root, groups, current := expiredFixture(t)
			provider, _ := manualPreviewFixture(t, 1)
			provider.mode = integration.CleanupExpired
			req := protocol.DiagnosticsRequest{Section: "cleanup_preview", CleanupMode: "expired", WorkspaceID: groups[0].ID, Workspace: groups[0].Path}
			switch scenario {
			case "recent":
				current.SourceRefs[0].Metadata["completed_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
			case "retirement-stage":
				current.SourceRefs[0].Metadata["completed_at"] = time.Now().Add(-4 * 24 * time.Hour).UTC().Format(time.RFC3339)
			case "active":
				current.SourceRefs[0].Activity = protocol.ActivityBusy
			case "review":
				current.SourceRefs[1].AgentReview = true
			case "unregistered":
				if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version}); err != nil {
					t.Fatal(err)
				}
			case "missing-workspace":
				req.WorkspaceID = ""
			case "manual-with-workspace":
				req.CleanupMode = "manual"
			case "unsupported-mode":
				req.CleanupMode = "safe"
			}
			store.SetTasks([]protocol.Task{current})
			records, _, _ := store.InspectionSnapshot()
			req.ParentID = records[0].ID
			if scenario == "wrong-parent" {
				req.ParentID = "not-retained"
			}
			page, err := Probe(context.Background(), store, req, scenario == "pending", integration.NewRegistry(provider, workspace.Source{}))
			if err == nil || len(page.Items) > 0 || provider.calls != 0 || strings.Contains(err.Error(), "private-") {
				t.Fatalf("unsafe candidate reached planner: %+v %v calls=%d", page, err, provider.calls)
			}
		})
	}
}

type expiredNoAnchor struct{ workspace.Source }

func (expiredNoAnchor) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	return nil, nil
}

func TestExpiredCleanupPreviewRejectsMissingAnchorDriftAndExcludedBudgetExcess(t *testing.T) {
	for _, scenario := range []string{"no-anchor", "excluded-oversized", "lifecycle-drift", "registration-drift"} {
		t.Run(scenario, func(t *testing.T) {
			store, root, groups, current := expiredFixture(t)
			provider, _ := manualPreviewFixture(t, 2)
			provider.mode = integration.CleanupExpired
			for i := range provider.targets {
				provider.targets[i].WorkspaceID = groups[i].ID
			}
			registry := integration.NewRegistry(provider, workspace.Source{})
			switch scenario {
			case "no-anchor":
				registry = integration.NewRegistry(provider, expiredNoAnchor{})
			case "excluded-oversized":
				provider.targets[1].Operation["token"] = strings.Repeat("private", 10000)
			case "lifecycle-drift":
				provider.run = func(context.Context) {
					current.SourceRefs[0].Metadata["completed_at"] = time.Now().UTC().Format(time.RFC3339)
					store.SetTasks([]protocol.Task{current})
				}
			case "registration-drift":
				provider.run = func(context.Context) {
					groups[0].SessionName = "private-changed"
					if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: groups}); err != nil {
						t.Fatal(err)
					}
				}
			}
			records, _, _ := store.InspectionSnapshot()
			page, err := Probe(context.Background(), store, protocol.DiagnosticsRequest{Section: "cleanup_preview", CleanupMode: "expired", ParentID: records[0].ID, WorkspaceID: groups[0].ID}, false, registry)
			if err == nil || len(page.Items) > 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe native evidence escaped: %+v %v", page, err)
			}
		})
	}
}

func TestExpiredCleanupPreviewNativeExpiringBlockersAndOtherTargetDrift(t *testing.T) {
	store, _, groups, _ := expiredFixture(t)
	provider, _ := manualPreviewFixture(t, 2)
	provider.mode = integration.CleanupExpired
	for i := range provider.targets {
		provider.targets[i].WorkspaceID = groups[i].ID
		provider.targets[i].Path = filepath.Join(groups[i].Path, "member")
	}
	registry := integration.NewRegistry(provider, workspace.Source{})
	records, _, _ := store.InspectionSnapshot()
	req := protocol.DiagnosticsRequest{Section: "cleanup_preview", CleanupMode: "expired", WorkspaceID: groups[0].ID, ParentID: records[0].ID, Limit: 1}
	first, err := Probe(context.Background(), store, req, false, registry)
	if err != nil || first.Total != 2 || first.NextCursor == "" {
		t.Fatalf("preview=%+v %v", first, err)
	}
	req.Cursor = first.NextCursor
	provider.targets[1].Operation["token"] = "private-changed"
	if _, err := Probe(context.Background(), store, req, false, registry); err == nil {
		t.Fatal("other native target drift reused expiry cursor")
	}
	req.Cursor = ""
	req.Limit = 50
	page, err := Probe(context.Background(), store, req, false, registry)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range page.Items {
		v := row.CleanupPreview
		if v.Provider == "git" {
			found = true
			if v.SafetyNoticeCount != 2 || v.AutomaticBlockerCount != 1 {
				t.Fatalf("native expiry notice semantics lost: %+v", v)
			}
		}
	}
	if !found {
		t.Fatal("selected group member absent")
	}
	for _, section := range []string{"tasks", "runtime_probe", "retirement_preview", "agent_probe"} {
		invalid := protocol.DiagnosticsRequest{Section: section, CleanupMode: "expired", WorkspaceID: groups[0].ID, ParentID: groups[0].ID}
		if _, err := Read(store, invalid, false, registry); err == nil {
			t.Fatal("cached read accepted cleanup mode")
		}
		if _, err := Probe(context.Background(), store, invalid, false, registry); err == nil {
			t.Fatal("other probe accepted cleanup mode")
		}
	}
}
