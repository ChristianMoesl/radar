package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

// CheckCleanupBundle is an explicit maintenance action, never a read/probe. Its
// caller must obtain approval for native reads/metadata writes and real transient
// admission. It shares the native planner and Service guards, not their policy.
// No Cleanup is called and successful guard return is not full eligibility.
func CheckCleanupBundle(ctx context.Context, store *state.Store, req protocol.DiagnosticMaintenanceRequest, pending func() bool, providers integration.Registry) (protocol.DiagnosticCleanupCheck, error) {
	fail := func() (protocol.DiagnosticCleanupCheck, error) {
		return protocol.DiagnosticCleanupCheck{}, errors.New("native manual cleanup guard check unavailable; no Cleanup dispatch requested")
	}
	if req.Schema != protocol.DiagnosticsVersion || req.Operation != "cleanup_check" || req.Mode != "check" || req.PlanID != "" || req.FeedbackWorkspace != "" || req.ParentID == "" || len(req.ParentID) > 512 || len(req.Workspace) > 4096 || (req.Scope != "origin" && req.Scope != "feedback" && req.Scope != "installation") || (req.Scope == "installation" && req.Workspace != "") || (req.Scope != "installation" && (req.Workspace == "" || !filepath.IsAbs(req.Workspace))) || pending == nil {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, CleanupPreviewTimeout)
	defer cancel()
	if ctx.Err() != nil || pending() {
		return fail()
	}
	// Validate exact registered scope before any provider planner/read is invoked.
	registration, err := cleanupRegistrationSnapshot(providers)
	if err != nil {
		return fail()
	}
	var groups []protocol.DiagnosticWorkspace
	if json.Unmarshal([]byte(registration), &groups) != nil {
		return fail()
	}
	ids, paths := map[string]bool{}, map[string]bool{}
	matches := 0
	for _, group := range groups {
		path := filepath.Clean(group.Path)
		if group.ID == "" || len(group.ID) > 512 || len(group.Path) > 4096 || !filepath.IsAbs(group.Path) || ids[group.ID] || paths[path] {
			return fail()
		}
		ids[group.ID], paths[path] = true, true
		if req.Workspace != "" && path == filepath.Clean(req.Workspace) {
			matches++
		}
	}
	if req.Workspace != "" && matches != 1 {
		return fail()
	}
	prepared, err := prepareCleanup(ctx, store, protocol.DiagnosticsRequest{ParentID: req.ParentID, Workspace: req.Workspace, CleanupMode: "manual"}, providers)
	if err != nil || registration != prepared.registration || len(prepared.selected.ID) == 0 || len(prepared.selected.ID) > 512 || prepared.selected.NumericID < 1 {
		return fail()
	}
	// Keep supported effects and all size bounds, without preview presentation IDs
	// or target deduplication. Admission deduplication never drops native targets.
	for _, target := range prepared.preview.Targets {
		if _, err := cleanupTargetView(target); err != nil || !prepared.seen[target.Source] {
			return fail()
		}
	}
	fresh := func() bool {
		current, err := cleanupRegistrationSnapshot(providers)
		return err == nil && current == registration && store.Revision() == prepared.revision && !pending() && ctx.Err() == nil
	}
	if !fresh() {
		return fail()
	}
	checkErr := cleanup.New(prepared.providers).CheckBundle(ctx, prepared.preview, cleanup.ExecuteOptions{Mode: integration.CleanupConfirmed})
	if !fresh() {
		return fail()
	}
	status := "guards_passed_at_check"
	if checkErr != nil {
		status = "not_passed_or_unavailable"
	}
	return protocol.DiagnosticCleanupCheck{Schema: protocol.DiagnosticsVersion, Operation: "cleanup_check", Mode: "manual", Scope: req.Scope, RecordID: prepared.selected.ID, TaskID: prepared.selected.NumericID, Evidence: "native_manual_cleanup_bundle_guard_check_not_execution_eligibility", Status: status, TargetCount: len(prepared.preview.Targets), Revision: prepared.revision, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), ExecutionEligibility: "unknown_last_moment_provider_guards_not_run", CleanupDispatched: false, ResourceRefreshPending: false, Limitations: protocol.DiagnosticCleanupCheckLimitations()}, nil
}
