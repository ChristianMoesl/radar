package diagnostics

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

// expiredCleanupCandidate uses the production GC gate/stage, not manual cleanup
// policy. Scope bounds the selected registered candidate; provider Preview still
// receives the owning task exactly as GC does before native candidate selection.
func expiredCleanupCandidate(req protocol.DiagnosticsRequest, records []state.TaskRecord, refs []state.SourceRefRecord, providers integration.Registry, registration, recordID string) (*workspacegc.Candidate, error) {
	var groups []protocol.DiagnosticWorkspace
	if json.Unmarshal([]byte(registration), &groups) != nil {
		return nil, fmt.Errorf("expired cleanup registration unavailable")
	}
	var group *protocol.DiagnosticWorkspace
	for _, entry := range groups {
		if entry.ID != req.WorkspaceID {
			continue
		}
		if group != nil {
			return nil, fmt.Errorf("expired cleanup registration ambiguous")
		}
		copy := entry
		group = &copy
	}
	if group == nil || !filepath.IsAbs(group.Path) || (req.Workspace != "" && filepath.Clean(req.Workspace) != filepath.Clean(group.Path)) {
		return nil, fmt.Errorf("expired cleanup workspace is not registered in selected scope")
	}
	manager, err := providers.WorkspaceManager()
	if err != nil {
		return nil, fmt.Errorf("expired cleanup workspace configuration unsupported")
	}
	root, err := manager.DefaultRoot()
	if err != nil || strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("expired cleanup workspace configuration unavailable")
	}
	selectedRecords := []state.TaskRecord{}
	selectedRefs := []state.SourceRefRecord{}
	for _, record := range records {
		if record.ID == recordID {
			selectedRecords = append(selectedRecords, record)
		}
	}
	for _, ref := range refs {
		if ref.TaskRecordID == recordID {
			selectedRefs = append(selectedRefs, ref)
		}
	}
	now := time.Now()
	plan, err := workspacegc.InspectPlan(selectedRecords, selectedRefs, now, workspacegc.Options{WorkspaceRoot: root})
	if err != nil {
		return nil, fmt.Errorf("expired cleanup GC planner unavailable")
	}
	var candidate *workspacegc.Candidate
	for _, entry := range plan.Candidates {
		if entry.RecordID != recordID || entry.WorkspaceID != group.ID || filepath.Clean(entry.Path) != filepath.Clean(group.Path) {
			continue
		}
		if candidate != nil {
			return nil, fmt.Errorf("expired cleanup candidate ambiguous")
		}
		copy := entry
		candidate = &copy
	}
	if candidate == nil {
		return nil, fmt.Errorf("workspace is not a current cached GC candidate; inspect gc_plan")
	}
	mode, err := workspacegc.CandidateMode(*candidate, now)
	if err != nil || mode != integration.CleanupExpired {
		return nil, fmt.Errorf("workspace is not in expired cleanup stage; inspect gc_plan")
	}
	return candidate, nil
}
