package diagnostics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

func retirementPreview(ctx context.Context, store *state.Store, req protocol.DiagnosticsRequest, pending bool, providers integration.Registry) (protocol.DiagnosticsPage, error) {
	if req.ParentID == "" || len(req.ParentID) > 512 || req.ID != "" || req.State != "" || req.Cursor != "" || req.Limit < 0 || req.Limit > 50 || (req.Workspace != "" && (!filepath.IsAbs(req.Workspace) || len(req.Workspace) > 4096)) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("retirement preview requires one exact registered workspace parent and no row/state/cursor selectors")
	}
	ctx, cancel := context.WithTimeout(ctx, RuntimeProbeTimeout)
	defer cancel()
	if previewExpired(ctx) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("retirement preview cancelled or timed out")
	}
	registrations, err := providers.WorkspaceDiagnostics()
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("retirement preview registration capability unavailable")
	}
	planner, ok := registrations.(integration.WorkspaceRetirementPreview)
	if !ok {
		return protocol.DiagnosticsPage{}, fmt.Errorf("native registration-only retirement preview unsupported")
	}
	groups, err := registrations.DiagnosticWorkspaceRegistrations()
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("retirement preview registration unavailable")
	}
	before, _ := json.Marshal(groups)
	var selected *protocol.DiagnosticWorkspace
	for _, group := range groups {
		if group.ID != req.ParentID || (req.Workspace != "" && filepath.Clean(req.Workspace) != filepath.Clean(group.Path)) {
			continue
		}
		if selected != nil {
			return protocol.DiagnosticsPage{}, fmt.Errorf("retirement preview registration ambiguous")
		}
		copy := group
		selected = &copy
	}
	if selected == nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("retirement preview parent is not registered in the selected scope")
	}
	manager, err := providers.WorkspaceManager()
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("retirement preview workspace configuration unsupported")
	}
	root, err := manager.DefaultRoot()
	if err != nil || strings.TrimSpace(root) == "" {
		return protocol.DiagnosticsPage{}, fmt.Errorf("retirement preview workspace configuration unavailable")
	}
	records, refs, revision := store.InspectionSnapshot()
	plan, err := workspacegc.InspectPlan(records, refs, time.Now(), workspacegc.Options{WorkspaceRoot: root})
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("retirement preview GC planner unavailable")
	}
	var candidate *workspacegc.Candidate
	for _, entry := range plan.Candidates {
		if entry.WorkspaceID != selected.ID || filepath.Clean(entry.Path) != filepath.Clean(selected.Path) {
			continue
		}
		if candidate != nil {
			return protocol.DiagnosticsPage{}, fmt.Errorf("retirement preview has ambiguous cached GC candidates")
		}
		copy := entry
		candidate = &copy
	}
	if candidate == nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("workspace is not a current cached GC candidate; inspect gc_plan")
	}
	mode, err := workspacegc.CandidateMode(*candidate, time.Now())
	if err != nil || mode != integration.CleanupRetire {
		return protocol.DiagnosticsPage{}, fmt.Errorf("only the runtime-retirement stage supports this preview; inspect gc_plan")
	}
	if previewExpired(ctx) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("retirement preview cancelled or timed out")
	}
	targets, err := planner.DiagnosticRetirementTargets(ctx, candidate.Task, selected.ID)
	if err != nil {
		// Native path/registration errors can contain private names and paths.
		return protocol.DiagnosticsPage{}, fmt.Errorf("native retirement target checks unavailable; no executable plan created")
	}
	view := protocol.DiagnosticRetirementPreview{ID: selected.ID, RecordID: clip(candidate.RecordID), TaskID: candidate.TaskID, Stage: "runtime_retirement", Targets: []protocol.DiagnosticRetirementTarget{}, Preserved: []string{"workspace_files", "worktrees_and_branches", "sandbox_and_local_state", "registration"}, ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "unknown_not_checked", LeaseEvidence: "unknown_not_checked"}
	if len(targets) < 1 || len(targets) > 3 {
		return protocol.DiagnosticsPage{}, fmt.Errorf("native retirement target coverage unavailable")
	}
	seen := map[string]bool{}
	for _, target := range targets {
		if target.WorkspaceID != selected.ID || filepath.Clean(target.Path) != filepath.Clean(selected.Path) || seen[target.Source] {
			return protocol.DiagnosticsPage{}, fmt.Errorf("native retirement target identity or scope unavailable")
		}
		seen[target.Source] = true
		effect, expected, kind := "", selected.ID, "workspace"
		switch target.Source {
		case "workspace":
			effect = "retain_workspace_files_and_registration"
		case "sbx":
			effect, expected, kind = "stop_sandbox_commands_and_services", selected.SandboxName, "sandbox"
		case "tmux":
			effect, expected, kind = "remove_terminal_session_and_processes", selected.SessionName, "session"
		default:
			return protocol.DiagnosticsPage{}, fmt.Errorf("native retirement target provider unsupported")
		}
		if expected == "" || target.ResourceID != expected || target.Kind != kind || target.ProvidesWorkspace != (target.Source == "workspace") {
			return protocol.DiagnosticsPage{}, fmt.Errorf("native retirement target registration mismatch")
		}
		identity, _ := json.Marshal([]string{selected.ID, target.Source, target.Kind, target.ResourceID, target.Path})
		hash := sha256.Sum256(identity)
		view.Targets = append(view.Targets, protocol.DiagnosticRetirementTarget{ID: "retirement-target:" + hex.EncodeToString(hash[:]), Provider: target.Source, Effect: effect, SafetyNoticeCount: len(target.Safety), AutomaticBlockerCount: len(cleanup.AutomaticBlockingMessages([]protocol.CleanupTarget{target}, false))})
	}
	if !seen["workspace"] || seen["sbx"] != (selected.SandboxName != "") || seen["tmux"] != (selected.SessionName != "") {
		return protocol.DiagnosticsPage{}, fmt.Errorf("native retirement target coverage unavailable")
	}
	// Re-run only the non-mutating native target planner, never full cleanup
	// preview, to detect registration changes not present in the public view.
	oldTargets, _ := json.Marshal(targets)
	freshTargets, err := planner.DiagnosticRetirementTargets(ctx, candidate.Task, selected.ID)
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("native retirement target checks unavailable after preview")
	}
	newTargets, _ := json.Marshal(freshTargets)
	current, err := registrations.DiagnosticWorkspaceRegistrations()
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("retirement registration unavailable after preview")
	}
	after, _ := json.Marshal(current)
	if string(before) != string(after) || string(oldTargets) != string(newTargets) || revision != store.Revision() {
		return protocol.DiagnosticsPage{}, fmt.Errorf("diagnostic snapshot changed; preview again")
	}
	mode, err = workspacegc.CandidateMode(*candidate, time.Now())
	if err != nil || mode != integration.CleanupRetire {
		return protocol.DiagnosticsPage{}, fmt.Errorf("diagnostic snapshot changed; retirement stage changed")
	}
	if previewExpired(ctx) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("retirement preview cancelled or timed out")
	}
	req.Limit = 1
	return paginate(req, []protocol.DiagnosticItem{{ID: selected.ID, RetirementPreview: &view}}, revision, pending, "explicit_native_registration_only_retirement_preview")
}

func previewExpired(ctx context.Context) bool {
	deadline, bounded := ctx.Deadline()
	return ctx.Err() != nil || (bounded && !time.Now().Before(deadline))
}
