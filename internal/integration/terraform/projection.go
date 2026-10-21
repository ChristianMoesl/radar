package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/terraform/settings"
	"radar/internal/linking"
	"radar/internal/protocol"
)

func (s Source) observe(ctx context.Context, session session, cfg settings.Config, run resource, included []resource, req integration.CollectRequest) (integration.Observation, bool, error) {
	if run.Type != "runs" || !validID(run.ID, "run-") {
		return integration.Observation{}, false, fmt.Errorf("invalid Terraform run identity")
	}
	creator := run.related("created-by")
	if creator.ID == "" || creator.ID != session.userID || creator.Type != "users" {
		return integration.Observation{}, false, nil
	}
	space := run.related("workspace")
	name := session.spaces[space.ID]
	if name == "" || space.Type != "workspaces" {
		return integration.Observation{}, false, fmt.Errorf("Terraform run is outside the selected workspace scope")
	}
	var attrs runAttributes
	if json.Unmarshal(run.Attributes, &attrs) != nil || attrs.PlanOnly == nil {
		return integration.Observation{}, false, fmt.Errorf("Terraform run state is incomplete")
	}
	signal, label, failure, err := classify(attrs)
	if err != nil {
		return integration.Observation{}, false, err
	}
	created, err := time.Parse(time.RFC3339, attrs.CreatedAt)
	if err != nil {
		return integration.Observation{}, false, fmt.Errorf("Terraform run timestamp is invalid")
	}
	id := runIdentity(session.host, run.ID)
	base := "https://" + session.host + "/app/" + url.PathEscape(cfg.Organization) + "/workspaces/" + url.PathEscape(name)
	message := strings.Join(strings.Fields(attrs.Message), " ")
	if message == "" {
		message = run.ID
	}
	metadata := map[string]string{
		"run_id": run.ID, "service_host": session.host, "creator_id": session.userID,
		"organization": cfg.Organization, "workspace_id": space.ID, "workspace_name": name,
		"workspace_url": base, "run_status": attrs.Status, "created_at": created.UTC().Format(time.RFC3339),
		"plan_only": strconv.FormatBool(*attrs.PlanOnly),
	}
	previous := previousRefs(req.Previous)[id]
	ref := protocol.SourceRef{
		ID: id, EntityID: id, CanonicalKey: id, Source: "terraform", SourceLabel: "Terraform", Kind: "run",
		Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem,
		Authority: protocol.SourceRefAuthorityContributing, RetainInactive: true,
		Title: name + ": " + message, URL: base + "/runs/" + run.ID, Status: label, WorkflowState: attrs.Status, Metadata: metadata,
	}
	if *attrs.PlanOnly {
		metadata["run_type"] = "speculative plan"
	} else {
		metadata["run_type"] = "plan and apply"
	}
	if failure {
		cursor := created.UTC().Format(time.RFC3339)
		for _, stamp := range attrs.Timestamps {
			if at, err := time.Parse(time.RFC3339, stamp); err == nil && at.After(created) {
				created = at
				cursor = at.UTC().Format(time.RFC3339)
			}
		}
		metadata["failure_cursor"] = cursor
		reviewed := false
		for _, task := range req.Previous {
			for _, previous := range task.SourceRefs {
				if previous.ID != id || previous.Metadata["creator_id"] != session.userID {
					continue
				}
				reviewed = previous.Metadata["failure_reviewed_at"] == cursor || task.AcknowledgementCursor >= cursor
			}
		}
		if reviewed {
			signal = integration.SignalDone
			metadata["failure_reviewed_at"] = cursor
		} else {
			ref.Acknowledgement = &protocol.SourceRefAcknowledgement{Cursor: cursor, FallbackSignal: string(integration.SignalLowPriority), FallbackReason: label + " — reviewed; confirming completion"}
		}
	}
	// Consume only presentation metadata; full execution-plan JSON and signed
	// log URLs never enter Radar's source observations or logs.
	planID := run.related("plan").ID
	if validID(planID, "plan-") {
		metadata["plan_id"] = planID
		if previous.Metadata["plan_id"] == planID {
			for _, key := range []string{"additions", "changes", "destructions"} {
				if value := previous.Metadata[key]; value != "" {
					metadata[key] = value
				}
			}
		}
		var plan resource
		for _, item := range included {
			if item.Type == "plans" && item.ID == planID {
				plan = item
				break
			}
		}
		if plan.ID == "" {
			if doc, e := session.client.get(ctx, "/plans/"+planID); e == nil {
				plan, _ = doc.one("plans")
			}
		}
		var p planAttributes
		if plan.ID == planID && json.Unmarshal(plan.Attributes, &p) == nil {
			for key, value := range map[string]*int{"additions": p.Additions, "changes": p.Changes, "destructions": p.Destructions} {
				if value != nil && *value >= 0 {
					metadata[key] = strconv.Itoa(*value)
				}
			}
		}
	}
	var ingress resource
	cvID := run.related("configuration-version").ID
	if validID(cvID, "cv-") {
		metadata["configuration_version_id"] = cvID
	}
	for _, item := range included {
		if item.Type == "configuration-versions" && item.ID == run.related("configuration-version").ID {
			for _, linked := range included {
				if linked.Type == "ingress-attributes" && linked.ID == item.related("ingress-attributes").ID {
					ingress = linked
				}
			}
		}
	}
	if ingress.ID == "" {
		if cv := run.related("configuration-version").ID; validID(cv, "cv-") {
			if doc, e := session.client.get(ctx, "/configuration-versions/"+cv+"/ingress-attributes"); e == nil {
				ingress, _ = doc.one("ingress-attributes")
			}
		}
	}
	var git ingressAttributes
	if ingress.ID != "" {
		if json.Unmarshal(ingress.Attributes, &git) != nil {
			ingress = resource{}
			git = ingressAttributes{}
		}
	}
	retainedContext := ingress.ID == "" && cvID != "" && previous.Metadata["configuration_version_id"] == cvID
	if retainedContext {
		git.Branch, git.Repository, git.CloneURL = previous.Metadata["source_branch"], previous.Repo, previous.Metadata["repository_url"]
		if previous.Presentation.WorkspaceName != "" {
			git.DefaultBranch = new(false)
		}
	}
	if git.Branch != "" {
		metadata["source_branch"] = git.Branch
	}
	if err := s.link(&ref, attrs.Message, git, req.LinkingMarks); err != nil {
		return integration.Observation{}, false, err
	}
	if retainedContext {
		for _, key := range previous.LinkingKeys {
			if !linking.IsMarkKey(key) {
				ref.LinkingKeys = linking.Keys(append(ref.LinkingKeys, key)...)
			}
		}
	}
	ref.Signal = string(signal)
	ref.Presentation.Label = name + " · " + label
	if *attrs.PlanOnly {
		ref.Presentation.Label += " · speculative"
	}
	if metadata["additions"] != "" && metadata["changes"] != "" && metadata["destructions"] != "" {
		ref.Presentation.Label += fmt.Sprintf(" · +%s ~%s −%s", metadata["additions"], metadata["changes"], metadata["destructions"])
	}
	return integration.Observation{Ref: ref, Signal: signal, Reason: label, TaskKind: "terraform_run"}, true, nil
}

