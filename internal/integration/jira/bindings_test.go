package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/linking"
	"radar/internal/protocol"
)

func TestResolveBindingsReadsUnassignedOpenAndDoneIssues(t *testing.T) {
	for _, test := range []struct {
		status, category string
		signal           integration.WorkSignal
	}{
		{"In Progress", "indeterminate", integration.SignalInProgress},
		{"Blocked", "indeterminate", integration.SignalAttention},
		{"Done", "done", integration.SignalDone},
	} {
		t.Run(test.status, func(t *testing.T) {
			var fetched atomic.Int32
			server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/search/jql" {
					_ = json.NewEncoder(w).Encode(searchResponse{})
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/issue/ABC-7" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				fetched.Add(1)
				_ = json.NewEncoder(w).Encode(boundJiraFixture(test.status, test.category))
			})
			defer server.Close()
			configureJiraSource(t, server.URL, `{"status_mapping":{"In Progress":"in_progress","Blocked":"attention","Done":"immediate"}}`)
			req := boundJiraRequest()
			// Cold cache: no previous task and no issue in active assigned search.
			req.Result = NewSource().Collect(context.Background(), integration.CollectRequest{Logger: req.Logger, LinkingMarks: req.LinkingMarks})
			if !req.Result.Complete || len(req.Result.Observations) != 0 {
				t.Fatalf("ordinary search = %+v", req.Result)
			}
			result := NewSource().ResolveBindings(context.Background(), req)
			if !result.Complete || len(result.Observations) != 1 || fetched.Load() != 1 {
				t.Fatalf("result = %+v; fetched=%d", result, fetched.Load())
			}
			got := result.Observations[0]
			contracttest.AssertValidSourceRefs(t, "jira", []protocol.SourceRef{got.Ref})
			if got.Ref.ID != "jira:issue:ABC-7" || got.Signal != test.signal || got.Ref.Signal != string(test.signal) || got.TargetTaskID != 0 || !contributingBoundIssue(got.Ref) || !slices.Contains(got.Ref.LinkingKeys, "mark:ABC-7") {
				t.Fatalf("bound observation = %+v", got)
			}
			// Epic is outside the configured authoritative type filter. Explicit
			// tracking remains a contributor; ordinary mentions still do not.
			if got.Ref.Metadata["issue_type"] != "Epic" || got.Ref.URL != "https://jira.example.test/browse/ABC-7" {
				t.Fatalf("source facts = %+v", got.Ref)
			}
		})
	}
}

func TestResolveBindingsSkipsFreshIssuesAndPromotesOnlyExplicitlyBoundMentions(t *testing.T) {
	var called atomic.Int32
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()
	configureJiraSource(t, server.URL, `{"authoritative_issue_types":[]}`)
	value := boundJiraFixture("In Progress", "indeterminate")
	ref := sourceRefFromIssue(Config{BaseURL: "https://jira.example.test"}, value, linking.NewMarkMatcher([]string{"ABC"}))
	ref.Signal = "in_progress"
	req := boundJiraRequest()
	req.Result.Observations = []integration.Observation{{Ref: ref, Signal: integration.SignalInProgress}}
	result := NewSource().ResolveBindings(context.Background(), req)
	if !result.Complete || len(result.Observations) != 0 {
		t.Fatalf("fresh = %+v", result)
	}
	mention := informationalObservation(Config{BaseURL: "https://jira.example.test"}, value, issueMention{Key: "ABC-7", TaskID: 42})
	unrelated := informationalObservation(Config{BaseURL: "https://jira.example.test"}, boundJiraFixture("Done", "done"), issueMention{Key: "ABC-7", TaskID: 99})
	req.Result.Observations = []integration.Observation{mention, unrelated}
	result = NewSource().ResolveBindings(context.Background(), req)
	if !result.Complete || len(result.Observations) != 1 {
		t.Fatalf("bound mention = %+v", result)
	}
	got := result.Observations[0]
	if !contributingBoundIssue(got.Ref) || got.Ref.ID != "jira:issue:ABC-7" || got.Signal != integration.SignalInProgress || got.TargetTaskID != 0 || got.Ref.Presentation.TitleOrder != nil {
		t.Fatalf("promoted observation = %+v", got)
	}
	if req.Result.Observations[0].Ref.Role != protocol.SourceRefRoleInformational || req.Result.Observations[1].Ref.Role != protocol.SourceRefRoleInformational {
		t.Fatal("binding mutated ordinary informational observations")
	}
	if called.Load() != 0 {
		t.Fatalf("fresh refs made %d requests", called.Load())
	}
}

