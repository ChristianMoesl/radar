package diagnostics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

// ManualCleanupScope captures private registration/retained identity without
// reconstructing a task or invoking a provider planner. The server must still
// require the canonical current filtered production task before planning/apply.
type ManualCleanupScope struct {
	RecordID     string
	TaskID       int
	Revision     int64
	Registration string
	FeedbackID   string
	WorkspaceID  string
}

func CaptureManualCleanupScope(ctx context.Context, store *state.Store, req protocol.DiagnosticMaintenanceRequest, providers integration.Registry) (ManualCleanupScope, error) {
	fail := func() (ManualCleanupScope, error) {
		return ManualCleanupScope{}, errors.New("exact registered manual cleanup scope unavailable")
	}
	if ctx.Err() != nil || req.ParentID == "" || len(req.ParentID) > 512 || req.FeedbackWorkspace == "" || !filepath.IsAbs(req.FeedbackWorkspace) || len(req.FeedbackWorkspace) > 4096 || len(req.Workspace) > 4096 || (req.Scope != "origin" && req.Scope != "feedback" && req.Scope != "installation") || (req.Scope == "installation" && req.Workspace != "") || (req.Scope != "installation" && (req.Workspace == "" || !filepath.IsAbs(req.Workspace))) {
		return fail()
	}
	registration, err := cleanupRegistrationSnapshot(providers)
	if err != nil {
		return fail()
	}
	var groups []protocol.DiagnosticWorkspace
	if json.Unmarshal([]byte(registration), &groups) != nil {
		return fail()
	}
	ids, paths := map[string]bool{}, map[string]bool{}
	var feedback, selected *protocol.DiagnosticWorkspace
	for i := range groups {
		group := &groups[i]
		if !group.Registered || group.ID == "" || len(group.ID) > 512 || !filepath.IsAbs(group.Path) || filepath.Clean(group.Path) != group.Path || len(group.Path) > 4096 || ids[group.ID] || paths[group.Path] {
			return fail()
		}
		ids[group.ID], paths[group.Path] = true, true
		if group.Path == req.FeedbackWorkspace {
			feedback = group
		}
		if group.Path == req.Workspace {
			selected = group
		}
	}
	if feedback == nil || !feedback.Feedback {
		return fail()
	}
	if req.Scope != "installation" && (selected == nil || (req.Scope == "origin" && (feedback.FeedbackOrigin == "" || feedback.FeedbackOrigin != selected.Path)) || (req.Scope == "feedback" && selected.ID != feedback.ID)) {
		return fail()
	}
	records, refs, revision := store.InspectionSnapshot()
	result := ManualCleanupScope{Revision: revision, Registration: registration, FeedbackID: feedback.ID}
	if selected != nil {
		result.WorkspaceID = selected.ID
	}
	matches := 0
	for _, record := range records {
		if req.ParentID != record.ID && req.ParentID != strconv.Itoa(record.NumericID) {
			continue
		}
		if record.ID == "" || len(record.ID) > 512 || record.NumericID < 1 {
			return fail()
		}
		result.RecordID, result.TaskID = record.ID, record.NumericID
		matches++
	}
	if matches != 1 {
		return fail()
	}
	related := []state.SourceRefRecord{}
	for _, ref := range refs {
		if ref.TaskRecordID == result.RecordID {
			related = append(related, ref)
		}
	}
	if len(related) > 256 || (req.Workspace != "" && !recordInWorkspace(related, req.Workspace)) || store.Revision() != revision || ctx.Err() != nil {
		return fail()
	}
	return result, nil
}

// ManualCleanupTaskInScope applies the existing retained-reference path relation
// to the current production task, not historical-only refs or a reconstructed task.
func ManualCleanupTaskInScope(task protocol.Task, workspace string) bool {
	if len(task.SourceRefs) > 256 {
		return false
	}
	if workspace == "" {
		return true
	}
	refs := make([]state.SourceRefRecord, 0, len(task.SourceRefs))
	for _, ref := range task.SourceRefs {
		refs = append(refs, state.SourceRefRecord{Snapshot: ref})
	}
	return recordInWorkspace(refs, workspace)
}

