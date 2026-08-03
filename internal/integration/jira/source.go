package jira

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
)

const maxTitleDiscoveredIssues = 50

type Source struct{}

type issueMention struct {
	Key      string
	TaskID   int
	Explicit bool
	Order    int
}

func NewSource() Source {
	return Source{}
}

func (Source) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "jira", Label: "Jira", DisplayOrder: 0}
}

func (Source) Status(ctx context.Context, logger *slog.Logger) integration.StatusResult {
	status := protocol.SourceStatus{Name: "jira", Status: "ok"}
	if _, err := config.Load(); err != nil {
		logger.Debug("jira user configuration is invalid", "error", err)
		status.Status = "error"
		status.Detail = "could not load config"
		return integration.StatusResult{Status: status, CanRun: false}
	}
	_, ok, missing := configFromEnv()
	if !ok {
		logger.Debug("jira collector not configured", "missing", missing)
		status.Status = "disabled"
		status.Detail = "missing " + strings.Join(missing, ", ")
		return integration.StatusResult{Status: status, CanRun: false}
	}
	return integration.StatusResult{Status: status, CanRun: true}
}

func (Source) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	userConfig, err := config.Load()
	if err != nil {
		req.Logger.Warn("jira user configuration is invalid", "error", err)
		status := protocol.SourceStatus{Name: "jira", Status: "error", Detail: "could not load config"}
		return integration.CollectResult{SourceStatus: &status}
	}
	jiraConfig, ok, missing := configFromEnv()
	if !ok {
		status := protocol.SourceStatus{Name: "jira", Status: "disabled", Detail: "missing " + strings.Join(missing, ", ")}
		return integration.CollectResult{SourceStatus: &status}
	}

	status := protocol.SourceStatus{Name: "jira", Status: "ok"}
	complete := true
	assigned := []issue{}
	details := make([]string, 0, 3)
	if len(userConfig.Jira.AuthoritativeIssueTypes) == 0 {
		details = append(details, "assigned search skipped")
	} else {
		assigned, err = searchAssignedIssues(ctx, jiraConfig, userConfig.Jira.AuthoritativeIssueTypes)
		if err != nil {
			complete = false
			status.Status = "error"
			details = append(details, "assigned search failed: "+err.Error())
			req.Logger.Warn("jira assigned issue collection failed", "error", err)
		} else {
			details = append(details, fmt.Sprintf("%d assigned issues", len(assigned)))
		}
	}

	mentions := discoverIssueMentions(req.Previous, req.LinkingMarks)
	mentionsByKey, keyOrder := groupMentions(mentions)
	issuesByKey := make(map[string]issue, len(assigned)+len(keyOrder))
	assignedKeys := make(map[string]bool, len(assigned))
	for _, value := range assigned {
		key := normalizeIssueKey(value.Key)
		if key == "" || assignedKeys[key] {
			continue
		}
		value.Key = key
		assignedKeys[key] = true
		issuesByKey[key] = value
	}

	fetchKeys := make([]string, 0, len(keyOrder))
	for _, key := range keyOrder {
		if !assignedKeys[key] {
			fetchKeys = append(fetchKeys, key)
		}
	}
	truncated := 0
	preserveKeys := map[string]bool{}
	if len(fetchKeys) > maxTitleDiscoveredIssues {
		truncated = len(fetchKeys) - maxTitleDiscoveredIssues
		for _, key := range fetchKeys[maxTitleDiscoveredIssues:] {
			preserveKeys[key] = true
		}
		fetchKeys = fetchKeys[:maxTitleDiscoveredIssues]
		complete = false
		status.Status = "error"
	}

	failedKeys := map[string]bool{}
	for _, key := range fetchKeys {
		value, fetchErr := fetchIssue(ctx, jiraConfig, key)
		if fetchErr != nil {
			complete = false
			status.Status = "error"
			failedKeys[key] = true
			preserveKeys[key] = true
			req.Logger.Warn("jira title reference fetch failed", "key", key, "error", fetchErr)
			continue
		}
		value.Key = key
		issuesByKey[key] = value
	}

	observations := make([]integration.Observation, 0, len(assigned)+len(mentions))
	projectedAssigned := map[string]bool{}
	for _, value := range assigned {
		key := normalizeIssueKey(value.Key)
		if key == "" || projectedAssigned[key] {
			continue
		}
		projectedAssigned[key] = true
		mention := firstMention(mentionsByKey[key])
		observations = append(observations, authoritativeObservation(userConfig.Jira, jiraConfig, value, mention, req.LinkingMarks))
	}
	for _, key := range keyOrder {
		value, exists := issuesByKey[key]
		if !exists || assignedKeys[key] {
			continue
		}
		keyMentions := mentionsByKey[key]
		authoritative := hasExplicitMention(keyMentions) || userConfig.Jira.IsAuthoritativeIssueType(issueTypeName(value))
		if authoritative {
			observations = append(observations, authoritativeObservation(userConfig.Jira, jiraConfig, value, firstMention(keyMentions), req.LinkingMarks))
			continue
		}
		for _, mention := range keyMentions {
			observations = append(observations, informationalObservation(jiraConfig, value, mention))
		}
	}

	if len(preserveKeys) > 0 || err != nil {
		observations = append(observations, previousJiraObservations(req.Previous, preserveKeys, err != nil)...)
	}
	observations = deduplicateObservations(observations)
	details = append(details, fmt.Sprintf("%d title references", len(keyOrder)))
	if len(failedKeys) > 0 {
		details = append(details, fmt.Sprintf("%d direct fetches failed", len(failedKeys)))
	}
	if truncated > 0 {
		details = append(details, fmt.Sprintf("%d title references truncated at limit %d", truncated, maxTitleDiscoveredIssues))
	}
	status.Detail = strings.Join(details, "; ")
	return integration.CollectResult{Observations: observations, Complete: complete, SourceStatus: &status}
}

