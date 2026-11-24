package diagnostics

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

type workspaceTask struct {
	recordID string
	view     protocol.DiagnosticRetention
}

func workspaceItems(req protocol.DiagnosticsRequest, records []state.TaskRecord, byRecord map[string][]state.SourceRefRecord, now time.Time, providers integration.Registry) ([]protocol.DiagnosticItem, string, error) {
	provider, err := providers.WorkspaceDiagnostics()
	if err != nil {
		return nil, "", fmt.Errorf("workspace registration diagnostics unsupported")
	}
	groups, err := provider.DiagnosticWorkspaceRegistrations()
	if err != nil {
		return nil, "", fmt.Errorf("workspace registration unavailable")
	}
	selected := []protocol.DiagnosticWorkspace{}
	paths := []string{}
	for _, group := range groups {
		if req.Workspace != "" && filepath.Clean(req.Workspace) != filepath.Clean(group.Path) {
			continue
		}
		if req.Section == "workspaces" && req.ID != "" && req.ID != group.ID {
			continue
		}
		if req.Section == "workspace_resources" && req.ParentID != "" && req.ParentID != group.ID {
			continue
		}
		selected = append(selected, group)
		paths = append(paths, group.Path)
	}
	// Scope first: an empty selection does not need a registration scan. Reuse
	// the preview scanner's fixed read/entry/aggregate bounds, never production's
	// unrestricted loader, while retaining full owner IDs for paged resources.
	var owners map[string][]string
	var ownerErr error
	if len(paths) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), RuntimeProbeTimeout)
		defer cancel()
		owners, ownerErr = agentservice.DiagnosticWorkspaceOwnerIDs(ctx, paths)
	}
	evidence := "fresh_registration_with_cached_task_ownership_including_history"
	if ownerErr != nil {
		evidence = "fresh_registration_with_cached_task_ownership_agent_ownership_unavailable"
	}
	items := []protocol.DiagnosticItem{}
	for _, group := range selected {
		tasks := []workspaceTask{}
		for _, record := range records {
			if !recordInWorkspace(byRecord[record.ID], group.Path) || (req.State != "" && req.State != record.State) {
				continue
			}
			tasks = append(tasks, workspaceTask{recordID: record.ID, view: workspaceRetention(record, byRecord[record.ID], now)})
		}
		if req.Section == "workspace_resources" {
			items = append(items, workspaceResources(req.ID, group, tasks, owners[group.Path])...)
			continue
		}
		if req.State != "" && len(tasks) == 0 {
			continue
		}
		view := workspaceSummary(group, tasks, owners[group.Path], ownerErr == nil)
		items = append(items, protocol.DiagnosticItem{ID: view.ID, Workspace: &view})
	}
	return items, evidence, nil
}

func workspaceRetention(record state.TaskRecord, refs []state.SourceRefRecord, now time.Time) protocol.DiagnosticRetention {
	task := productionTask(record, refs)
	view := protocol.DiagnosticRetention{GCRetention: gcRetentionView(record, refs, now), TaskID: record.NumericID, State: clip(record.State), DoneAt: clip(record.DoneAt), Activity: protocol.SourceRefsActivity(task.SourceRefs).String(), Review: record.AgentReviewPending}
	for _, ref := range task.SourceRefs {
		view.Review = view.Review || ref.AgentReview
	}
	if expiry, ok := workspacegc.ExpiryAt(task); ok {
		view.ExpiryAt = expiry.UTC().Format(time.RFC3339)
	}
	return view
}

