package diagnostics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"radar/internal/agentservice"
	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

const CleanupPreviewTimeout = 30 * time.Second

// cleanupPreview invokes the actual manual or expiry planner, never its executor.
// Explicit consent must warn about provider reads and native Git fetch/prune:
// unlike retirement_preview this can refresh local Git reference metadata.
func cleanupPreview(ctx context.Context, store *state.Store, req protocol.DiagnosticsRequest, pending bool, providers integration.Registry) (protocol.DiagnosticsPage, error) {
	if req.ParentID == "" || len(req.ParentID) > 512 || len(req.ID) > 512 || len(req.Cursor) > 512 || req.State != "" || req.Limit < 0 || req.Limit > 50 || (req.Workspace != "" && (!filepath.IsAbs(req.Workspace) || len(req.Workspace) > 4096)) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview requires one exact retained task parent and bounded selectors")
	}
	if req.CleanupMode == "" {
		req.CleanupMode = "manual"
	}
	if (req.CleanupMode != "manual" && req.CleanupMode != "expired") || len(req.WorkspaceID) > 512 || (req.CleanupMode == "manual" && req.WorkspaceID != "") || (req.CleanupMode == "expired" && req.WorkspaceID == "") {
		return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup mode requires manual or expired with an exact registered workspace ID")
	}
	if req.Limit == 0 {
		req.Limit = 20
	}
	ctx, cancel := context.WithTimeout(ctx, CleanupPreviewTimeout)
	defer cancel()
	if previewExpired(ctx) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview cancelled or timed out")
	}
	if pending {
		return protocol.DiagnosticsPage{}, fmt.Errorf("resource list refresh pending; cleanup preview unavailable")
	}
	records, refs, revision := store.InspectionSnapshot()
	var selected *state.TaskRecord
	for _, record := range records {
		if req.ParentID != record.ID && req.ParentID != strconv.Itoa(record.NumericID) {
			continue
		}
		if selected != nil {
			return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview task identity ambiguous")
		}
		copy := record
		selected = &copy
	}
	if selected == nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview task unavailable")
	}
	related := []state.SourceRefRecord{}
	for _, ref := range refs {
		if ref.TaskRecordID == selected.ID {
			related = append(related, ref)
		}
	}
	if req.Workspace != "" && !recordInWorkspace(related, req.Workspace) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview task is outside selected scope")
	}
	task := productionTask(*selected, related)
	if len(task.SourceRefs) > 256 {
		return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview exceeds native reference budget")
	}
	nativeProviders := providers.CleanupProviders()
	seen := map[string]bool{}
	for _, provider := range nativeProviders {
		name := provider.Descriptor().Name
		if seen[name] || !cleanupProviderKnown(name) {
			return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview provider unsupported or ambiguous")
		}
		seen[name] = true
	}
	if len(nativeProviders) == 0 {
		return protocol.DiagnosticsPage{}, fmt.Errorf("native cleanup preview unsupported")
	}
	// Frozen registration evidence catches workspace changes during native checks.
	// No duplicate expensive/network previews are run solely for pagination.
	registration, err := cleanupRegistrationSnapshot(providers)
	if err != nil {
		return protocol.DiagnosticsPage{}, err
	}
	mode := integration.CleanupConfirmed
	var candidate *workspacegc.Candidate
	if req.CleanupMode == "expired" {
		candidate, err = expiredCleanupCandidate(req, records, refs, providers, registration, selected.ID)
		if err != nil {
			return protocol.DiagnosticsPage{}, err
		}
		task, mode = candidate.Task, integration.CleanupExpired
	}
	if len(task.SourceRefs) > 256 {
		return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview exceeds native reference budget")
	}
	preview, err := cleanup.New(nativeProviders).Preview(ctx, task, mode)
	if err != nil {
		return protocol.DiagnosticsPage{}, fmt.Errorf("native cleanup preview unavailable; provider errors withheld")
	}
	if previewExpired(ctx) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview cancelled or timed out")
	}
	if len(preview.Targets) == 0 || len(preview.Targets) > 512 {
		return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview target coverage exceeds bounds")
	}
	bundleHash := sha256.New()
	bundleBytes := 0
	for _, target := range preview.Targets {
		if len(target.Safety) > 1024 {
			return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview safety evidence exceeds bounds")
		}
		raw, marshalErr := json.Marshal(target)
		if marshalErr != nil || len(raw) > 64*1024 {
			return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview native target exceeds bounds")
		}
		bundleBytes += len(raw)
		if bundleBytes > 1024*1024 {
			return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview native evidence exceeds bounds")
		}
		fmt.Fprintf(bundleHash, "%d:", len(raw))
		bundleHash.Write(raw)
	}
	if candidate != nil {
		selectedPreview, anchor := workspacegc.SelectTargetsForCandidate(preview, *candidate)
		if anchor == nil || len(selectedPreview.Targets) == 0 {
			return protocol.DiagnosticsPage{}, fmt.Errorf("native expired cleanup anchor unavailable; no executable plan created")
		}
		preview = selectedPreview
	}
	// Match Execute's path-based registered-owner guard, without acquiring any
	// lease or operation lock. All targets (including filtered-out pages) share
	// two bounded scans, with evidence computed before exact-row filtering.
	paths := []string{}
	for _, target := range preview.Targets {
		if target.Path != "" {
			paths = append(paths, target.Path)
		}
	}
	var ownerCounts map[string]int
	var ownerSnapshot [32]byte
	var ownerErr error
	if len(paths) > 0 {
		ownerCounts, ownerSnapshot, ownerErr = agentservice.DiagnosticWorkspaceRemovalOwners(ctx, paths)
	}
	items := []protocol.DiagnosticItem{}
	registrationSum := sha256.Sum256([]byte(registration))
	identities := map[string]bool{}
	nativeBytes := 0
	for _, target := range preview.Targets {
		if !seen[target.Source] {
			return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview target provider unavailable")
		}
		view, err := cleanupTargetView(target)
		if err != nil {
			return protocol.DiagnosticsPage{}, err
		}
		view.CleanupMode = req.CleanupMode
		view.AutomaticBlockerCount = len(cleanup.AutomaticBlockingMessages([]protocol.CleanupTarget{target}, mode == integration.CleanupExpired))
		if candidate != nil {
			expiry, ok := workspacegc.ExpiryAt(candidate.Task)
			if !ok {
				return protocol.DiagnosticsPage{}, fmt.Errorf("expired cleanup deadline unavailable")
			}
			view.WorkspaceID, view.ExpiryAt = candidate.WorkspaceID, expiry.UTC().Format(time.RFC3339Nano)
		}
		// Full native target bytes make identity sensitive to private operation and
		// safety drift without returning the underlying paths/messages/operations.
		if len(target.Safety) > 1024 {
			return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview safety evidence exceeds bounds")
		}
		raw, _ := json.Marshal(target)
		if len(raw) > 64*1024 {
			return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview native target exceeds bounds")
		}
		nativeBytes += len(raw)
		if nativeBytes > 1024*1024 {
			return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview native evidence exceeds bounds")
		}
		// Include the full private registration fingerprint, not only counts, so
		// same-count owner changes invalidate the next page without exposing IDs.
		identity := append(append([]byte{}, registrationSum[:]...), ownerSnapshot[:]...)
		identity = append(identity, bundleHash.Sum(nil)...)
		sum := sha256.Sum256(append(identity, raw...))
		view.ID = "cleanup-target:" + hex.EncodeToString(sum[:])
		resourceIdentity := target.Source + "\x00" + target.Kind + "\x00" + target.ResourceID
		if identities[resourceIdentity] {
			return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview target identity ambiguous")
		}
		identities[resourceIdentity] = true
		view.RecordID, view.TaskID = clip(selected.ID), selected.NumericID
		view.ExecutionEligibility = "unknown_fresh_execution_guards_not_run"
		view.OwnershipEvidence = "not_checked_no_target_path"
		if target.Path != "" {
			view.OwnershipEvidence = "unavailable"
			if ownerErr == nil {
				count := ownerCounts[target.Path]
				view.OwnershipEvidence, view.AgentOwnerCount = "fresh_bounded_registration", &count
				if count > 0 {
					view.ExecutionEligibility = "blocked_registered_agent"
				}
			}
		}
		if req.ID == "" || req.ID == view.ID {
			items = append(items, protocol.DiagnosticItem{ID: view.ID, CleanupPreview: &view})
		}
	}
	after, err := cleanupRegistrationSnapshot(providers)
	if err != nil {
		return protocol.DiagnosticsPage{}, err
	}
	if registration != after || store.Revision() != revision {
		return protocol.DiagnosticsPage{}, fmt.Errorf("diagnostic snapshot changed; preview again")
	}
	if len(paths) > 0 {
		_, freshSnapshot, freshErr := agentservice.DiagnosticWorkspaceRemovalOwners(ctx, paths)
		if (ownerErr == nil) != (freshErr == nil) || (ownerErr == nil && ownerSnapshot != freshSnapshot) {
			return protocol.DiagnosticsPage{}, fmt.Errorf("registered ownership snapshot changed; preview again")
		}
	}
	if store.Revision() != revision {
		return protocol.DiagnosticsPage{}, fmt.Errorf("diagnostic snapshot changed; preview again")
	}
	if previewExpired(ctx) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("cleanup preview cancelled or timed out")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	evidence := "explicit_native_manual_cleanup_preview_not_execution_plan"
	if candidate != nil {
		evidence = "explicit_native_expired_cleanup_preview_not_execution_plan"
	}
	return paginate(req, items, revision, pending, evidence)
}

