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
	jiraConfig, _, missing := configFromEnv()
	detail := ""
	if len(missing) > 0 {
		detail = "missing " + strings.Join(missing, ", ")
	}
	if availability := integration.OptionalStatus("jira", cfg.Jira.Enabled, detail); !availability.CanRun {
		return integration.CollectResult{SourceStatus: &availability.Status}
	}

	seen := map[string]bool{}
	fetched := make([]issue, 0)
	fetchedObservations := map[string]int{}
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
				result.Observations = append(result.Observations, integration.Observation{Ref: ref, Signal: integration.SignalDone, Reason: ref.Status})
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
		fetched = append(fetched, value)
		fetchedObservations[key] = len(result.Observations)
		result.Observations = append(result.Observations, authoritativeObservation(cfg.Jira, jiraConfig, value, issueMention{}, req.LinkingMarks, nil))
	}
	// Explicit tracking also owns Jira's development relationships when the
	// issue is no longer discoverable by assignment or title. Only freshly
	// fetched issues need this lookup; cached terminal refs retain their facts.
	development := collectDevelopmentLinks(ctx, jiraConfig, fetched, req.Previous, req.Logger, source.developmentResolvers)
	for key, index := range fetchedObservations {
		ref := &result.Observations[index].Ref
		ref.LinkingKeys = linking.Keys(append(ref.LinkingKeys, development.LinkingKeysByIssue[key]...)...)
	}
	boundDevelopmentStatus(&result, req, development)
	return result
}

// Development failures affect diagnostics, not completeness of successfully
// resolved issue states, exactly as in ordinary Collect. Keep its diagnostics
// too, since the collector replaces source status when a resolver supplies one.
func boundDevelopmentStatus(result *integration.CollectResult, req integration.BindingRequest, development developmentCollection) {
	details := make([]string, 0, 4)
	if development.PullRequests > 0 {
		details = append(details, fmt.Sprintf("%d bound issue development pull requests", development.PullRequests))
	}
	if development.Invalid > 0 {
		details = append(details, fmt.Sprintf("%d invalid bound issue development pull requests", development.Invalid))
		if req.Logger != nil {
			req.Logger.Warn("jira bound issue development links contained invalid pull requests", "count", development.Invalid)
		}
	}
	if development.Failed > 0 {
		details = append(details, fmt.Sprintf("%d bound issue development lookups failed", development.Failed))
	}
	if development.Unavailable > 0 {
		details = append(details, "bound issue development links unavailable")
	}
	if len(details) == 0 {
		return
	}
	status := protocol.SourceStatus{Name: "jira", Status: "ok"}
	if result.SourceStatus != nil {
		status = *result.SourceStatus
	} else if req.Result.SourceStatus != nil {
		status = *req.Result.SourceStatus
	}
	if development.Invalid > 0 || development.Failed > 0 {
		status.Status = "error"
	}
	if status.Detail != "" {
		details = append([]string{status.Detail}, details...)
	}
	status.Detail = strings.Join(details, "; ")
	result.SourceStatus = &status
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
