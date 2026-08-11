package jira

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/linking"
	"radar/internal/protocol"
)

func TestInformationalIssueSourceRefContract(t *testing.T) {
	value := jiraIssueWithType("RAD-7", "Epic", "Open")
	observation := informationalObservation(Config{BaseURL: "https://jira.example.test"}, value, issueMention{Key: "RAD-7", TaskID: 42})
	contracttest.AssertValidSourceRefs(t, "jira", []protocol.SourceRef{observation.Ref})
}

func TestCollectFetchesUnassignedTitleReferenceAsInformational(t *testing.T) {
	var requests []searchRequest
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search/jql" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var request searchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
		if strings.HasPrefix(request.JQL, "key IN") {
			_ = json.NewEncoder(w).Encode(searchResponse{Issues: []issue{jiraIssueWithType("RAD-7", "Epic", "Open")}})
			return
		}
		_ = json.NewEncoder(w).Encode(searchResponse{})
	})
	defer server.Close()
	configureJiraSource(t, server.URL, `{}`)

	result := NewSource().Collect(context.Background(), jiraCollectRequest([]protocol.Task{{ID: 42, Title: "Investigate rad-7 rollout"}}))
	if !result.Complete || len(result.Observations) != 1 {
		t.Fatalf("result = %+v", result)
	}
	got := result.Observations[0]
	if got.TargetTaskID != 42 || got.Ref.Role != protocol.SourceRefRoleInformational || got.Ref.ID != "jira:mention:42:RAD-7" {
		t.Fatalf("observation = %+v", got)
	}
	if got.Signal != "" || got.Ref.Signal != "" || got.Ref.CanonicalKey != "" || len(got.Ref.LinkingKeys) != 0 {
		t.Fatalf("informational authority leaked: %+v", got)
	}
	if got.Ref.Metadata["issue_type"] != "Epic" || got.Ref.EntityID != "jira:issue:RAD-7" {
		t.Fatalf("reference = %+v", got.Ref)
	}
	if len(requests) != 2 || requests[1].JQL != `key IN ("RAD-7")` {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestCollectMakesConfiguredTitleReferenceAuthoritative(t *testing.T) {
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		var request searchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(request.JQL, "key IN") {
			_ = json.NewEncoder(w).Encode(searchResponse{Issues: []issue{jiraIssueWithType("RAD-7", " Service Request ", "In Progress")}})
			return
		}
		_ = json.NewEncoder(w).Encode(searchResponse{})
	})
	defer server.Close()
	configureJiraSource(t, server.URL, `{"authoritative_issue_types":["service request"]}`)

	result := NewSource().Collect(context.Background(), jiraCollectRequest([]protocol.Task{{ID: 3, Title: "RAD-7 rollout"}}))
	if len(result.Observations) != 1 {
		t.Fatalf("observations = %+v", result.Observations)
	}
	got := result.Observations[0]
	if got.TargetTaskID != 3 || got.Ref.Role != protocol.SourceRefRoleAuthoritative || got.Signal != integration.SignalInProgress {
		t.Fatalf("observation = %+v", got)
	}
	if got.Ref.CanonicalKey != "jira:issue:RAD-7" || len(got.Ref.LinkingKeys) != 1 || got.Ref.LinkingKeys[0] != "mark:RAD-7" {
		t.Fatalf("linking = %+v", got.Ref)
	}
}

func TestCollectDoesNotDirectFetchAssignedTitleDuplicate(t *testing.T) {
	var requests []string
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		_ = json.NewEncoder(w).Encode(searchResponse{Issues: []issue{jiraIssueWithType("RAD-7", "Task", "Open")}})
	})
	defer server.Close()
	configureJiraSource(t, server.URL, `{}`)

	result := NewSource().Collect(context.Background(), jiraCollectRequest([]protocol.Task{{ID: 5, Title: "RAD-7 rollout"}}))
	if len(result.Observations) != 1 || result.Observations[0].TargetTaskID != 5 {
		t.Fatalf("observations = %+v", result.Observations)
	}
	if !reflect.DeepEqual(requests, []string{"POST /search/jql"}) {
		t.Fatalf("requests = %+v, want one assigned search", requests)
	}
}