// DiscloseManualCleanup preserves every original ordinal, including duplicate
// native resource identities. It refuses unsupported/oversized public evidence
// rather than turning a truncated page into permission for an undisclosed suffix.
// Ownership and lease probes are point-in-time observations, not held admission.
func DiscloseManualCleanup(ctx context.Context, preview protocol.CleanupPreview, scope ManualCleanupScope) ([]protocol.DiagnosticCleanupTarget, error) {
	fail := func() ([]protocol.DiagnosticCleanupTarget, error) {
		return nil, errors.New("complete manual cleanup disclosure unavailable")
	}
	if preview.TaskID != scope.TaskID || len(preview.Targets) < 1 || len(preview.Targets) > 512 || ctx.Err() != nil {
		return fail()
	}
	var groups []protocol.DiagnosticWorkspace
	if json.Unmarshal([]byte(scope.Registration), &groups) != nil {
		return fail()
	}
	workspaceIDs := map[string]bool{}
	for _, group := range groups {
		workspaceIDs[group.ID] = true
	}
	paths := []string{}
	for _, target := range preview.Targets {
		if target.Path != "" {
			paths = append(paths, target.Path)
		}
	}
	var owners map[string]int
	var ownership [32]byte
	if len(paths) > 0 {
		var err error
		owners, ownership, err = agentservice.DiagnosticWorkspaceRemovalOwners(ctx, paths)
		if err != nil {
			return fail()
		}
	}
	leases, leaseSnapshot := cleanupLeaseSnapshot(ctx, paths)
	views := make([]protocol.DiagnosticCleanupTarget, 0, len(preview.Targets))
	nativeBytes := 0
	for i, target := range preview.Targets {
		if len(target.Safety) > 1024 {
			return fail()
		}
		raw, err := json.Marshal(target)
		nativeBytes += len(raw)
		if err != nil || len(raw) > 64*1024 || nativeBytes > 1024*1024 {
			return fail()
		}
		view, err := cleanupTargetView(target)
		if err != nil {
			return fail()
		}
		sum := sha256.Sum256(append([]byte(fmt.Sprintf("%d:", i)), raw...))
		view.ID = "cleanup-target:" + hex.EncodeToString(sum[:])
		view.RecordID, view.TaskID, view.CleanupMode = scope.RecordID, scope.TaskID, "manual"
		if len(target.WorkspaceID) > 512 || (target.WorkspaceID != "" && !workspaceIDs[target.WorkspaceID]) {
			return fail()
		}
		view.WorkspaceID = target.WorkspaceID
		view.ExecutionEligibility = "unknown_fresh_execution_guards_not_run"
		view.OwnershipEvidence, view.LeaseEvidence = "not_checked_no_target_path", "not_checked_no_target_path"
		if target.Path != "" {
			count, exists := owners[target.Path]
			// Missing/contended/unavailable lease evidence is not full eligibility, but
			// an observed contention or registered owner blocks plan issuance outright.
			if !exists || count != 0 || leases[target.Path] == "contended_at_probe" {
				return fail()
			}
			view.OwnershipEvidence, view.AgentOwnerCount = "fresh_bounded_registration", &count
			view.LeaseEvidence = leases[target.Path]
		}
		views = append(views, view)
	}
	if len(paths) > 0 {
		_, fresh, err := agentservice.DiagnosticWorkspaceRemovalOwners(ctx, paths)
		if err != nil || fresh != ownership {
			return fail()
		}
	}
	if _, fresh := cleanupLeaseSnapshot(ctx, paths); fresh != leaseSnapshot || ctx.Err() != nil {
		return fail()
	}
	raw, err := json.Marshal(views)
	if err != nil || len(raw) > 28*1024 {
		return fail()
	}
	return views, nil
}