func discoverIssueMentions(tasks []protocol.Task, marks linking.MarkMatcher) []issueMention {
	ordered := append([]protocol.Task(nil), tasks...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	mentions := make([]issueMention, 0)
	order := 0
	for _, task := range ordered {
		mentionIndexes := map[string]int{}
		for _, title := range titleFacts(task) {
			for _, key := range marks.Values(title) {
				if _, seen := mentionIndexes[key]; seen {
					continue
				}
				mentionIndexes[key] = len(mentions)
				mentions = append(mentions, issueMention{Key: key, TaskID: task.ID, Order: order})
				order++
			}
		}
		for _, key := range explicitAssociationKeys(task) {
			if index, seen := mentionIndexes[key]; seen {
				mentions[index].Explicit = true
				continue
			}
			mentionIndexes[key] = len(mentions)
			mentions = append(mentions, issueMention{Key: key, TaskID: task.ID, Explicit: true, Order: order})
			order++
		}
	}
	return mentions
}

func titleFacts(task protocol.Task) []string {
	facts := make([]string, 0, len(task.SourceRefs)+2)
	jiraTitle := false
	for _, ref := range task.SourceRefs {
		if ref.Source == "jira" && ref.Kind == "issue" && ref.Title == task.Title {
			jiraTitle = true
			break
		}
	}
	if !jiraTitle && strings.TrimSpace(task.Title) != "" {
		facts = append(facts, task.Title)
	}
	if manualTitle := strings.TrimSpace(task.Metadata["manual_title"]); manualTitle != "" {
		facts = append(facts, manualTitle)
	}
	for _, ref := range task.SourceRefs {
		if ref.Source != "jira" && strings.TrimSpace(ref.Title) != "" {
			facts = append(facts, ref.Title)
		}
	}
	return facts
}

func explicitAssociationKeys(task protocol.Task) []string {
	keys := make([]string, 0)
	for _, association := range task.Associations {
		if association.Source != "jira" {
			continue
		}
		if key := normalizeIssueKey(association.ExternalID); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func groupMentions(mentions []issueMention) (map[string][]issueMention, []string) {
	grouped := map[string][]issueMention{}
	order := make([]string, 0)
	for _, mention := range mentions {
		if _, exists := grouped[mention.Key]; !exists {
			order = append(order, mention.Key)
		}
		grouped[mention.Key] = append(grouped[mention.Key], mention)
	}
	return grouped, order
}

func firstMention(mentions []issueMention) issueMention {
	if len(mentions) == 0 {
		return issueMention{}
	}
	return mentions[0]
}

func hasExplicitMention(mentions []issueMention) bool {
	for _, mention := range mentions {
		if mention.Explicit {
			return true
		}
	}
	return false
}

func authoritativeObservation(cfg config.JiraConfig, jiraConfig Config, value issue, mention issueMention, marks linking.MarkMatcher) integration.Observation {
	ref := sourceRefFromIssue(jiraConfig, value, marks)
	ref.Role = protocol.SourceRefRoleAuthoritative
	if mention.TaskID != 0 {
		order := mention.Order
		ref.Presentation.TitleOrder = &order
	}
	signal := integration.WorkSignal(cfg.SignalForStatus(ref.Status))
	if strings.EqualFold(ref.Metadata["status_category"], "done") {
		signal = integration.SignalDone
	}
	ref.Signal = string(signal)
	return integration.Observation{Ref: ref, TargetTaskID: mention.TaskID, Signal: signal, Reason: ref.Status}
}

func informationalObservation(jiraConfig Config, value issue, mention issueMention) integration.Observation {
	ref := sourceRefFromIssue(jiraConfig, value)
	ref.ID = fmt.Sprintf("jira:mention:%d:%s", mention.TaskID, value.Key)
	ref.Role = protocol.SourceRefRoleInformational
	ref.Signal = ""
	ref.CanonicalKey = ""
	ref.LinkingKeys = nil
	ref.Lifecycle = ""
	ref.Presentation = protocol.SourceRefPresentation{}
	return integration.Observation{Ref: ref, TargetTaskID: mention.TaskID}
}

func previousJiraObservations(tasks []protocol.Task, failedKeys map[string]bool, keepAllAuthoritative bool) []integration.Observation {
	observations := make([]integration.Observation, 0)
	seen := map[string]bool{}
	for _, task := range tasks {
		for _, ref := range task.SourceRefs {
			if ref.Source != "jira" || ref.Kind != "issue" || ref.ID == "" || seen[ref.ID] {
				continue
			}
			key := normalizeIssueKey(ref.Metadata["key"])
			preserveAssigned := keepAllAuthoritative && ref.Role == protocol.SourceRefRoleAuthoritative && ref.Presentation.TitleOrder == nil
			if !failedKeys[key] && !preserveAssigned {
				continue
			}
			seen[ref.ID] = true
			signal := integration.WorkSignal(ref.Signal)
			observations = append(observations, integration.Observation{Ref: ref, TargetTaskID: task.ID, Signal: signal, Reason: ref.Status})
		}
	}
	return observations
}

func deduplicateObservations(observations []integration.Observation) []integration.Observation {
	seen := map[string]bool{}
	kept := make([]integration.Observation, 0, len(observations))
	for _, observation := range observations {
		identity := observation.Ref.ID
		if identity == "" {
			identity = fmt.Sprintf("target:%d:%s", observation.TargetTaskID, observation.Ref.URL)
		}
		if seen[identity] {
			continue
		}
		seen[identity] = true
		kept = append(kept, observation)
	}
	return kept
}

func (Source) NormalizeAssociation(_ context.Context, value string) (protocol.TaskAssociation, error) {
	key := normalizeIssueKey(value)
	if key == "" {
		return protocol.TaskAssociation{}, fmt.Errorf("invalid Jira issue key %q", strings.TrimSpace(value))
	}
	markKey := linking.MarkKey(key)
	return protocol.TaskAssociation{
		Source:       "jira",
		ExternalID:   key,
		CanonicalKey: markKey,
		LinkingKeys:  []string{markKey},
		Lifecycle:    protocol.SourceRefLifecycleWorkItem,
	}, nil
}

func normalizeIssueKey(key string) string {
	key = strings.ToUpper(strings.TrimSpace(key))
	parts := strings.Split(key, "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	for _, r := range parts[0] {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return ""
		}
	}
	for _, r := range parts[1] {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return key
}

func issueTypeName(value issue) string {
	if value.Fields.IssueType == nil {
		return ""
	}
	return strings.TrimSpace(value.Fields.IssueType.Name)
}

func (Source) Reconcile(ctx context.Context, req integration.ReconcileRequest) []integration.Observation {
	tasks := ResolveDoneIssues(ctx, req.Previous, req.Active, req.Result.Complete, req.Logger, req.LinkingMarks)
	observations := make([]integration.Observation, 0, len(tasks))
	for _, task := range tasks {
		if len(task.SourceRefs) == 0 {
			continue
		}
		ref := task.SourceRefs[0]
		ref.Signal = task.Attention
		observations = append(observations, integration.Observation{
			Ref: ref, Signal: integration.WorkSignal(task.Attention), Reason: task.Reason,
		})
	}
	return observations
}

var _ integration.Source = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.Reconciler = Source{}
var _ integration.AssociationProvider = Source{}
var _ integration.WorkTracker = Source{}
