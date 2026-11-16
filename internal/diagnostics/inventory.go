// Package diagnostics projects bounded, allowlisted observations. It never
// collects providers, starts runtimes, reads private logs or executes maintenance.
package diagnostics

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

const maxPageBytes = 256 * 1024
const maxChildren = protocol.DiagnosticChildLimit

func Read(store *state.Store, req protocol.DiagnosticsRequest, pending bool, providers integration.Registry) (protocol.DiagnosticsPage, error) {
	if req.Section != "tasks" && req.Section != "workspaces" && req.Section != "workspace_resources" && req.Section != "source_refs" && req.Section != "bindings" && req.Section != "agents" && req.Section != "agent_schedules" {
		return protocol.DiagnosticsPage{}, fmt.Errorf("unsupported diagnostic inventory")
	}
	if req.Limit == 0 {
		req.Limit = 20
	}
	if req.Limit < 1 || req.Limit > 50 || len(req.Cursor) > 512 || len(req.ID) > 512 || len(req.ParentID) > 512 || len(req.State) > 64 || len(req.Workspace) > 4096 {
		return protocol.DiagnosticsPage{}, fmt.Errorf("diagnostic selector exceeds bounds")
	}
	if req.ParentID != "" && req.Section != "source_refs" && req.Section != "bindings" && req.Section != "workspace_resources" && req.Section != "agent_schedules" {
		return protocol.DiagnosticsPage{}, fmt.Errorf("parent_id requires source_refs, bindings, workspace_resources or agent_schedules")
	}
	if req.Section == "workspace_resources" && req.State != "" {
		return protocol.DiagnosticsPage{}, fmt.Errorf("state filters retained task lifecycle, not workspace resources")
	}
	if (req.Section == "agents" || req.Section == "agent_schedules") && req.State != "" {
		return protocol.DiagnosticsPage{}, fmt.Errorf("state filters retained task lifecycle, not agent runner state")
	}
	if req.Workspace != "" && !filepath.IsAbs(req.Workspace) {
		return protocol.DiagnosticsPage{}, fmt.Errorf("diagnostic scope must be absolute")
	}
	records, refs, revision := store.InspectionSnapshot()
	byRecord := map[string][]state.SourceRefRecord{}
	for _, ref := range refs {
		byRecord[ref.TaskRecordID] = append(byRecord[ref.TaskRecordID], ref)
	}
	for id := range byRecord {
		sort.Slice(byRecord[id], func(i, j int) bool { return byRecord[id][i].ID < byRecord[id][j].ID })
	}
	items := []protocol.DiagnosticItem{}
	evidence := "cached_daemon_state"
	now := time.Now()
	if req.Section == "tasks" {
		linking := state.LinkingInputs(records, refs)
		for _, record := range records {
			if req.ID != "" && req.ID != record.ID && req.ID != strconv.Itoa(record.NumericID) {
				continue
			}
			if req.State != "" && req.State != record.State {
				continue
			}
			if req.Workspace != "" && !recordInWorkspace(byRecord[record.ID], req.Workspace) {
				continue
			}
			task := taskView(record, byRecord[record.ID], linking[record.ID], now)
			items = append(items, protocol.DiagnosticItem{ID: task.RecordID, Task: &task})
		}
	} else if req.Section == "agents" {
		agents, err := agentservice.DiagnosticAgents(req.Workspace, req.ID)
		if err != nil {
			return protocol.DiagnosticsPage{}, fmt.Errorf("agent registration diagnostics unavailable")
		}
		for _, agent := range agents {
			items = append(items, protocol.DiagnosticItem{ID: agent.ID, Agent: &agent})
		}
		evidence = "fresh_registration_with_persisted_agent_checkpoints"
	} else if req.Section == "agent_schedules" {
		schedules, err := agentservice.DiagnosticAgentSchedules(req.Workspace, req.ParentID, req.ID)
		if err != nil {
			return protocol.DiagnosticsPage{}, fmt.Errorf("agent schedule checkpoint diagnostics unavailable")
		}
		for _, schedule := range schedules {
			items = append(items, protocol.DiagnosticItem{ID: schedule.ID, AgentSchedule: &schedule})
		}
		evidence = "fresh_schedule_checkpoints_runtime_errors_unknown"
	} else if req.Section == "source_refs" || req.Section == "bindings" {
		items = referenceItems(req, records, refs, byRecord)
	} else {
		var err error
		items, evidence, err = workspaceItems(req, records, byRecord, now, providers)
		if err != nil {
			return protocol.DiagnosticsPage{}, err
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return paginate(req, items, revision, pending, evidence)
}

func taskView(record state.TaskRecord, refs []state.SourceRefRecord, linking state.LinkingEvidence, now time.Time) protocol.DiagnosticTask {
	task := productionTask(record, refs)
	inputs := state.LifecycleInputs(record, refs)
	view := protocol.DiagnosticTask{RecordID: clip(record.ID), TaskID: record.NumericID, State: clip(record.State), DoneAt: clip(record.DoneAt), UpdatedAt: clip(record.UpdatedAt), Activity: protocol.SourceRefsActivity(task.SourceRefs).String(), Review: record.AgentReviewPending, Acknowledged: record.Ack.Cursor != "", DecisionReasonAvailable: record.Reason != "", SourceCount: len(refs), Sources: []protocol.DiagnosticRef{}}
	decision := state.InspectLifecycle(record, refs)
	view.Lifecycle = protocol.DiagnosticLifecycle{
		Evidence: "production_lifecycle_evaluation_on_cached_inputs",
		Decision: decision.Decision, SourceState: decision.SourceState,
		WorkItemInputs: decision.WorkItemInputs, DoneWorkItemInputs: decision.DoneWorkItemInputs,
		FallbackWorkItemInputs: decision.FallbackWorkItemInputs, AuthoritativeRefs: decision.AuthoritativeRefs,
		ActivityHolds: decision.ActivityHolds, ReviewHolds: decision.ReviewHolds,
		RecordedReviewPending: decision.RecordedReviewPending, NextReviewPending: decision.NextReviewPending,
	}
	for _, record := range refs {
		if record.Active {
			view.Review = view.Review || record.Snapshot.AgentReview
		}
		if len(view.Sources) == maxChildren {
			view.Truncated = true
			continue
		}
		view.Sources = append(view.Sources, sourceView(record, view.RecordID, view.TaskID, inputs[record.ID]))
	}
	view.AuthoredIdentityCount = linking.AuthoredIdentities
	view.LinkingComponentRecords = linking.ConnectedRecords
	view.LinkingBlocked = linking.Blocked
	view.GCRetention = gcRetentionView(record, refs, now)
	return view
}

func gcRetentionView(record state.TaskRecord, refs []state.SourceRefRecord, now time.Time) protocol.DiagnosticGCRetention {
	gate := workspacegc.InspectRetention(record, refs, now)
	view := protocol.DiagnosticGCRetention{Decision: gate.Decision, Evidence: "production_gc_retention_gate_only"}
	if !gate.ReadyAt.IsZero() {
		view.ReadyAt = gate.ReadyAt.UTC().Format(time.RFC3339Nano)
	}
	return view
}

func productionTask(record state.TaskRecord, refs []state.SourceRefRecord) protocol.Task {
	task := protocol.Task{ID: record.NumericID, Attention: record.State, DoneAt: record.DoneAt}
	for _, ref := range refs {
		if ref.Active {
			task.SourceRefs = append(task.SourceRefs, ref.Snapshot)
		}
	}
	return task
}

func recordInWorkspace(refs []state.SourceRefRecord, path string) bool {
	for _, record := range refs {
		ref := record.Snapshot
		for _, candidate := range []string{ref.Path, ref.Metadata["workspace_path"]} {
			if candidate == "" {
				continue
			}
			rel, err := filepath.Rel(filepath.Clean(path), filepath.Clean(candidate))
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				return true
			}
		}
	}
	return false
}

func clip(value string) string { return protocol.DiagnosticText(value) }

func paginate(req protocol.DiagnosticsRequest, items []protocol.DiagnosticItem, revision int64, pending bool, evidence string) (protocol.DiagnosticsPage, error) {
	cursor := req.Cursor
	req.Cursor = ""
	raw, _ := json.Marshal(struct {
		Request  protocol.DiagnosticsRequest
		Items    []protocol.DiagnosticItem
		Revision int64
		Pending  bool
		Evidence string
	}{req, items, revision, pending, evidence})
	hash := sha256.Sum256(raw)
	snapshot := hex.EncodeToString(hash[:])
	offset := 0
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return protocol.DiagnosticsPage{}, fmt.Errorf("invalid diagnostic cursor")
		}
		parts := strings.Split(string(decoded), ":")
		if len(parts) != 2 || parts[0] != snapshot {
			return protocol.DiagnosticsPage{}, fmt.Errorf("diagnostic snapshot changed; restart pagination")
		}
		offset, err = strconv.Atoi(parts[1])
		if err != nil || offset < 0 || offset > len(items) {
			return protocol.DiagnosticsPage{}, fmt.Errorf("invalid diagnostic cursor offset")
		}
	}
	page := protocol.DiagnosticsPage{Schema: protocol.DiagnosticsVersion, Section: req.Section, Evidence: evidence, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Revision: revision, Snapshot: snapshot, Total: len(items), ResourceRefreshPending: pending, Limitations: []string{"free_form_content_withheld", "runtime_not_probed", "cleanup_planner_not_run", "provider_freshness_not_established", "text_fields_limited_to_512_bytes", "nested_lists_limited_to_16_entries", "historical_manual_intent_not_reconstructed", "registration_paths_are_not_observed_resource_presence"}}
	end := min(offset+req.Limit, len(items))
	for {
		page.Items = items[offset:end]
		page.Complete = end == len(items)
		page.NextCursor = ""
		if !page.Complete {
			page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(snapshot + ":" + strconv.Itoa(end)))
		}
		output, _ := json.Marshal(page)
		if len(output) <= maxPageBytes {
			return page, nil
		}
		end--
		if end <= offset {
			return protocol.DiagnosticsPage{}, fmt.Errorf("diagnostic item exceeds output bound")
		}
	}
}
