package main

import (
	"errors"
	"flag"
	"runtime"
	"strings"
	"time"

	"radar/internal/client"
	"radar/internal/config"
	"radar/internal/integration/workspace"
	"radar/internal/logging"
	"radar/internal/protocol"
	"radar/internal/socket"
	"radar/internal/version"
)

// Fixed feedback host boundary. Reads/probes never imply mutation. Only explicit
// collection-only refresh apply may mutate through the dedicated native adapter.
// No daemon startup, ordinary refresh/GC fallback, raw state or secret export.
func runFeedbackDiagnostics(args []string) {
	flags := outputFlags("radar feedback-diagnostics")
	current := flags.String("workspace", "", "registered feedback workspace")
	section := flags.String("section", "overview", "overview, tasks, workspaces, source_refs, bindings, workspace_resources, agents, agent_schedules, gc_plan, runtime_probe, agent_probe, retirement_preview, cleanup_preview, integrations, operations, refresh or logs")
	scope := flags.String("scope", "origin", "inventories: origin, feedback or installation; refresh requires installation")
	id := flags.String("id", "", "stable entity ID filter")
	parentID := flags.String("parent-id", "", "source_refs: task ID; bindings: retained source-ref ID; workspace_resources: registered workspace ID; agent_schedules: agent registration ID; gc_plan: owning task ID; runtime_probe/retirement_preview: required registered workspace ID; agent_probe: required agent registration ID; cleanup_preview: required retained task ID")
	cleanupMode := flags.String("cleanup-mode", "", "cleanup_preview only: manual (default) or expired")
	workspaceID := flags.String("workspace-id", "", "expired cleanup_preview only: exact registered GC candidate workspace ID")
	action := flags.String("action", "preview", "refresh only: preview (default) or explicit apply")
	planID := flags.String("plan-id", "", "refresh apply only: exact fresh preview ID, not consent")
	state := flags.String("state", "", "exact retained task state filter")
	limit := flags.Int("limit", 20, "page size (inventories 1-50; logs 1-100)")
	cursor := flags.String("cursor", "", "opaque inventory/log continuation cursor")
	since := flags.String("since", "", "logs: inclusive native daemon wall time YYYY-MM-DD HH:MM:SS")
	until := flags.String("until", "", "logs: inclusive native daemon wall time YYYY-MM-DD HH:MM:SS")
	severity := flags.String("severity", "", "logs: exact debug, info, warn or error")
	provider := flags.String("provider", "", "logs: opaque provider identity from log metadata")
	entity := flags.String("entity", "", "logs: opaque entity identity from log metadata")
	operation := flags.String("operation", "", "logs: opaque operation/correlation identity from log metadata")
	_ = parseFlags(flags, args)
	if flags.NArg() != 0 || *current == "" || (*section != "refresh" && *section != "overview" && *section != "logs" && *section != "tasks" && *section != "workspaces" && *section != "source_refs" && *section != "bindings" && *section != "workspace_resources" && *section != "agents" && *section != "agent_schedules" && *section != "gc_plan" && *section != "runtime_probe" && *section != "agent_probe" && *section != "cleanup_preview" && *section != "retirement_preview" && *section != "integrations" && *section != "operations") {
		fatal(errors.New("usage: radar feedback-diagnostics --workspace <feedback workspace> --section overview|tasks|workspaces|source_refs|bindings|workspace_resources|agents|agent_schedules|gc_plan|runtime_probe|agent_probe|retirement_preview|cleanup_preview|integrations|operations|refresh|logs --json"))
	}
	if !jsonOutput {
		fatal(errors.New("feedback-diagnostics requires --json"))
	}
	invalidSelector := false
	flags.Visit(func(f *flag.Flag) {
		if *section == "refresh" {
			switch f.Name {
			case "workspace", "section", "scope", "action", "plan-id", "json":
			default:
				invalidSelector = true
			}
		} else if f.Name == "action" || f.Name == "plan-id" {
			invalidSelector = true
		}
		if *section != "cleanup_preview" && (f.Name == "cleanup-mode" || f.Name == "workspace-id") {
			invalidSelector = true
		}
		if *section == "logs" && (f.Name == "scope" || f.Name == "id" || f.Name == "parent-id" || f.Name == "state") {
			invalidSelector = true
		}
		if *section != "logs" && (f.Name == "since" || f.Name == "until" || f.Name == "severity" || f.Name == "provider" || f.Name == "entity" || f.Name == "operation") {
			invalidSelector = true
		}
	})
	if invalidSelector {
		fatal(errors.New("diagnostic selectors do not match the selected section"))
	}
	if *section == "refresh" {
		result, err := feedbackRefresh(*current, *scope, protocol.DiagnosticMaintenanceRequest{Operation: "refresh", Mode: *action, PlanID: *planID})
		if err != nil {
			fatal(err)
		}
		printResult(result)
		return
	}
	query := logging.LogQuery{Since: *since, Until: *until, Severity: *severity, Provider: *provider, Entity: *entity, Operation: *operation, Limit: *limit, Cursor: *cursor}
	var result map[string]any
	var err error
	if *section == "tasks" || *section == "workspaces" || *section == "source_refs" || *section == "bindings" || *section == "workspace_resources" || *section == "agents" || *section == "agent_schedules" || *section == "gc_plan" || *section == "runtime_probe" || *section == "agent_probe" || *section == "cleanup_preview" || *section == "retirement_preview" || *section == "integrations" || *section == "operations" {
		result, err = feedbackInventory(*current, *scope, protocol.DiagnosticsRequest{Section: *section, ParentID: *parentID, ID: *id, State: *state, Limit: *limit, Cursor: *cursor, CleanupMode: *cleanupMode, WorkspaceID: *workspaceID})
	} else {
		if *scope != "origin" || *id != "" || *parentID != "" || *state != "" || (*section != "logs" && (*cursor != "" || *limit != 20)) {
			fatal(errors.New("inventory selectors require an inventory section"))
		}
		result, err = feedbackDiagnostics(*current, *section, query)
	}
	if err != nil {
		fatal(err)
	}
	printResult(result)
}