func classify(a runAttributes) (integration.WorkSignal, string, bool, error) {
	switch a.Status {
	case "applied":
		return integration.SignalDone, "Applied", false, nil
	case "planned_and_finished":
		if a.PlanOnly != nil && *a.PlanOnly {
			return integration.SignalDone, "Plan completed", false, nil
		}
		return integration.SignalDone, "No changes", false, nil
	case "canceled", "force_canceled":
		return integration.SignalDone, "Canceled", false, nil
	case "discarded":
		return integration.SignalDone, "Discarded", false, nil
	case "errored":
		return integration.SignalAttention, "Run failed", true, nil
	case "policy_soft_failed":
		return integration.SignalAttention, "Policy failed", true, nil
	case "policy_override":
		return integration.SignalAttention, "Policy override required", false, nil
	case "planned_and_saved":
		return integration.SignalAttention, "Saved plan awaiting confirmation", false, nil
	case "pending", "fetching", "fetching_completed", "pre_plan_running", "pre_plan_completed", "queuing", "plan_queued", "planning", "planned", "cost_estimating", "cost_estimated", "policy_checking", "policy_checked", "confirmed", "post_plan_running", "post_plan_completed", "apply_queued", "applying":
		if a.Actions.Confirmable {
			return integration.SignalAttention, "Awaiting confirmation", false, nil
		}
		// Some tokens cannot confirm, even though the run is awaiting approval.
		if a.AutoApply != nil && !*a.AutoApply && a.PlanOnly != nil && !*a.PlanOnly && (a.Status == "policy_checked" || a.Status == "post_plan_completed") {
			return integration.SignalAttention, "Awaiting confirmation", false, nil
		}
		return integration.SignalInProgress, strings.ReplaceAll(a.Status, "_", " "), false, nil
	default:
		return "", "", false, fmt.Errorf("Terraform run has an unknown state; previous facts retained")
	}
}
