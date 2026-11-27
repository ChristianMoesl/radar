package main

import (
	"errors"
	"regexp"
	"time"

	"radar/internal/client"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
	"radar/internal/socket"
)

var feedbackStopAgentID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// The local CLI is a trusted adapter, not a consent token issuer. Pi must obtain
// separate fresh interactive interruption approval before invoking exact apply.
// Only native registration resolves the feedback/scope paths; never agentCall,
// daemon startup, generic agent commands, inventory fallback or automatic replay.
func feedbackAgentStop(current, scope, parent, agent, mode, plan string) (map[string]any, error) {
	if (scope != "origin" && scope != "feedback" && scope != "installation") || parent == "" || len(parent) > 512 || !feedbackStopAgentID.MatchString(agent) || (mode != "preview" && mode != "apply") || (mode == "preview" && plan != "") || (mode == "apply" && !validFeedbackPlanID(plan)) {
		return nil, errors.New("agent stop requires one exact retained task, registered agent, scope and preview or exact-plan apply")
	}
	root, group, found, err := workspace.RegisteredWorkspace(current, "")
	if err != nil || !found || !group.Feedback {
		return nil, errors.New("agent stop requires a registered Radar feedback workspace")
	}
	result := map[string]any{"diagnostics_schema": protocol.DiagnosticsVersion, "section": "agent_stop", "scope": scope, "available": false, "feedback_workspace_id": group.ID, "evidence": "unavailable"}
	req := protocol.DiagnosticMaintenanceRequest{Schema: protocol.DiagnosticsVersion, Operation: "agent_stop", Mode: mode, Scope: scope, ParentID: parent, AgentID: agent, PlanID: plan, FeedbackWorkspace: group.Path}
	scopeID := ""
	if scope == "feedback" {
		req.Workspace, scopeID = group.Path, group.ID
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
		result["reason"] = "agent_stop_outcome_unknown_do_not_replay"
		return result, nil
	}
	view := response.DiagnosticAgentStop
	if !response.OK || view == nil || !protocol.ValidDiagnosticAgentStop(*view, req) || view.FeedbackWorkspaceID != group.ID || view.ScopeWorkspaceID != scopeID {
		return nil, errors.New("native agent stop unavailable or exact plan changed; outcome may be unknown, do not replay or automatically retry")
	}
	result["available"], result["evidence"], result["agent_stop"] = true, view.Evidence, view
	return result, nil
}
