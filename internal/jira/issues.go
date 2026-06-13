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

	"radar.nvim/internal/protocol"
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
		ID:       "jira:issue:" + issue.Key,
		Source:   "jira",
		Kind:     "issue",
		Title:    issue.Key + " " + issue.Fields.Summary,
		URL:      jiraIssueURL(cfg.BaseURL, issue.Key),
		Status:   status,
		Metadata: metadata,
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