func TestCollectKeepsOtherReferencesAndPreviousMissingReference(t *testing.T) {
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		var request searchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(request.JQL, "key IN") {
			_ = json.NewEncoder(w).Encode(searchResponse{Issues: []issue{jiraIssueWithType("RAD-2", "Epic", "Open")}})
			return
		}
		_ = json.NewEncoder(w).Encode(searchResponse{})
	})
	defer server.Close()
	configureJiraSource(t, server.URL, `{}`)
	previousRef := protocol.SourceRef{ID: "jira:mention:9:RAD-1", EntityID: "jira:issue:RAD-1", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleInformational, Title: "RAD-1 Existing", Metadata: map[string]string{"key": "RAD-1"}}
	previous := []protocol.Task{{ID: 9, Title: "Compare RAD-1 and RAD-2", SourceRefs: []protocol.SourceRef{previousRef}}}

	result := NewSource().Collect(context.Background(), jiraCollectRequest(previous))
	if result.Complete || result.SourceStatus.Status != "error" || !strings.Contains(result.SourceStatus.Detail, "1 title references unavailable") {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Observations) != 2 {
		t.Fatalf("observations = %+v, want RAD-2 and preserved RAD-1", result.Observations)
	}
	ids := []string{result.Observations[0].Ref.ID, result.Observations[1].Ref.ID}
	if !contains(ids, "jira:mention:9:RAD-1") || !contains(ids, "jira:mention:9:RAD-2") {
		t.Fatalf("observation IDs = %+v", ids)
	}
}

func TestCollectBoundsTitleReferenceFetches(t *testing.T) {
	requestedKeys := make([]string, 0)
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		var request searchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(request.JQL, "key IN") {
			_ = json.NewEncoder(w).Encode(searchResponse{})
			return
		}
		for i := 1; i <= maxTitleDiscoveredIssues; i++ {
			requestedKeys = append(requestedKeys, "RAD-"+strconv.Itoa(i))
		}
		issues := make([]issue, 0, len(requestedKeys))
		for _, key := range requestedKeys {
			issues = append(issues, jiraIssueWithType(key, "Epic", "Open"))
		}
		_ = json.NewEncoder(w).Encode(searchResponse{Issues: issues})
	})
	defer server.Close()
	configureJiraSource(t, server.URL, `{"authoritative_issue_types":[]}`)
	titles := make([]string, 0, maxTitleDiscoveredIssues+1)
	for i := 1; i <= maxTitleDiscoveredIssues+1; i++ {
		titles = append(titles, "RAD-"+strconv.Itoa(i))
	}

	result := NewSource().Collect(context.Background(), jiraCollectRequest([]protocol.Task{{ID: 1, Title: strings.Join(titles, " ")}}))
	if result.Complete || len(requestedKeys) != maxTitleDiscoveredIssues || len(result.Observations) != maxTitleDiscoveredIssues {
		t.Fatalf("requested=%d observations=%d result=%+v", len(requestedKeys), len(result.Observations), result)
	}
	if requestedKeys[0] != "RAD-1" || requestedKeys[len(requestedKeys)-1] != "RAD-50" || !strings.Contains(result.SourceStatus.Detail, "truncated") {
		t.Fatalf("request bounds/status = %+v / %+v", requestedKeys, result.SourceStatus)
	}
}

func TestDiscoverIssueMentionsUsesSourceTitlesAndStableOrder(t *testing.T) {
	tasks := []protocol.Task{{
		ID: 12, Title: "Compare RAD-2 then RAD-1",
		SourceRefs: []protocol.SourceRef{
			{Source: "jira", Kind: "issue", Title: "RAD-99 Jira supplied"},
			{Source: "github", Kind: "pull_request", Title: "Also RAD-3"},
		},
	}}
	mentions := discoverIssueMentions(tasks, linking.NewMarkMatcher([]string{"RAD"}))
	keys := make([]string, 0, len(mentions))
	for _, mention := range mentions {
		keys = append(keys, mention.Key)
	}
	if !reflect.DeepEqual(keys, []string{"RAD-2", "RAD-1", "RAD-3"}) {
		t.Fatalf("keys = %+v", keys)
	}
}

func jiraSourceServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
}

func configureJiraSource(t *testing.T, apiURL, jiraJSON string) {
	t.Helper()
	t.Setenv("RADAR_JIRA_API_BASE_URL", apiURL)
	t.Setenv("RADAR_JIRA_BASE_URL", "https://jira.example.test")
	t.Setenv("RADAR_JIRA_EMAIL", "me@example.test")
	t.Setenv("RADAR_JIRA_API_TOKEN", "token")
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"linking_mark_prefixes":["RAD"],"jira":`+jiraJSON+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func jiraCollectRequest(previous []protocol.Task) integration.CollectRequest {
	return integration.CollectRequest{
		Previous: previous, LinkingMarks: linking.NewMarkMatcher([]string{"RAD"}),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func jiraIssueWithType(key, issueType, status string) issue {
	value := jiraIssueWithStatus(key, status)
	value.Fields.IssueType = &struct {
		Name string `json:"name"`
	}{Name: issueType}
	if strings.EqualFold(status, "Done") {
		value.Fields.Status.StatusCategory = &struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		}{Key: "done", Name: "Done"}
	}
	return value
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
