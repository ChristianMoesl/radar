package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"radar/internal/linking"
	"radar/internal/protocol"
)

type Config struct {
	BaseURL    string
	APIBaseURL string
	Email      string
	APIToken   string
}

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

func FetchAssignedIssues(ctx context.Context, logger *slog.Logger) ([]protocol.SourceRef, protocol.SourceStatus, error) {
	status := protocol.SourceStatus{Name: "jira", Status: "ok"}

	cfg, ok, missing := configFromEnv()
	if !ok {
		logger.Debug("jira collector not configured", "missing", missing)
		status.Status = "disabled"
		status.Detail = "missing " + strings.Join(missing, ", ")
		return nil, status, nil
	}

	issues, err := searchAssignedIssues(ctx, cfg)
	if err != nil {
		status.Status = "error"
		status.Detail = err.Error()
		return nil, status, err
	}

	return source_refsFromIssues(cfg, issues, ""), statusWithCount(status, len(issues), ""), nil
}

func ResolveDoneIssues(ctx context.Context, previous []protocol.Task, active []protocol.Task, jiraComplete bool, logger *slog.Logger) []protocol.Task {
	if !jiraComplete {
		logger.Debug("skipping jira done resolution; issue collection was incomplete")
		return keepTodaysDoneIssues(nil, previous)
	}

	cfg, ok, missing := configFromEnv()
	if !ok {
		logger.Debug("skipping jira done resolution; jira collector not configured", "missing", missing)
		return keepTodaysDoneIssues(nil, previous)
	}

	activeIssues := activeJiraIssueRefs(active)
	items := keepTodaysDoneIssues(nil, previous)
	seenDone := doneIssueIDs(items)
	checked := map[string]bool{}
	for _, item := range previous {
		if item.Attention == "done" {
			continue
		}

		issueRef, ok := jiraIssueSourceRef(item)
		if !ok || activeIssues[issueRef.ID] || checked[issueRef.ID] {
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
			SourceRefs: doneIssueSourceRefs(item.SourceRefs, cfg, issue, reason),
		}
		if id := doneIssueID(done); id != "" && seenDone[id] {
			continue
		}
		if id := doneIssueID(done); id != "" {
			seenDone[id] = true
		}
		items = append(items, done)
	}

	logger.Debug("resolved done jira issues", "count", len(items))
	return items
}

func source_refsFromIssues(cfg Config, issues []issue, suffix string) []protocol.SourceRef {
	source_refs := make([]protocol.SourceRef, 0, len(issues))
	for _, issue := range issues {
		source_refs = append(source_refs, sourceRefFromIssue(cfg, issue))
	}
	return source_refs
}

func statusWithCount(status protocol.SourceStatus, count int, suffix string) protocol.SourceStatus {
	status.Detail = fmt.Sprintf("%d assigned issues", count)
	if suffix != "" {
		status.Detail += " " + suffix
	}
	return status
}

func configFromEnv() (Config, bool, []string) {
	cloudID := os.Getenv("RADAR_JIRA_CLOUD_ID")
	cfg := Config{
		BaseURL:    strings.TrimRight(os.Getenv("RADAR_JIRA_BASE_URL"), "/"),
		APIBaseURL: strings.TrimRight(os.Getenv("RADAR_JIRA_API_BASE_URL"), "/"),
		Email:      os.Getenv("RADAR_JIRA_EMAIL"),
		APIToken:   os.Getenv("RADAR_JIRA_API_TOKEN"),
	}

	if cfg.APIBaseURL == "" && cloudID != "" {
		cfg.APIBaseURL = "https://api.atlassian.com/ex/jira/" + cloudID + "/rest/api/3"
	}

	missing := make([]string, 0)
	if cfg.Email == "" {
		missing = append(missing, "RADAR_JIRA_EMAIL")
	}
	if cfg.APIToken == "" {
		missing = append(missing, "RADAR_JIRA_API_TOKEN")
	}
	if cfg.APIBaseURL == "" {
		missing = append(missing, "RADAR_JIRA_CLOUD_ID or RADAR_JIRA_API_BASE_URL")
	}
	if cfg.BaseURL == "" {
		missing = append(missing, "RADAR_JIRA_BASE_URL")
	}
	return cfg, len(missing) == 0, missing
}

func fetchIssue(ctx context.Context, cfg Config, key string) (issue, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	issueURL := cfg.APIBaseURL + "/issue/" + url.PathEscape(key) + "?fields=summary,status,issuetype,priority"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, issueURL, nil)
	if err != nil {
		return issue{}, err
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.SetBasicAuth(cfg.Email, cfg.APIToken)

	res, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return issue{}, err
	}
	defer res.Body.Close()

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return issue{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return issue{}, fmt.Errorf("jira issue fetch failed: %s: %s", res.Status, strings.TrimSpace(string(data)))
	}

	var response issue
	if err := json.Unmarshal(data, &response); err != nil {
		return issue{}, fmt.Errorf("jira issue decode failed: %w: %s", err, previewResponse(data))
	}
	return response, nil
}

