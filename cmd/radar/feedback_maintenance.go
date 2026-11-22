package main

import (
	"errors"
	"time"

	"radar/internal/client"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
	"radar/internal/socket"
)

// feedbackRefresh never falls back to ordinary refresh: that daemon callback
// can also execute workspace GC. Inspection breadth never grants mutation scope.
func feedbackRefresh(current, scope string, req protocol.DiagnosticMaintenanceRequest) (map[string]any, error) {
	if scope != "installation" || req.ParentID != "" || req.Workspace != "" || req.Operation != "refresh" || (req.Mode != "preview" && req.Mode != "apply") || (req.Mode == "preview" && req.PlanID != "") || (req.Mode == "apply" && !validFeedbackPlanID(req.PlanID)) {
		return nil, errors.New("refresh requires explicit installation scope and preview or exact-plan apply")
	}
	_, group, found, err := workspace.RegisteredWorkspace(current, "")
	if err != nil || !found || !group.Feedback {
		return nil, errors.New("maintenance requires a registered Radar feedback workspace")
	}
	req.Schema, req.Scope = protocol.DiagnosticsVersion, "installation"
	result := map[string]any{"diagnostics_schema": protocol.DiagnosticsVersion, "section": "refresh", "scope": "installation", "available": false, "feedback_workspace_id": group.ID, "evidence": "unavailable"}
	path, err := socket.Path()
	if err != nil {
		result["reason"] = "daemon_unavailable"
		return result, nil
	}
	timeout := 2 * time.Second
	if req.Mode == "apply" {
		timeout = 32 * time.Second
	}
	response, err := client.MaintainDiagnostics(path, req, timeout)
	if err != nil {
		result["reason"] = "daemon_unavailable"
		if req.Mode == "apply" {
			result["reason"] = "request_outcome_unknown_do_not_replay"
		}
		return result, nil
	}
	if !response.OK || response.Maintenance == nil {
		return nil, errors.New("collection-only maintenance unavailable or preview changed; do not automatically retry an apply")
	}
	view := response.Maintenance
	if view.Schema != protocol.DiagnosticsVersion || view.Operation != "refresh" || view.Mode != req.Mode || view.Scope != "installation" || (req.Mode == "apply" && view.PlanID != req.PlanID) {
		return nil, errors.New("maintenance version or exact-request mismatch; do not replay apply")
	}
	result["available"], result["evidence"], result["maintenance"] = true, view.Evidence, view
	return result, nil
}

func validFeedbackPlanID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
