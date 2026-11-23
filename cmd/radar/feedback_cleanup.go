package main

import (
	"errors"
	"time"

	"radar/internal/client"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
	"radar/internal/socket"
)

// Manual cleanup has one complete-plan flow, not an inventory/page executor.
// Registered scope selects the task; native cleanup still covers its whole bundle.
func feedbackManualCleanup(current, scope, parent, mode, plan string) (map[string]any, error) {
	if (scope != "origin" && scope != "feedback" && scope != "installation") || parent == "" || len(parent) > 512 || (mode != "preview" && mode != "apply") || (mode == "preview" && plan != "") || (mode == "apply" && !validFeedbackPlanID(plan)) {
		return nil, errors.New("manual cleanup requires exact task scope and complete preview or exact-plan apply")
	}
	root, group, found, err := workspace.RegisteredWorkspace(current, "")
	if err != nil || !found || !group.Feedback {
		return nil, errors.New("manual cleanup requires a registered Radar feedback workspace")
	}
	result := map[string]any{"diagnostics_schema": protocol.DiagnosticsVersion, "section": "cleanup_preview", "scope": scope, "available": false, "feedback_workspace_id": group.ID, "evidence": "unavailable"}
	req := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "cleanup", Mode: mode, Scope: scope, ParentID: parent, PlanID: plan, FeedbackWorkspace: group.Path}
	scopeID := ""
	if scope == "feedback" {
		req.Workspace = group.Path
		scopeID = group.ID
	}
	if scope == "origin" {
		_, origin, exists, err := workspace.RegisteredWorkspace(group.FeedbackOrigin, root)
		if group.FeedbackOrigin == "" || err != nil || !exists || origin.Path != group.FeedbackOrigin {
			result["reason"] = "origin_not_registered"
			return result, nil
		}
		req.Workspace, scopeID = origin.Path, origin.ID
		result["origin_workspace_id"] = origin.ID
	}
	path, err := socket.Path()
	if err != nil {
		result["reason"] = "daemon_unavailable"
		return result, nil
	}
	response, err := client.MaintainDiagnostics(path, req, 32*time.Second)
	if err != nil {
		result["reason"] = "cleanup_outcome_unknown_do_not_replay"
		return result, nil
	}
	view := response.DiagnosticCleanup
	if !response.OK || view == nil || !protocol.ValidDiagnosticCleanup(*view, req) || view.FeedbackWorkspaceID != group.ID || view.ScopeWorkspaceID != scopeID {
		return nil, errors.New("native manual cleanup unavailable or exact plan changed; outcome may be unknown, do not replay or automatically retry")
	}
	result["available"], result["evidence"], result["cleanup"] = true, view.Evidence, view
	return result, nil
}
