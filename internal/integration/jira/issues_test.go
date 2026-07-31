package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/protocol"
)

func TestIssueSourceRefContract(t *testing.T) {
	var issue issue
	issue.Key = "RAD-123"
	issue.Fields.Summary = "Ship integration contracts"
	ref := sourceRefFromIssue(Config{BaseURL: "https://jira.example.test"}, issue)
	contracttest.AssertValidSourceRefs(t, "jira", []protocol.SourceRef{ref})
}

func TestSearchAssignedIssuesUsesSearchJQLEndpoint(t *testing.T) {
	var called []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = append(called, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("Authorization"); got != basicAuth("me@example.com", "token") {
			t.Fatalf("authorization = %q, want basic auth", got)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/search/jql" {
			t.Fatalf("request = %s %s, want POST /search/jql", r.Method, r.URL.Path)
		}
		var request searchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		wantJQL := `assignee = currentUser() AND statusCategory != Done AND issuetype IN ("Story", "Bug") ORDER BY updated DESC`
		if request.JQL != wantJQL {
			t.Fatalf("JQL = %q, want %q", request.JQL, wantJQL)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(searchResponse{
			Issues: []issue{{Key: "ABC-123"}},
		})
	}))
	defer server.Close()

	issues, err := searchAssignedIssues(context.Background(), Config{
		APIBaseURL: server.URL,
		BaseURL:    "https://jira.example.com",
		Email:      "me@example.com",
		APIToken:   "token",
	}, []string{"Story", "Bug"})
	if err != nil {
		t.Fatalf("searchAssignedIssues() error = %v", err)
	}
	if len(issues) != 1 || issues[0].Key != "ABC-123" {
		t.Fatalf("issues = %+v, want one ABC-123 issue", issues)
	}
	if len(called) != 1 {
		t.Fatalf("called %d endpoints, want 1", len(called))
	}
}