func TestResolveBindingsReusesHistoricalTerminalIssueAndRechecksPreservedResults(t *testing.T) {
	var called atomic.Int32
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		_ = json.NewEncoder(w).Encode(boundJiraFixture("In Progress", "indeterminate"))
	})
	defer server.Close()
	configureJiraSource(t, server.URL, `{}`)
	ref := sourceRefFromIssue(Config{BaseURL: "https://jira.example.test"}, boundJiraFixture("Done", "done"))
	ref.Signal = "done"
	req := boundJiraRequest()
	req.Previous = []protocol.Task{{Attention: "in_progress", DoneAt: "2020-01-01T10:00:00Z", SourceRefs: []protocol.SourceRef{ref}}}
	result := NewSource().ResolveBindings(context.Background(), req)
	if !result.Complete || len(result.Observations) != 1 || !reflect.DeepEqual(result.Observations[0].Ref, ref) || called.Load() != 0 {
		t.Fatalf("terminal reuse = %+v; requests=%d", result, called.Load())
	}
	ref.Signal = "in_progress"
	req.Previous[0].Attention = "done" // Aggregate/manual done is not remote completion.
	req.Previous[0].SourceRefs[0] = ref
	req.Result = integration.CollectResult{Observations: []integration.Observation{{Ref: ref, Signal: integration.SignalInProgress}}}
	result = NewSource().ResolveBindings(context.Background(), req)
	if !result.Complete || len(result.Observations) != 1 || result.Observations[0].Signal != integration.SignalInProgress || called.Load() != 1 {
		t.Fatalf("preserved ref recheck = %+v; requests=%d", result, called.Load())
	}
}

func TestResolveBindingsDoesNotCompleteMissingFailedOrInvalidIssues(t *testing.T) {
	for _, name := range []string{"missing", "error", "wrong identity", "missing status"} {
		t.Run(name, func(t *testing.T) {
			server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
				value := boundJiraFixture("Done", "done")
				switch name {
				case "missing":
					w.WriteHeader(http.StatusNotFound)
					return
				case "error":
					w.WriteHeader(http.StatusInternalServerError)
					return
				case "wrong identity":
					value.Key = "ABC-8"
				case "missing status":
					value.Fields.Status = nil
				}
				_ = json.NewEncoder(w).Encode(value)
			})
			defer server.Close()
			configureJiraSource(t, server.URL, `{}`)
			result := NewSource().ResolveBindings(context.Background(), boundJiraRequest())
			if result.Complete || len(result.Observations) != 0 || result.SourceStatus == nil || result.SourceStatus.Status != "error" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestResolveBindingsValidatesIssueIDsAndAvailabilityBeforeFetch(t *testing.T) {
	var called atomic.Int32
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()
	configureJiraSource(t, server.URL, `{}`)
	for _, id := range []string{"jira:mention:42:ABC-7", "jira:issue:abc-7", "jira:issue:ABC-0", "jira:issue:ABC-07", "jira:issue:ABC--7", "jira:issue:ABC-7/../8", "jira:issue:ABC-7?x", "github:pr:acme/app:7"} {
		req := boundJiraRequest()
		req.Bindings[0].ID = id
		if result := NewSource().ResolveBindings(context.Background(), req); result.Complete || len(result.Observations) != 0 {
			t.Fatalf("invalid %q accepted: %+v", id, result)
		}
	}
	for _, field := range []string{"source", "kind", "key", "work_item"} {
		req := boundJiraRequest()
		switch field {
		case "source":
			req.Bindings[0].Source = "github"
		case "kind":
			req.Bindings[0].Kind = "pull_request"
		case "key":
			req.Bindings[0].Key = "lifetime"
		case "work_item":
			req.Bindings[0].WorkItem = false
		}
		if result := NewSource().ResolveBindings(context.Background(), req); result.Complete || len(result.Observations) != 0 {
			t.Fatalf("invalid %s accepted: %+v", field, result)
		}
	}
	configureJiraSource(t, server.URL, `{"enabled":false}`)
	if result := NewSource().ResolveBindings(context.Background(), boundJiraRequest()); result.Complete || len(result.Observations) != 0 || result.SourceStatus.Status != "disabled" {
		t.Fatalf("disabled = %+v", result)
	}
	configureJiraSource(t, server.URL, `{}`)
	t.Setenv("RADAR_JIRA_API_TOKEN", "")
	if result := NewSource().ResolveBindings(context.Background(), boundJiraRequest()); result.Complete || len(result.Observations) != 0 {
		t.Fatalf("missing credentials = %+v", result)
	}
	if called.Load() != 0 {
		t.Fatalf("invalid/disabled bindings made %d requests", called.Load())
	}
}

func TestResolveBindingsDeduplicatesIssuesWithoutDiscardingSuccessfulBindings(t *testing.T) {
	var called atomic.Int32
	server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		_ = json.NewEncoder(w).Encode(boundJiraFixture("Done", "done"))
	})
	defer server.Close()
	configureJiraSource(t, server.URL, `{}`)
	req := boundJiraRequest()
	req.Bindings = append(req.Bindings, req.Bindings[0], protocol.SourceBinding{Source: "jira", Kind: "issue", ID: "bad", WorkItem: true})
	result := NewSource().ResolveBindings(context.Background(), req)
	if result.Complete || len(result.Observations) != 1 || called.Load() != 1 {
		t.Fatalf("result = %+v; called=%d", result, called.Load())
	}
}

func boundJiraRequest() integration.BindingRequest {
	return integration.BindingRequest{Bindings: []protocol.SourceBinding{{Source: "jira", Kind: "issue", ID: "jira:issue:ABC-7", WorkItem: true}}, Result: integration.CollectResult{Complete: true}, LinkingMarks: linking.NewMarkMatcher([]string{"ABC"}), Logger: jiraCollectRequest(nil).Logger}
}

func boundJiraFixture(status, category string) issue {
	value := jiraIssueWithType("ABC-7", "Epic", status)
	value.Fields.Status.StatusCategory = &struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}{Key: category, Name: category}
	return value
}
