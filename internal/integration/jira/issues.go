package jira

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"radar/internal/linking"
	"radar/internal/protocol"
)

type searchRequest struct {
	JQL        string   `json:"jql"`
	MaxResults int      `json:"maxResults"`
	Fields     []string `json:"fields"`
}

type searchResponse struct {
	Issues []issue `json:"issues"`
}

type issue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary   string `json:"summary"`
		IssueType *struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Priority *struct {
			Name string `json:"name"`
		} `json:"priority"`
		Status *struct {
			Name           string `json:"name"`
			StatusCategory *struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"statusCategory"`
		} `json:"status"`
	} `json:"fields"`
}

func FetchAssignedIssues(ctx context.Context, issueTypes []string, logger *slog.Logger, matchers ...linking.MarkMatcher) ([]protocol.SourceRef, protocol.SourceStatus, error) {
	status := protocol.SourceStatus{Name: "jira", Status: "ok"}

	cfg, missing, configErr := loadConnection()
	if configErr != nil {
		status.Status, status.Detail = "error", configErr.Error()
		return nil, status, configErr
	}
	if len(missing) > 0 {
		logger.Debug("jira collector not configured", "missing", missing)
		status.Status = "disabled"
		status.Detail = "missing " + strings.Join(missing, ", ")
		return nil, status, nil
	}

	if len(issueTypes) == 0 {
		return nil, statusWithCount(status, 0, "(authoritative issue types disabled)"), nil
	}

	issues, err := searchAssignedIssues(ctx, cfg, issueTypes)
	if err != nil {
		status.Status = "error"
		status.Detail = err.Error()
		return nil, status, err
	}

	return source_refsFromIssues(cfg, issues, firstMarkMatcher(matchers)), statusWithCount(status, len(issues), ""), nil
}

func ResolveDoneIssues(ctx context.Context, previous []protocol.Task, active []protocol.Task, jiraComplete bool, logger *slog.Logger, matchers ...linking.MarkMatcher) []protocol.Task {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if !jiraComplete {
		logger.Debug("skipping jira done resolution; issue collection was incomplete")
		return keepTodaysDoneIssues(nil, previous)
	}

	cfg, missing, configErr := loadConnection()
	if configErr != nil || len(missing) > 0 {
		logger.Debug("skipping jira done resolution; jira collector not configured", "missing", missing)
		return keepTodaysDoneIssues(nil, previous)
	}

	marks := firstMarkMatcher(matchers)
	activeIssues := activeJiraIssueRefs(active)
	items := keepTodaysDoneIssues(nil, previous)
	seenDone := doneIssueIDs(items)
	checked := map[string]bool{}
	for _, item := range previous {
		if item.Attention == "done" {
			continue
		}

		for _, issueRef := range jiraIssueSourceRefs(item) {
			if issueRef.Signal == "done" || activeIssues[issueRef.ID] || checked[issueRef.ID] {
				continue
			}
			checked[issueRef.ID] = true

			key, ok := issueKeyFromSourceRefID(issueRef.ID)
			if !ok {
				logger.Warn("could not parse jira issue source ref id", "id", issueRef.ID)
				continue
			}
			issue, err := fetchIssue(ctx, cfg, key)
			if err != nil {
				logger.Warn("could not resolve previous jira issue", "id", item.ID, "error", err)
				continue
			}
			if !issueDone(issue) {
				continue
			}

			reason := "jira done"
			done := protocol.Task{
				Kind:       "jira_done_issue",
				Title:      item.Title,
				Repo:       item.Repo,
				URL:        item.URL,
				Attention:  "done",
				Reason:     reason,
				DoneAt:     time.Now().UTC().Format(time.RFC3339),
				SourceRefs: doneIssueSourceRefs(item.SourceRefs, cfg, issue, reason, marks),
			}
			if id := doneIssueID(done); id != "" && seenDone[id] {
				continue
			}
			if id := doneIssueID(done); id != "" {
				seenDone[id] = true
			}
			items = append(items, done)
		}
	}

	logger.Debug("resolved done jira issues", "count", len(items))
	return items
}

func source_refsFromIssues(cfg Config, issues []issue, marks linking.MarkMatcher) []protocol.SourceRef {
	sourceRefs := make([]protocol.SourceRef, 0, len(issues))
	for _, issue := range issues {
		sourceRefs = append(sourceRefs, sourceRefFromIssue(cfg, issue, marks))
	}
	return sourceRefs
}

func firstMarkMatcher(matchers []linking.MarkMatcher) linking.MarkMatcher {
	if len(matchers) == 0 {
		return linking.MarkMatcher{}
	}
	return matchers[0]
}

func statusWithCount(status protocol.SourceStatus, count int, suffix string) protocol.SourceStatus {
	status.Detail = fmt.Sprintf("%d assigned issues", count)
	if suffix != "" {
		status.Detail += " " + suffix
	}
	return status
}

func assignedIssuesJQL(issueTypes []string) string {
	jql := "assignee = currentUser() AND statusCategory != Done"
	if len(issueTypes) > 0 {
		quoted := make([]string, 0, len(issueTypes))
		for _, issueType := range issueTypes {
			issueType = strings.TrimSpace(issueType)
			issueType = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(issueType)
			quoted = append(quoted, `"`+issueType+`"`)
		}
		jql += " AND issuetype IN (" + strings.Join(quoted, ", ") + ")"
	}
	return jql + " ORDER BY updated DESC"
}

func sourceRefFromIssue(cfg Config, issue issue, matchers ...linking.MarkMatcher) protocol.SourceRef {
	issue.Key = strings.ToUpper(strings.TrimSpace(issue.Key))
	status := ""
	statusCategory := ""
	if issue.Fields.Status != nil {
		status = issue.Fields.Status.Name
		if issue.Fields.Status.StatusCategory != nil {
			statusCategory = issue.Fields.Status.StatusCategory.Key
		}
	}

	metadata := map[string]string{
		"key": issue.Key,
	}
	if statusCategory != "" {
		metadata["status_category"] = statusCategory
	}
	if issue.Fields.IssueType != nil {
		metadata["issue_type"] = issue.Fields.IssueType.Name
	}
	if issue.Fields.Priority != nil {
		metadata["priority"] = issue.Fields.Priority.Name
	}

	marks := firstMarkMatcher(matchers)
	return protocol.SourceRef{
		ID:             "jira:issue:" + issue.Key,
		Source:         "jira",
		SourceLabel:    "Jira",
		Kind:           "issue",
		Role:           protocol.SourceRefRoleAuthoritative,
		EntityID:       "jira:issue:" + issue.Key,
		Lifecycle:      protocol.SourceRefLifecycleWorkItem,
		Authority:      protocol.SourceRefAuthorityContributing,
		RetainInactive: true,
		Presentation: protocol.SourceRefPresentation{
			PreferTitle:   true,
			WorkspaceName: issue.Key + " " + issue.Fields.Summary,
		},
		Title:        issue.Key + " " + issue.Fields.Summary,
		URL:          jiraIssueURL(cfg.BaseURL, issue.Key),
		Status:       status,
		CanonicalKey: "jira:issue:" + issue.Key,
		LinkingKeys:  marks.Keys(issue.Key),
		Metadata:     metadata,
	}
}

func jiraIssueURL(baseURL string, key string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL + "/browse/" + key
	}
	parsed.Path = "/browse/" + key
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func activeJiraIssueRefs(tasks []protocol.Task) map[string]bool {
	active := map[string]bool{}
	for _, task := range tasks {
		for _, sourceRef := range task.SourceRefs {
			if sourceRef.Role == protocol.SourceRefRoleAuthoritative && sourceRef.Source == "jira" && sourceRef.Kind == "issue" {
				active[sourceRef.ID] = true
			}
		}
	}
	return active
}

func jiraIssueSourceRefs(task protocol.Task) []protocol.SourceRef {
	var refs []protocol.SourceRef
	for _, ref := range task.SourceRefs {
		if ref.Role == protocol.SourceRefRoleAuthoritative && ref.Source == "jira" && ref.Kind == "issue" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func issueKeyFromSourceRefID(id string) (string, bool) {
	key, ok := strings.CutPrefix(id, "jira:issue:")
	return key, ok && key != ""
}

func issueDone(issue issue) bool {
	return issue.Fields.Status != nil && issue.Fields.Status.StatusCategory != nil && strings.EqualFold(issue.Fields.Status.StatusCategory.Key, "done")
}

func doneIssueSourceRefs(_ []protocol.SourceRef, cfg Config, issue issue, reason string, marks linking.MarkMatcher) []protocol.SourceRef {
	sourceRef := sourceRefFromIssue(cfg, issue, marks)
	sourceRef.Status = reason
	sourceRef.Signal = "done"
	return []protocol.SourceRef{sourceRef}
}

func keepTodaysDoneIssues(items []protocol.Task, previous []protocol.Task) []protocol.Task {
	seen := doneIssueIDs(items)
	for _, item := range previous {
		if item.Attention != "done" || !isToday(item.DoneAt) || !hasSource(item, "jira") {
			continue
		}
		for _, ref := range jiraIssueSourceRefs(item) {
			if (ref.Signal != "" && ref.Signal != "done") || seen[ref.ID] {
				continue
			}
			done := item
			done.SourceRefs = []protocol.SourceRef{ref}
			seen[ref.ID] = true
			items = append(items, done)
		}
	}
	return items
}

func doneIssueIDs(items []protocol.Task) map[string]bool {
	seen := map[string]bool{}
	for _, item := range items {
		if id := doneIssueID(item); id != "" {
			seen[id] = true
		}
	}
	return seen
}

func doneIssueID(item protocol.Task) string {
	for _, sourceRef := range item.SourceRefs {
		if sourceRef.Role == protocol.SourceRefRoleAuthoritative && sourceRef.Source == "jira" && sourceRef.ID != "" {
			return sourceRef.ID
		}
	}
	return ""
}

func hasSource(item protocol.Task, source string) bool {
	for _, sourceRef := range item.SourceRefs {
		if sourceRef.Source == source {
			return true
		}
	}
	return false
}

func isToday(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return strings.HasPrefix(value, time.Now().Format("2006-01-02"))
	}
	now := time.Now()
	y1, m1, d1 := parsed.Local().Date()
	y2, m2, d2 := now.Local().Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}