func workspaceSummary(group protocol.DiagnosticWorkspace, tasks []workspaceTask, owners []string, ownershipKnown bool) protocol.DiagnosticWorkspace {
	view := group
	view.ID, view.Path = clip(group.ID), clip(group.Path)
	view.SandboxName, view.SessionName = clip(group.SandboxName), clip(group.SessionName)
	view.MemberCount, view.MountCount, view.PortCount, view.TaskCount = len(group.Members), len(group.Mounts), len(group.Ports), len(tasks)
	view.Members, view.Mounts, view.Ports = []protocol.DiagnosticMember{}, []protocol.DiagnosticMount{}, []protocol.PortForward{}
	view.TaskIDs, view.AgentOwners, view.Retention = []int{}, []string{}, []protocol.DiagnosticRetention{}
	view.RuntimeEvidence, view.ExecutionEligibility = "unknown_not_probed", "unknown_planner_not_run"
	view.AgentOwnershipEvidence = "unavailable"
	view.AgentOwnerCount = nil
	view.Truncated = len(group.Members) > maxChildren || len(group.Mounts) > maxChildren || len(group.Ports) > maxChildren || len(tasks) > maxChildren
	for _, member := range group.Members[:min(len(group.Members), maxChildren)] {
		view.Members = append(view.Members, protocol.DiagnosticMember{Path: clip(member.Path), Repository: clip(member.Repository)})
	}
	for _, mount := range group.Mounts[:min(len(group.Mounts), maxChildren)] {
		view.Mounts = append(view.Mounts, protocol.DiagnosticMount{Path: clip(mount.Path), ReadOnly: mount.ReadOnly})
	}
	view.Ports = append(view.Ports, group.Ports[:min(len(group.Ports), maxChildren)]...)
	if ownershipKnown {
		count := len(owners)
		view.AgentOwnerCount = &count
		view.AgentOwnershipEvidence = "fresh_registration"
		view.Truncated = view.Truncated || count > maxChildren
		for _, id := range owners[:min(count, maxChildren)] {
			view.AgentOwners = append(view.AgentOwners, clip(id))
		}
		if count > 0 {
			view.ExecutionEligibility = "blocked_registered_agent"
		}
	}
	for _, task := range tasks[:min(len(tasks), maxChildren)] {
		view.TaskIDs = append(view.TaskIDs, task.view.TaskID)
		view.Retention = append(view.Retention, task.view)
	}
	return view
}

func workspaceResources(id string, group protocol.DiagnosticWorkspace, tasks []workspaceTask, owners []string) []protocol.DiagnosticItem {
	items := []protocol.DiagnosticItem{}
	seen := map[string]*protocol.DiagnosticWorkspaceResource{}
	add := func(view protocol.DiagnosticWorkspaceResource) {
		// Hash complete identity before clipping. Volatile retention decisions
		// are deliberately excluded so a task relationship keeps its ID.
		view.ID = protocol.DiagnosticWorkspaceResourceID(group.ID, view)
		if id != "" && id != view.ID {
			return
		}
		if prior := seen[view.ID]; prior != nil {
			prior.Occurrences++
			return
		}
		view.WorkspaceID, view.RecordID, view.AgentID = clip(group.ID), clip(view.RecordID), clip(view.AgentID)
		view.Occurrences = 1
		if view.Member != nil {
			view.Member.Path, view.Member.Repository = clip(view.Member.Path), clip(view.Member.Repository)
		}
		if view.Mount != nil {
			view.Mount.Path = clip(view.Mount.Path)
		}
		seen[view.ID] = &view
		items = append(items, protocol.DiagnosticItem{ID: view.ID, WorkspaceResource: &view})
	}
	for _, member := range group.Members {
		add(protocol.DiagnosticWorkspaceResource{Kind: "member", Evidence: "fresh_registration", Member: &member})
	}
	for _, mount := range group.Mounts {
		add(protocol.DiagnosticWorkspaceResource{Kind: "mount", Evidence: "fresh_registration", Mount: &mount})
	}
	for _, port := range group.Ports {
		add(protocol.DiagnosticWorkspaceResource{Kind: "port", Evidence: "fresh_registration", Port: &port})
	}
	for _, task := range tasks {
		add(protocol.DiagnosticWorkspaceResource{Kind: "task_owner", Evidence: "cached_task_ownership_including_history", RecordID: task.recordID, Task: &task.view})
	}
	for _, owner := range owners {
		add(protocol.DiagnosticWorkspaceResource{Kind: "agent_owner", Evidence: "fresh_registration", AgentID: owner})
	}
	return items
}
