package main

import (
	"errors"
	"strconv"
	"time"

	"radar/internal/client"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
	"radar/internal/socket"
)

// feedbackCleanupCheck has no fallback to a probe, ordinary cleanup or refresh.
// Paths are resolved from exact current registrations, never client targets.
func feedbackCleanupCheck(current, scope, parent string) (map[string]any, error) {
	if (scope != "origin" && scope != "feedback" && scope != "installation") || parent == "" || len(parent) > 512 {
		return nil, errors.New("cleanup guard check requires one exact retained task parent and registered scope")
	}
	root, group, found, err := workspace.RegisteredWorkspace(current, "")
	if err != nil || !found || !group.Feedback {
		return nil, errors.New("cleanup guard checks require a registered Radar feedback workspace")
	}
	result := map[string]any{"diagnostics_schema": protocol.DiagnosticsVersion, "section": "cleanup_check", "scope": scope, "available": false, "feedback_workspace_id": group.ID, "evidence": "unavailable"}
	req := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "cleanup_check", Mode: "check", Scope: scope, ParentID: parent}
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
	response, err := client.MaintainDiagnostics(path, req, 32*time.Second)
	if err != nil {
		result["reason"] = "check_outcome_unknown_do_not_replay"
		return result, nil
	}
	if !response.OK || response.CleanupCheck == nil {
		return nil, errors.New("native cleanup guard check unavailable; coordination/Git metadata effects may remain; do not automatically retry")
	}
	view := response.CleanupCheck
	if view.Schema != protocol.DiagnosticsVersion || view.Operation != "cleanup_check" || view.Mode != "manual" || view.Scope != scope || (parent != view.RecordID && parent != strconv.Itoa(view.TaskID)) {
		return nil, errors.New("cleanup guard check version or exact-target mismatch; do not automatically retry")
	}
	result["available"], result["evidence"], result["cleanup_check"] = true, view.Evidence, view
	return result, nil
}