func searchAssignedIssues(ctx context.Context, cfg Config) ([]issue, error) {
	request := searchRequest{
		JQL:        "assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC",
		MaxResults: 100,
		Fields:     []string{"summary", "status", "issuetype", "priority"},
	}

	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	endpoints := []searchEndpoint{
		{Method: http.MethodPost, Path: "/search/jql"},
		{Method: http.MethodGet, Path: "/search/jql"},
		{Method: http.MethodPost, Path: "/search"},
	}

	errors := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		issues, retry, err := searchAssignedIssuesAt(ctx, cfg, endpoint, request, body)
		if err == nil {
			return issues, nil
		}
		errors = append(errors, err.Error())
		if !retry {
			return nil, err
		}
	}

	if len(errors) > 0 {
		return nil, fmt.Errorf("jira search failed on all supported endpoints: %s", strings.Join(errors, " | "))
	}
	return nil, fmt.Errorf("jira search failed: no supported search endpoint")
}

type searchEndpoint struct {
	Method string
	Path   string
}

func searchAssignedIssuesAt(ctx context.Context, cfg Config, endpoint searchEndpoint, request searchRequest, body []byte) ([]issue, bool, error) {
	url := cfg.APIBaseURL + endpoint.Path
	var reader io.Reader = bytes.NewReader(body)
	if endpoint.Method == http.MethodGet {
		query := urlValues(request)
		url += "?" + query.Encode()
		reader = nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, endpoint.Method, url, reader)
	if err != nil {
		return nil, false, err
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.SetBasicAuth(cfg.Email, cfg.APIToken)

	res, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, false, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		retry := res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone || res.StatusCode == http.StatusMethodNotAllowed
		return nil, retry, fmt.Errorf("jira search failed via %s %s: %s: %s", endpoint.Method, endpoint.Path, res.Status, strings.TrimSpace(string(data)))
	}

	var response searchResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, true, fmt.Errorf("jira search decode failed via %s %s: %w: %s", endpoint.Method, endpoint.Path, err, previewResponse(data))
	}
	return response.Issues, false, nil
}

func previewResponse(data []byte) string {
	value := strings.TrimSpace(string(data))
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 240 {
		return value[:240] + "..."
	}
	return value
}

func urlValues(request searchRequest) url.Values {
	values := url.Values{}
	values.Set("jql", request.JQL)
	values.Set("maxResults", fmt.Sprintf("%d", request.MaxResults))
	if len(request.Fields) > 0 {
		values.Set("fields", strings.Join(request.Fields, ","))
	}
	return values
}

func sourceRefFromIssue(cfg Config, issue issue) protocol.SourceRef {
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

	return protocol.SourceRef{
		ID:           "jira:issue:" + issue.Key,
		Source:       "jira",
		SourceLabel:  "Jira",
		Kind:         "issue",
		Title:        issue.Key + " " + issue.Fields.Summary,
		URL:          jiraIssueURL(cfg.BaseURL, issue.Key),
		Status:       status,
		CanonicalKey: "jira:issue:" + issue.Key,
		LinkingKeys:  linking.Keys("ticket:" + strings.ToUpper(issue.Key)),
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
			if sourceRef.Source == "jira" && sourceRef.Kind == "issue" {
				active[sourceRef.ID] = true
			}
		}
	}
	return active
}

func jiraIssueSourceRef(task protocol.Task) (protocol.SourceRef, bool) {
	for _, sourceRef := range task.SourceRefs {
		if sourceRef.Source == "jira" && sourceRef.Kind == "issue" {
			return sourceRef, true
		}
	}
	return protocol.SourceRef{}, false
}

func issueKeyFromSourceRefID(id string) (string, bool) {
	key, ok := strings.CutPrefix(id, "jira:issue:")
	return key, ok && key != ""
}

func issueDone(issue issue) bool {
	return issue.Fields.Status != nil && issue.Fields.Status.StatusCategory != nil && strings.EqualFold(issue.Fields.Status.StatusCategory.Key, "done")
}

func doneIssueSourceRefs(_ []protocol.SourceRef, cfg Config, issue issue, reason string) []protocol.SourceRef {
	sourceRef := sourceRefFromIssue(cfg, issue)
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
		id := doneIssueID(item)
		if id != "" && seen[id] {
			continue
		}
		if id != "" {
			seen[id] = true
		}
		items = append(items, item)
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
		if sourceRef.Source == "jira" && sourceRef.ID != "" {
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
