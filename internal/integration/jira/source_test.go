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
	"radar/internal/protocol"
)

func TestCollectFetchesUnassignedTitleReferenceAsInformational(t *testing.T) {
	var requests []string
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/search/jql":
			_ = json.NewEncoder(w).Encode(searchResponse{})
		case "/issue/RAD-7":
			_ = json.NewEncoder(w).Encode(jiraIssueWithType("RAD-7", "Epic", "Open"))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
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
	if got.Ref.Metadata["issue_type"] != "Epic" || got.Ref.Metadata["canonical_id"] != "jira:issue:RAD-7" {
		t.Fatalf("metadata = %+v", got.Ref.Metadata)
	}
	if !reflect.DeepEqual(requests, []string{"POST /search/jql", "GET /issue/RAD-7"}) {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestCollectMakesConfiguredTitleReferenceAuthoritative(t *testing.T) {
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/jql" {
			_ = json.NewEncoder(w).Encode(searchResponse{})
			return
		}
		_ = json.NewEncoder(w).Encode(jiraIssueWithType("RAD-7", " Service Request ", "In Progress"))
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
	if got.Ref.CanonicalKey != "jira:issue:RAD-7" || len(got.Ref.LinkingKeys) != 1 || got.Ref.LinkingKeys[0] != "ticket:RAD-7" {
		t.Fatalf("linking = %+v", got.Ref)
	}
}

func TestCollectTreatsExplicitAttachmentAsAuthoritative(t *testing.T) {
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/jql" {
			t.Fatal("assigned search must be skipped for explicit empty authoritative types")
		}
		_ = json.NewEncoder(w).Encode(jiraIssueWithType("RAD-7", "Epic", "Done"))
	})
	defer server.Close()
	configureJiraSource(t, server.URL, `{"authoritative_issue_types":[]}`)

	previous := []protocol.Task{{ID: 8, Title: "No key remains", Metadata: map[string]string{"association_keys": "ticket:RAD-7"}}}
	result := NewSource().Collect(context.Background(), jiraCollectRequest(previous))
	if len(result.Observations) != 1 || result.Observations[0].Ref.Role != protocol.SourceRefRoleAuthoritative || result.Observations[0].Signal != integration.SignalDone {
		t.Fatalf("observations = %+v", result.Observations)
	}
	if !result.Complete || !strings.Contains(result.SourceStatus.Detail, "assigned search skipped") {
		t.Fatalf("result status = %+v", result)
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

func TestCollectKeepsOtherReferencesAndPreviousFailedReference(t *testing.T) {
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/jql":
			_ = json.NewEncoder(w).Encode(searchResponse{})
		case "/issue/RAD-1":
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		case "/issue/RAD-2":
			_ = json.NewEncoder(w).Encode(jiraIssueWithType("RAD-2", "Epic", "Open"))
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	})
	defer server.Close()
	configureJiraSource(t, server.URL, `{}`)
	previousRef := protocol.SourceRef{ID: "jira:mention:9:RAD-1", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleInformational, Title: "RAD-1 Existing", Metadata: map[string]string{"key": "RAD-1"}}
	previous := []protocol.Task{{ID: 9, Title: "Compare RAD-1 and RAD-2", SourceRefs: []protocol.SourceRef{previousRef}}}

	result := NewSource().Collect(context.Background(), jiraCollectRequest(previous))
	if result.Complete || result.SourceStatus.Status != "error" || !strings.Contains(result.SourceStatus.Detail, "1 direct fetches failed") {
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
	requested := make([]string, 0)
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Path)
		key := strings.TrimPrefix(r.URL.Path, "/issue/")
		_ = json.NewEncoder(w).Encode(jiraIssueWithType(key, "Epic", "Open"))
	})
	defer server.Close()
	configureJiraSource(t, server.URL, `{"authoritative_issue_types":[]}`)
	titles := make([]string, 0, maxTitleDiscoveredIssues+1)
	for i := 1; i <= maxTitleDiscoveredIssues+1; i++ {
		titles = append(titles, "RAD-"+strconv.Itoa(i))
	}

	result := NewSource().Collect(context.Background(), jiraCollectRequest([]protocol.Task{{ID: 1, Title: strings.Join(titles, " ")}}))
	if result.Complete || len(requested) != maxTitleDiscoveredIssues || len(result.Observations) != maxTitleDiscoveredIssues {
		t.Fatalf("requests=%d observations=%d result=%+v", len(requested), len(result.Observations), result)
	}
	if requested[0] != "/issue/RAD-1" || requested[len(requested)-1] != "/issue/RAD-50" || !strings.Contains(result.SourceStatus.Detail, "truncated") {
		t.Fatalf("request bounds/status = %+v / %+v", requested, result.SourceStatus)
	}
}

func TestDiscoverIssueMentionsUsesDurableTitlesAndStableOrder(t *testing.T) {
	tasks := []protocol.Task{{
		ID: 12, Title: "RAD-99 Jira supplied", Metadata: map[string]string{"manual_title": "Compare RAD-2 then RAD-1"},
		SourceRefs: []protocol.SourceRef{
			{Source: "jira", Kind: "issue", Title: "RAD-99 Jira supplied"},
			{Source: "github", Kind: "pull_request", Title: "Also RAD-3"},
		},
	}}
	mentions := discoverIssueMentions(tasks)
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
	if err := os.WriteFile(path, []byte(`{"jira":`+jiraJSON+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func jiraCollectRequest(previous []protocol.Task) integration.CollectRequest {
	return integration.CollectRequest{Previous: previous, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
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
