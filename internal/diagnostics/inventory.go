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
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

const maxPageBytes = 256 * 1024
const maxChildren = 16

func Read(store *state.Store, req protocol.DiagnosticsRequest, pending bool) (protocol.DiagnosticsPage, error) {
	if req.Section != "tasks" && req.Section != "workspaces" {
		return protocol.DiagnosticsPage{}, fmt.Errorf("unsupported diagnostic inventory")
	}
	if req.Limit == 0 {
		req.Limit = 20
	}
	if req.Limit < 1 || req.Limit > 50 || len(req.Cursor) > 512 || len(req.ID) > 512 || len(req.State) > 64 || len(req.Workspace) > 4096 {
		return protocol.DiagnosticsPage{}, fmt.Errorf("diagnostic selector exceeds bounds")
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
	if req.Section == "tasks" {
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
			task := taskView(record, byRecord[record.ID])
			items = append(items, protocol.DiagnosticItem{ID: task.RecordID, Task: &task})
		}
	} else {
		root, err := workspace.DefaultRoot()
		if err != nil {
			return protocol.DiagnosticsPage{}, fmt.Errorf("workspace registration unavailable")
		}
		registry, err := workspacegroup.Load(root)
		if err != nil {
			return protocol.DiagnosticsPage{}, fmt.Errorf("workspace registration unavailable")
		}
		paths := make([]string, 0, len(registry.Workspaces))
		for _, group := range registry.Workspaces {
			paths = append(paths, group.Path)
		}
		owners, ownerErr := agentservice.WorkspaceRemovalOwners(paths)
		for _, group := range registry.Workspaces {
			if req.ID != "" && req.ID != group.ID {
				continue
			}
			if req.Workspace != "" && filepath.Clean(req.Workspace) != filepath.Clean(group.Path) {
				continue
			}
			item := protocol.DiagnosticWorkspace{ID: clip(group.ID), Path: clip(group.Path), Registered: true, Feedback: group.Feedback, FeedbackOrigin: clip(group.FeedbackOrigin), Mounts: []protocol.DiagnosticMount{}, Ports: []protocol.PortForward{}, TaskIDs: []int{}, Members: []protocol.DiagnosticMember{}, AgentOwners: []string{}, Retention: []protocol.DiagnosticRetention{}, RuntimeEvidence: "unknown_not_probed", AgentOwnershipEvidence: "fresh_registration", ExecutionEligibility: "unknown_planner_not_run"}
			if ownerErr != nil {
				item.AgentOwnershipEvidence = "unavailable"
			} else {
				for _, id := range owners[group.Path] {
					if len(item.AgentOwners) == maxChildren {
						item.Truncated = true
						break
					}
					item.AgentOwners = append(item.AgentOwners, clip(id))
				}
			}
			if len(item.AgentOwners) > 0 {
				item.ExecutionEligibility = "blocked_registered_agent"
			}
			item.NotePath, item.SessionName = clip(group.NotePath), clip(group.SessionName)
			if group.Sandbox != nil {
				item.SandboxName, item.SharedDirectory = clip(group.Sandbox.Name), clip(group.Sandbox.SharedDirectory)
				for _, mount := range group.Sandbox.Mounts {
					if len(item.Mounts) == maxChildren {
						item.Truncated = true
						break
					}
					item.Mounts = append(item.Mounts, protocol.DiagnosticMount{Path: clip(mount)})
				}
				for _, mount := range group.Sandbox.AdditionalMounts {
					if len(item.Mounts) == maxChildren {
						item.Truncated = true
						break
					}
					item.Mounts = append(item.Mounts, protocol.DiagnosticMount{Path: clip(mount.Path), ReadOnly: mount.ReadOnly})
				}
				for _, port := range group.Sandbox.Ports {
					if len(item.Ports) == maxChildren {
						item.Truncated = true
						break
					}
					item.Ports = append(item.Ports, protocol.PortForward{HostPort: port.HostPort, SandboxPort: port.SandboxPort})
				}
			}
			for _, member := range group.Members {
				if len(item.Members) == maxChildren {
					item.Truncated = true
					break
				}
				item.Members = append(item.Members, protocol.DiagnosticMember{Path: clip(member.Path), Repository: clip(member.Repository)})
			}
			for _, record := range records {
				if !recordInWorkspace(byRecord[record.ID], group.Path) {
					continue
				}
				if req.State != "" && req.State != record.State {
					continue
				}
				if len(item.TaskIDs) == maxChildren {
					item.Truncated = true
					break
				}
				item.TaskIDs = append(item.TaskIDs, record.NumericID)
				task := productionTask(record, byRecord[record.ID])
				retention := protocol.DiagnosticRetention{TaskID: record.NumericID, State: clip(record.State), DoneAt: clip(record.DoneAt), Activity: protocol.SourceRefsActivity(task.SourceRefs).String(), Review: record.AgentReviewPending}
				for _, ref := range task.SourceRefs {
					retention.Review = retention.Review || ref.AgentReview
				}
				if expiry, ok := workspacegc.ExpiryAt(task); ok {
					retention.ExpiryAt = expiry.UTC().Format(time.RFC3339)
				}
				item.Retention = append(item.Retention, retention)
			}
			if req.State != "" && len(item.TaskIDs) == 0 {
				continue
			}
			items = append(items, protocol.DiagnosticItem{ID: item.ID, Workspace: &item})
		}
		evidence = "fresh_registration_with_cached_task_ownership_including_history"
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return paginate(req, items, revision, pending, evidence)
}

func taskView(record state.TaskRecord, refs []state.SourceRefRecord) protocol.DiagnosticTask {
	task := productionTask(record, refs)
	inputs := state.LifecycleInputs(record, refs)
	view := protocol.DiagnosticTask{RecordID: clip(record.ID), TaskID: record.NumericID, State: clip(record.State), DoneAt: clip(record.DoneAt), UpdatedAt: clip(record.UpdatedAt), Activity: protocol.SourceRefsActivity(task.SourceRefs).String(), Review: record.AgentReviewPending, Acknowledged: record.Ack.Cursor != "", DecisionReasonAvailable: record.Reason != "", SourceCount: len(refs), Sources: []protocol.DiagnosticRef{}}
	view.DecisionEvidence = "cached_state_no_active_work_item_inputs"
	if len(inputs) > 0 {
		view.DecisionEvidence = "cached_state_with_production_selected_work_item_inputs"
	}
	for _, record := range refs {
		if record.Active {
			view.Review = view.Review || record.Snapshot.AgentReview
		}
		if len(view.Sources) == maxChildren {
			view.Truncated = true
			continue
		}
		ref := record.Snapshot
		view.Sources = append(view.Sources, protocol.DiagnosticRef{LifecycleInput: inputs[record.ID], ID: clip(ref.ID), Source: clip(ref.Source), Kind: clip(ref.Kind), Role: ref.Role, Lifecycle: ref.Lifecycle, Authority: ref.Authority, Active: record.Active, ObservedAt: clip(record.ObservedAt), Path: clip(ref.Path), WorkspaceID: clip(ref.WorkspaceID), AgentID: clip(ref.AgentID), Signal: clip(ref.Signal), Activity: ref.Activity.String(), Authored: ref.Authored, Review: ref.AgentReview, Muted: ref.Muted, BindingCount: len(ref.Bindings), BindingProblem: ref.BindingError != "", CleanupProblemCount: len(ref.CleanupIssues)})
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

func clip(value string) string {
	const max = 512
	if len(value) > max {
		return strings.ToValidUTF8(value[:max], "�") + "…"
	}
	return strings.ToValidUTF8(value, "�")
}

func paginate(req protocol.DiagnosticsRequest, items []protocol.DiagnosticItem, revision int64, pending bool, evidence string) (protocol.DiagnosticsPage, error) {
	cursor := req.Cursor
	req.Cursor = ""
	raw, _ := json.Marshal(struct {
		Request  protocol.DiagnosticsRequest
		Items    []protocol.DiagnosticItem
		Revision int64
		Pending  bool
	}{req, items, revision, pending})
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
