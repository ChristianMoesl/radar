package jira

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
)

func (source Source) ResolveBindings(ctx context.Context, req integration.BindingRequest) integration.CollectResult {
	result := integration.CollectResult{Complete: true}
	if len(req.Bindings) == 0 {
		return result
	}
	cfg, err := config.Load()
	if err != nil {
		jiraBindingFailure(&result, req, err)
		return result
	}
	if availability := integration.OptionalStatus("jira", cfg.Jira.Enabled, ""); !availability.CanRun {
		return integration.CollectResult{SourceStatus: &availability.Status}
	}
	jiraConfig, missing, err := loadConnection()
	if err != nil {
		jiraBindingFailure(&result, req, err)
		return result
	}
	detail := ""
	if len(missing) > 0 {
		detail = "missing " + strings.Join(missing, ", ")
	}
	if availability := integration.OptionalStatus("jira", cfg.Jira.Enabled, detail); !availability.CanRun {
		return integration.CollectResult{SourceStatus: &availability.Status}
	}

	seen := map[string]bool{}
	for _, binding := range req.Bindings {
		key, ok := boundIssueKey(binding)
		if !ok {
			jiraBindingFailure(&result, req, fmt.Errorf("invalid issue binding %q", binding.ID))
			continue
		}
		if seen[binding.ID] {
			continue
		}
		seen[binding.ID] = true
		observed, found := observedBoundIssue(req.Result.Observations, binding.ID)
		// Incomplete Jira results can contain preserved cache observations. Only
		// a complete collection proves these facts are freshly read from Jira.
		if found && req.Result.Complete {
			if contributingBoundIssue(observed.Ref) {
				continue
			}
			// A type filter can make ordinary title discovery informational. The
			// explicit work-item binding still owns the authoritative identity;
			// promote only this fetched entity, never the unrelated mentions.
			result.Observations = append(result.Observations, boundIssueObservation(observed.Ref, cfg.Jira, req.LinkingMarks))
			continue
		}
		if !found {
			if ref, ok := cachedBoundIssue(req.Previous, binding.ID); ok {
				result.Observations = append(result.Observations, boundIssueObservation(ref, cfg.Jira, req.LinkingMarks))
				continue
			}
		}
		value, err := fetchIssue(ctx, jiraConfig, key)
		if err != nil {
			jiraBindingFailure(&result, req, fmt.Errorf("resolve %s: %w", binding.ID, err))
			continue
		}
		if normalizeIssueKey(value.Key) != key || value.Fields.Status == nil || strings.TrimSpace(value.Fields.Status.Name) == "" ||
			value.Fields.Status.StatusCategory == nil || strings.TrimSpace(value.Fields.Status.StatusCategory.Key) == "" || strings.TrimSpace(value.Fields.Summary) == "" {
			jiraBindingFailure(&result, req, fmt.Errorf("issue %s response has invalid identity or state", binding.ID))
			continue
		}
		// Explicit tracking survives reassignment and type-filter changes. This
		// does not change authoritative/informational rules for ordinary search.
		result.Observations = append(result.Observations, authoritativeObservation(cfg.Jira, jiraConfig, value, issueMention{}, req.LinkingMarks))
	}
	return result
}

func boundIssueKey(binding protocol.SourceBinding) (string, bool) {
	if binding.Source != "jira" || binding.Kind != "issue" || binding.Key != "" || !binding.WorkItem {
		return "", false
	}
	key, ok := issueKeyFromSourceRefID(binding.ID)
	if !ok || normalizeIssueKey(key) != key {
		return "", false
	}
	_, suffix, _ := strings.Cut(key, "-")
	number, err := strconv.ParseUint(suffix, 10, 64)
	if err != nil || number == 0 || strconv.FormatUint(number, 10) != suffix {
		return "", false
	}
	return key, true
}

func contributingBoundIssue(ref protocol.SourceRef) bool {
	return ref.Source == "jira" && ref.Kind == "issue" && ref.Role == protocol.SourceRefRoleAuthoritative &&
		ref.Lifecycle == protocol.SourceRefLifecycleWorkItem && ref.Authority == protocol.SourceRefAuthorityContributing
}

func observedBoundIssue(observations []integration.Observation, id string) (integration.Observation, bool) {
	for _, observation := range observations {
		ref := observation.Ref
		if ref.Source == "jira" && ref.Kind == "issue" && (ref.ID == id || ref.EntityID == id) {
			return observation, true
		}
	}
	return integration.Observation{}, false
}

func cachedBoundIssue(tasks []protocol.Task, id string) (protocol.SourceRef, bool) {
	for _, task := range tasks {
		for _, ref := range task.SourceRefs {
			if ref.ID == id && contributingBoundIssue(ref) && ref.Signal == string(integration.SignalDone) {
				return ref, true
			}
		}
	}
	return protocol.SourceRef{}, false
}

func boundIssueObservation(ref protocol.SourceRef, cfg config.JiraConfig, marks linking.MarkMatcher) integration.Observation {
	ref.ID = ref.EntityID
	ref.Role = protocol.SourceRefRoleAuthoritative
	ref.CanonicalKey = ref.ID
	ref.Lifecycle = protocol.SourceRefLifecycleWorkItem
	ref.Authority = protocol.SourceRefAuthorityContributing
	ref.RetainInactive = true
	ref.LinkingKeys = marks.Keys(ref.Metadata["key"])
	ref.Presentation = protocol.SourceRefPresentation{PreferTitle: true, WorkspaceName: ref.Title}
	signal := integration.WorkSignal(cfg.SignalForStatus(ref.Status))
	if strings.EqualFold(ref.Metadata["status_category"], "done") {
		signal = integration.SignalDone
	}
	ref.Signal = string(signal)
	return integration.Observation{Ref: ref, Signal: signal, Reason: ref.Status}
}

func jiraBindingFailure(result *integration.CollectResult, req integration.BindingRequest, err error) {
	result.Complete = false
	status := protocol.SourceStatus{Name: "jira", Status: "error", Detail: "bound issue resolution failed: " + err.Error()}
	result.SourceStatus = &status
	if req.Logger != nil {
		req.Logger.Warn("jira bound issue resolution failed", "error", err)
	}
}

var _ integration.BoundSourceResolver = Source{}