func cleanupProviderKnown(name string) bool {
	return name == "git" || name == "sbx" || name == "tmux" || name == "workspace"
}

func cleanupRegistrationSnapshot(providers integration.Registry) (string, error) {
	provider, err := providers.WorkspaceDiagnostics()
	if err != nil {
		return "", fmt.Errorf("cleanup preview registration capability unavailable")
	}
	groups, err := provider.DiagnosticWorkspaceRegistrations()
	if err != nil {
		return "", fmt.Errorf("cleanup preview registration unavailable")
	}
	if len(groups) > 1024 {
		return "", fmt.Errorf("cleanup preview registration exceeds bounds")
	}
	raw, err := json.Marshal(groups)
	if err != nil || len(raw) > 1024*1024 {
		return "", fmt.Errorf("cleanup preview registration unavailable or over budget")
	}
	return string(raw), nil
}

func cleanupTargetView(target protocol.CleanupTarget) (protocol.DiagnosticCleanupTarget, error) {
	view := protocol.DiagnosticCleanupTarget{Provider: target.Source, Safety: []protocol.DiagnosticCleanupSafety{}, Preserved: []string{"remote_repositories"}}
	switch {
	case target.Source == "git" && target.Kind == "worktree":
		view.Effect = "remove_worktree_and_local_changes"
		if target.Operation["delete_branch"] != "" {
			view.BranchEffect = "delete_local_branch"
		} else {
			view.BranchEffect = "retain_local_branch"
		}
	case target.Source == "sbx" && target.Kind == "sandbox":
		view.Effect = "remove_sandbox_and_local_state"
	case target.Source == "tmux" && target.Kind == "session":
		view.Effect = "remove_terminal_session_and_processes"
	case target.Source == "workspace" && target.Kind == "workspace":
		view.Effect = "remove_workspace_local_files_registration_and_archive_completed_note"
	case target.Source == "workspace" && target.Kind == "missing_member":
		view.Effect = "forget_missing_worktree_registration"
		view.BranchEffect = "retain_local_branch"
	default:
		return view, fmt.Errorf("cleanup preview target effect unsupported")
	}
	if target.ResourceID == "" {
		return view, fmt.Errorf("cleanup preview native target identity unavailable")
	}
	// Preserve native safety distinctions and blocker/expiry flags, not raw text.
	counts := map[protocol.DiagnosticCleanupSafety]int{}
	for _, notice := range target.Safety {
		kind := notice.Kind
		switch kind {
		case "local_changes", "unpublished_data", "safety_check_unavailable", "deletes_local_data", "unknown_entries", "disposable_data":
		default:
			kind = "other_notice"
		}
		key := protocol.DiagnosticCleanupSafety{Kind: kind, BlocksAutomatic: notice.BlocksAutomatic, Expires: notice.Expires}
		counts[key]++
	}
	for key, count := range counts {
		key.Count = count
		view.Safety = append(view.Safety, key)
	}
	sort.Slice(view.Safety, func(i, j int) bool {
		a, _ := json.Marshal(view.Safety[i])
		b, _ := json.Marshal(view.Safety[j])
		return string(a) < string(b)
	})
	view.SafetyNoticeCount = len(target.Safety)
	view.AutomaticBlockerCount = len(cleanup.AutomaticBlockingMessages([]protocol.CleanupTarget{target}, false))
	return view, nil
}