func feedbackDiagnostics(current, section string, logQuery logging.LogQuery) (map[string]any, error) {
	root, group, found, err := workspace.RegisteredWorkspace(current, "")
	if err != nil || !found || !group.Feedback {
		return nil, errors.New("diagnostics require a registered Radar feedback workspace")
	}
	if section == "logs" {
		page, err := logging.DiagnosticLogs(logQuery)
		if errors.Is(err, logging.ErrDiagnosticLogSnapshotChanged) {
			return map[string]any{"diagnostics_schema": protocol.DiagnosticsVersion, "section": section, "private": true, "scope": "installation", "available": false, "evidence": "unavailable", "reason": "snapshot_changed_restart_pagination"}, nil
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"diagnostics_schema": protocol.DiagnosticsVersion, "section": section, "private": true, "scope": "installation", "available": true, "evidence": "bounded_native_log_metadata_not_operation_history", "logs": page,
			"warning": "Private installation-wide log metadata only. Raw messages/errors/values are withheld. Missing evidence and completed timings are not success; approval does not authorize publication."}, nil
	}
	result := map[string]any{
		"diagnostics_schema": protocol.DiagnosticsVersion, "section": "overview", "version": version.Number, "commit": version.Commit,
		"built": version.Date, "os": runtime.GOOS, "arch": runtime.GOARCH,
		"daemon":       map[string]any{"available": false, "reason": "daemon_unavailable"},
		"capabilities": map[string]any{"sections": []string{"overview", "tasks", "workspaces", "source_refs", "bindings", "workspace_resources", "agents", "agent_schedules", "gc_plan", "runtime_probe", "agent_probe", "retirement_preview", "cleanup_preview", "integrations", "operations", "refresh", "logs"}, "maintenance": true, "collection_only_refresh": true, "cleanup_apply": false, "runtime_controls": false, "runtime_retirement_previews": true, "manual_cleanup_previews": true, "expiry_cleanup_previews": true, "cleanup_preview_lease_contention_probes": true, "runtime_probes": true, "agent_socket_probes": true, "current_daemon_agent_supervision": true, "agent_supervision_history": false, "runtime_probes_require_exact_registered_workspace": true, "retained_inventory_requires_matching_daemon": true, "integration_access_checks": false, "integration_collection_history": false, "current_daemon_collection_attempts": true, "current_daemon_managed_operations": true, "native_managed_handler_timing": true, "native_cleanup_completed_phase_aggregates": true, "native_cleanup_returned_target_summaries": true, "durable_operation_history": false, "verified_operation_resource_outcomes": false, "filtered_redacted_log_metadata": true, "raw_log_contents": false, "configuration_policy_origins": true, "daemon_process_configuration": true, "workspace_effective_policy": false, "daemon_environment": false},
	}
	// Native defaults and configuration origins share the exact production
	// decoder. No arbitrary configuration strings or referenced files escape.
	result["settings"] = config.DiagnosticSettings()
	result["feedback_sandbox_registered"] = group.Sandbox != nil
	if path, err := socket.Path(); err == nil {
		if response, err := client.InspectDiagnostics(path, protocol.DiagnosticsRequest{Section: "overview"}, time.Second); err == nil {
			result["daemon"] = map[string]any{"available": false, "reason": "matching_daemon_overview_required"}
			if response.OK && response.DiagnosticOverview != nil && response.DiagnosticOverview.Schema == protocol.DiagnosticsVersion && response.DiagnosticOverview.Settings.Scope == "daemon_process_global_defaults" && len(response.DiagnosticOverview.Providers) <= 32 {
				result["daemon"] = map[string]any{"available": true, "version": response.Version, "providers": response.DiagnosticOverview.Providers, "diagnostics_schema": response.DiagnosticOverview.Schema, "settings": response.DiagnosticOverview.Settings, "resource_refresh_pending": response.ResourceRefreshPending}
			}
		}
	}
	if group.FeedbackOrigin != "" {
		// Resolve again so an unregistered origin cannot become arbitrary
		// host filesystem access. Never enroll an unmanaged directory.
		_, origin, exists, err := workspace.RegisteredWorkspace(group.FeedbackOrigin, root)
		if err == nil && exists && origin.Path == group.FeedbackOrigin {
			members := make([]map[string]string, 0, len(origin.Members))
			for _, member := range origin.Members[:min(len(origin.Members), 16)] {
				members = append(members, map[string]string{"path": member.Path, "repository": member.Repository})
			}
			var sandbox any
			if origin.Sandbox != nil {
				sandbox = map[string]any{"name": origin.Sandbox.Name, "runtime_evidence": "unknown_not_probed"}
			}
			result["origin"] = map[string]any{"available": true, "id": origin.ID, "path": origin.Path, "members": members, "sandbox": sandbox, "evidence": "fresh_registration", "truncated": len(origin.Members) > 16}

		}
		if result["origin"] == nil {
			result["origin"] = map[string]any{"available": false}
		}
	}
	return result, nil
}