func TestSearchAssignedIssuesFallsBackToSearch(t *testing.T) {
	var called []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = append(called, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /search/jql":
			http.Error(w, `{"errorMessages":["not supported"]}`, http.StatusNotFound)
		case "GET /search/jql":
			http.Error(w, `{"errorMessages":["not supported"]}`, http.StatusNotFound)
		case "POST /search":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(searchResponse{
				Issues: []issue{{Key: "ABC-456"}},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	issues, err := searchAssignedIssues(context.Background(), Config{
		APIBaseURL: server.URL,
		BaseURL:    "https://jira.example.com",
		Email:      "me@example.com",
		APIToken:   "token",
	}, nil)
	if err != nil {
		t.Fatalf("searchAssignedIssues() error = %v", err)
	}
	if len(issues) != 1 || issues[0].Key != "ABC-456" {
		t.Fatalf("issues = %+v, want one ABC-456 issue", issues)
	}
	want := []string{"POST /search/jql", "GET /search/jql", "POST /search"}
	if len(called) != len(want) {
		t.Fatalf("called %d endpoints, want %d", len(called), len(want))
	}
	for i := range want {
		if called[i] != want[i] {
			t.Fatalf("called[%d] = %q, want %q", i, called[i], want[i])
		}
	}
}

func TestAssignedIssuesJQLFiltersConfiguredIssueTypes(t *testing.T) {
	got := assignedIssuesJQL([]string{"Story", "Service Request", `Type "quoted"`})
	want := `assignee = currentUser() AND statusCategory != Done AND issuetype IN ("Story", "Service Request", "Type \"quoted\"") ORDER BY updated DESC`
	if got != want {
		t.Fatalf("assignedIssuesJQL() = %q, want %q", got, want)
	}
}

func TestAssignedIssuesJQLIncludesAllIssueTypesByDefault(t *testing.T) {
	got := assignedIssuesJQL(nil)
	want := "assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC"
	if got != want {
		t.Fatalf("assignedIssuesJQL() = %q, want %q", got, want)
	}
}

func TestSourceCollectClassifiesEachJiraStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(searchResponse{Issues: []issue{
			jiraIssueWithStatus("RAD-1", "In Progress"),
			jiraIssueWithStatus("RAD-2", "in review"),
			jiraIssueWithStatus("RAD-3", "Selected for Development"),
			jiraIssueWithStatus("RAD-4", " Blocked "),
			jiraDoneIssue("RAD-5"),
		}})
	}))
	defer server.Close()

	t.Setenv("RADAR_JIRA_API_BASE_URL", server.URL)
	t.Setenv("RADAR_JIRA_BASE_URL", "https://jira.example.test")
	t.Setenv("RADAR_JIRA_EMAIL", "me@example.com")
	t.Setenv("RADAR_JIRA_API_TOKEN", "token")
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"jira":{"status_mapping":{"In Progress":"in_progress","In Review":"in_progress","Blocked":"attention","Done":"immediate"},"unmapped_status":"low_priority"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	result := NewSource().Collect(context.Background(), integration.CollectRequest{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	want := []integration.WorkSignal{integration.SignalInProgress, integration.SignalInProgress, integration.SignalLowPriority, integration.SignalAttention, integration.SignalDone}
	if len(result.Observations) != len(want) {
		t.Fatalf("observations = %+v", result.Observations)
	}
	for i := range want {
		if result.Observations[i].Signal != want[i] || result.Observations[i].Reason != result.Observations[i].Ref.Status {
			t.Fatalf("observation[%d] = %+v, want signal %q with original status reason", i, result.Observations[i], want[i])
		}
	}
}

func jiraIssueWithStatus(key, status string) issue {
	var value issue
	value.Key = key
	value.Fields.Summary = "Task " + key
	value.Fields.Status = &struct {
		Name           string `json:"name"`
		StatusCategory *struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"statusCategory"`
	}{Name: status}
	return value
}

func jiraDoneIssue(key string) issue {
	value := jiraIssueWithStatus(key, "Done")
	value.Fields.Status.StatusCategory = &struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}{Key: "done", Name: "Done"}
	return value
}

func TestResolveDoneIssuesMarksMissingDoneIssueDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/issue/RAD-123" {
			t.Fatalf("request = %s %s, want GET /issue/RAD-123", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"10001","key":"RAD-123","fields":{"summary":"Ship reconciliation","status":{"name":"Done","statusCategory":{"key":"done","name":"Done"}}}}`))
	}))
	defer server.Close()

	t.Setenv("RADAR_JIRA_API_BASE_URL", server.URL)
	t.Setenv("RADAR_JIRA_BASE_URL", "https://jira.example.test")
	t.Setenv("RADAR_JIRA_EMAIL", "me@example.com")
	t.Setenv("RADAR_JIRA_API_TOKEN", "token")

	previous := []protocol.Task{{
		ID:        42,
		Kind:      "jira_issue",
		Title:     "RAD-123 Ship reconciliation",
		URL:       "https://jira.example.test/browse/RAD-123",
		Attention: "in_progress",
		Reason:    "jira issue",
		SourceRefs: []protocol.SourceRef{{
			ID:     "jira:issue:RAD-123",
			Source: "jira",
			Kind:   "issue",
			Role:   protocol.SourceRefRoleAuthoritative,
			Title:  "RAD-123 Ship reconciliation",
		}},
	}}

	items := ResolveDoneIssues(context.Background(), previous, nil, true, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if len(items) != 1 {
		t.Fatalf("done items = %d, want 1: %+v", len(items), items)
	}
	if items[0].Kind != "jira_done_issue" || items[0].Attention != "done" || items[0].Reason != "jira done" {
		t.Fatalf("done item = %+v, want jira_done_issue done", items[0])
	}
	if items[0].DoneAt == "" {
		t.Fatalf("DoneAt is empty")
	}
	if items[0].SourceRefs[0].Status != "jira done" {
		t.Fatalf("source ref status = %q, want jira done", items[0].SourceRefs[0].Status)
	}
	if items[0].SourceRefs[0].CanonicalKey != "jira:issue:RAD-123" || !slices.Contains(items[0].SourceRefs[0].LinkingKeys, "ticket:RAD-123") {
		t.Fatalf("source ref linking = %+v", items[0].SourceRefs[0])
	}
}

func basicAuth(user string, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}