func feedbackInventory(current, scope string, req protocol.DiagnosticsRequest) (map[string]any, error) {
	if req.Section != "cleanup_preview" && (req.CleanupMode != "" || req.WorkspaceID != "") {
		return nil, errors.New("cleanup selectors require cleanup_preview")
	}
	if (req.CleanupMode != "" && req.CleanupMode != "manual" && req.CleanupMode != "expired") || len(req.WorkspaceID) > 512 || (req.CleanupMode == "expired" && req.WorkspaceID == "") || (req.CleanupMode != "expired" && req.WorkspaceID != "") {
		return nil, errors.New("invalid cleanup mode or registered workspace ID")
	}
	if scope != "origin" && scope != "feedback" && scope != "installation" {
		return nil, errors.New("invalid diagnostic scope")
	}
	if req.Limit < 1 || req.Limit > 50 || len(req.ID) > 512 || len(req.ParentID) > 512 || len(req.State) > 64 || len(req.Cursor) > 512 {
		return nil, errors.New("diagnostic selector exceeds bounds")
	}
	if req.ParentID != "" && req.Section != "source_refs" && req.Section != "bindings" && req.Section != "workspace_resources" && req.Section != "agent_schedules" && req.Section != "gc_plan" && req.Section != "runtime_probe" && req.Section != "agent_probe" && req.Section != "retirement_preview" && req.Section != "cleanup_preview" {
		return nil, errors.New("parent-id requires source_refs, bindings, workspace_resources, agent_schedules, gc_plan, runtime_probe, agent_probe, retirement_preview or cleanup_preview")
	}
	if (req.Section == "agents" || req.Section == "agent_schedules") && req.State != "" {
		return nil, errors.New("state filters retained task lifecycle, not agent runner state")
	}
	if (req.Section == "integrations" || req.Section == "operations") && req.State != "" {
		return nil, errors.New("state filters retained task lifecycle, not integrations or operations")
	}
	if req.Section == "workspace_resources" && req.State != "" {
		return nil, errors.New("state filters retained task lifecycle, not workspace resources")
	}
	if req.Section == "runtime_probe" && (req.ParentID == "" || req.State != "") {
		return nil, errors.New("runtime probe requires an exact registered workspace parent and no task-state filter")
	}
	if req.Section == "agent_probe" && (req.ParentID == "" || len(req.ParentID) > 64 || req.ID != "" || req.State != "" || req.Cursor != "") {
		return nil, errors.New("agent probe requires an exact registered agent parent and no row/state/cursor selectors")
	}
	if req.Section == "cleanup_preview" && (req.ParentID == "" || req.State != "") {
		return nil, errors.New("cleanup preview requires one exact retained task parent and no state filter")
	}
	if req.Section == "retirement_preview" && (req.ParentID == "" || req.ID != "" || req.State != "" || req.Cursor != "") {
		return nil, errors.New("retirement preview requires an exact registered workspace parent and no row/state/cursor selectors")
	}
	root, group, found, err := workspace.RegisteredWorkspace(current, "")
	if err != nil || !found || !group.Feedback {
		return nil, errors.New("diagnostics require a registered Radar feedback workspace")
	}
	result := map[string]any{"diagnostics_schema": protocol.DiagnosticsVersion, "section": req.Section, "scope": scope, "available": false, "feedback_workspace_id": group.ID, "evidence": "unavailable"}
	req.Workspace = "" // Only the registered scope below may supply a path.
	if scope == "feedback" {
		req.Workspace = group.Path
	}
	if scope == "origin" {
		if group.FeedbackOrigin == "" {
			result["reason"] = "origin_not_registered"
			return result, nil
		}
		_, origin, exists, err := workspace.RegisteredWorkspace(group.FeedbackOrigin, root)
		if err != nil || !exists || origin.Path != group.FeedbackOrigin {
			result["reason"] = "origin_not_registered"
			return result, nil
		}
		req.Workspace = origin.Path
		result["origin_workspace_id"] = origin.ID
	}
	path, err := socket.Path()
	if err != nil {
		result["reason"] = "daemon_unavailable"
		return result, nil
	}
	var response protocol.Response
	if req.Section == "cleanup_preview" {
		response, err = client.ProbeDiagnostics(path, req, 32*time.Second)
	} else if req.Section == "runtime_probe" || req.Section == "agent_probe" || req.Section == "retirement_preview" {
		response, err = client.ProbeDiagnostics(path, req, 7*time.Second)
	} else {
		response, err = client.InspectDiagnostics(path, req, 2*time.Second)
	}
	if err != nil {
		result["reason"] = "daemon_unavailable"
		return result, nil
	}
	if !response.OK && strings.HasPrefix(response.Error, "diagnostic snapshot changed;") {
		result["reason"] = "snapshot_changed_restart_pagination"
		return result, nil
	}
	if !response.OK || response.Diagnostics == nil {
		return nil, errors.New("diagnostic inventory unavailable: matching daemon/CLI support and a fresh pagination snapshot are required")
	}
	if response.Diagnostics.Schema != protocol.DiagnosticsVersion || response.Diagnostics.Section != req.Section {
		return nil, errors.New("diagnostic inventory version mismatch")
	}
	result["available"] = true
	result["evidence"] = response.Diagnostics.Evidence
	result["inventory"] = response.Diagnostics
	return result, nil
}
