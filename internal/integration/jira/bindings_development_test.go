package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/github"
	"radar/internal/protocol"
)

func TestResolveBindingsDiscoversDevelopmentRelationshipsOutsideOrdinarySearches(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "unassigned renamed task", true: "completed while stopped"}[terminal], func(t *testing.T) {
			var searches, fetched, summaries, details atomic.Int32
			server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/search/jql":
					searches.Add(1)
					var request searchRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					if strings.HasPrefix(request.JQL, "key IN") {
						t.Error("renamed note unexpectedly triggered title discovery")
					}
					_ = json.NewEncoder(w).Encode(searchResponse{})
				case "/issue/ABC-7":
					fetched.Add(1)
					value := boundJiraFixture("In Progress", "indeterminate")
					if terminal {
						value = boundJiraFixture("Done", "done")
					}
					value.ID = "10007"
					_ = json.NewEncoder(w).Encode(value)
				case "/rest/dev-status/1.0/issue/summary":
					summaries.Add(1)
					if r.URL.Query().Get("issueId") != "10007" {
						t.Errorf("issueId = %q", r.URL.Query().Get("issueId"))
					}
					_, _ = w.Write([]byte(`{"summary":{"pullrequest":{"byInstanceType":{"github-instance":{"count":1,"name":"GitHub"}}}}}`))
				case "/rest/dev-status/1.0/issue/detail":
					details.Add(1)
					if r.URL.Query().Get("issueId") != "10007" || r.URL.Query().Get("applicationType") != "github-instance" || r.URL.Query().Get("dataType") != "pullrequest" {
						t.Errorf("detail query = %v", r.URL.Query())
					}
					_, _ = w.Write([]byte(`{"detail":[{"pullRequests":[{"id":"#42","url":"https://github.com/acme/app/pull/42","repositoryName":"acme/app","source":{"branch":"feature/new-work"}}]}]}`))
				default:
					t.Errorf("unexpected local Jira request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			defer server.Close()
			configureJiraSource(t, server.URL, `{}`)
			t.Setenv("RADAR_JIRA_BASE_URL", server.URL) // All development requests stay on the local mock.
			source := NewSource(github.NewSource())
			req := boundJiraRequest()
			req.Previous = []protocol.Task{{ID: 17, Title: "Renamed adopted work", Muted: true, SourceRefs: []protocol.SourceRef{{Source: "obsidian", Kind: "task", Title: "Renamed adopted work"}}}}
			req.Result = source.Collect(context.Background(), integration.CollectRequest{Previous: req.Previous, LinkingMarks: req.LinkingMarks, Logger: req.Logger})
			if !req.Result.Complete || len(req.Result.Observations) != 0 || searches.Load() != 1 {
				t.Fatalf("ordinary result=%+v searches=%d", req.Result, searches.Load())
			}
			result := source.ResolveBindings(context.Background(), req)
			if !result.Complete || len(result.Observations) != 1 || fetched.Load() != 1 || summaries.Load() != 1 || details.Load() != 1 {
				t.Fatalf("bound result=%+v issue=%d summary=%d detail=%d", result, fetched.Load(), summaries.Load(), details.Load())
			}
			ref := result.Observations[0].Ref
			for _, key := range []string{"mark:ABC-7", "github:pr:acme/app:42", "branch:acme/app:feature-new-work"} {
				if !slices.Contains(ref.LinkingKeys, key) {
					t.Fatalf("bound Jira ref missing new development relationship %q: %+v", key, ref)
				}
			}
			if result.SourceStatus == nil || result.SourceStatus.Status != "ok" || !strings.Contains(result.SourceStatus.Detail, "1 bound issue development pull requests") || !strings.Contains(result.SourceStatus.Detail, req.Result.SourceStatus.Detail) {
				t.Fatalf("development status = %+v", result.SourceStatus)
			}
			if terminal {
				if result.Observations[0].Signal != integration.SignalDone {
					t.Fatalf("terminal = %+v", result.Observations[0])
				}
				req.Previous = []protocol.Task{{TrackingOnly: true, Attention: "done", DoneAt: "2020-01-01T10:00:00Z", SourceRefs: []protocol.SourceRef{ref}}}
				req.Result = integration.CollectResult{Complete: true}
				reused := source.ResolveBindings(context.Background(), req)
				if !reused.Complete || len(reused.Observations) != 1 || fetched.Load() != 1 || summaries.Load() != 1 || details.Load() != 1 || !slices.Contains(reused.Observations[0].Ref.LinkingKeys, "github:pr:acme/app:42") {
					t.Fatalf("historical terminal revalidated or lost relationships: %+v", reused)
				}
			}
		})
	}
}

func TestResolveBindingsDevelopmentPartialFailuresMatchOrdinaryCollection(t *testing.T) {
	for _, name := range []string{"failed", "unavailable", "invalid relationship"} {
		t.Run(name, func(t *testing.T) {
			server := jiraSourceServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/issue/ABC-7":
					value := boundJiraFixture("In Progress", "indeterminate")
					value.ID = "10007"
					_ = json.NewEncoder(w).Encode(value)
				case "/rest/dev-status/1.0/issue/summary":
					if name == "failed" {
						http.Error(w, "temporary failure", http.StatusBadGateway)
						return
					}
					if name == "unavailable" {
						http.NotFound(w, r)
						return
					}
					_, _ = w.Write([]byte(`{"summary":{"pullrequest":{"byInstanceType":{"github-instance":{"count":1,"name":"GitHub"}}}}}`))
				case "/rest/dev-status/1.0/issue/detail":
					_, _ = w.Write([]byte(`{"detail":[{"pullRequests":[{"id":"#42","url":"https://github.com/acme/app/pull/42","repositoryName":"acme/other","source":{"branch":"feature/new-work"}}]}]}`))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			})
			defer server.Close()
			configureJiraSource(t, server.URL, `{}`)
			t.Setenv("RADAR_JIRA_BASE_URL", server.URL)
			previous := sourceRefFromIssue(Config{BaseURL: server.URL}, boundJiraFixture("In Progress", "indeterminate"))
			previous.LinkingKeys = []string{"mark:ABC-7", "github:pr:acme/app:7", "branch:acme/app:existing-work"}
			req := boundJiraRequest()
			req.Previous = []protocol.Task{{SourceRefs: []protocol.SourceRef{previous}}}
			req.Result.SourceStatus = &protocol.SourceStatus{Name: "jira", Status: "ok", Detail: "ordinary search complete"}
			result := NewSource(github.NewSource()).ResolveBindings(context.Background(), req)
			if !result.Complete || len(result.Observations) != 1 || result.Observations[0].Signal != integration.SignalInProgress {
				t.Fatalf("development failure invalidated resolved issue facts: %+v", result)
			}
			wantStatus := "error"
			if name == "unavailable" {
				wantStatus = "ok"
			}
			if result.SourceStatus == nil || result.SourceStatus.Status != wantStatus || !strings.Contains(result.SourceStatus.Detail, "ordinary search complete") {
				t.Fatalf("status = %+v", result.SourceStatus)
			}
			ref := result.Observations[0].Ref
			if name == "invalid relationship" {
				if slices.Contains(ref.LinkingKeys, "github:pr:acme/app:42") || slices.Contains(ref.LinkingKeys, "github:pr:acme/app:7") || !strings.Contains(result.SourceStatus.Detail, "1 invalid bound issue development pull requests") {
					t.Fatalf("invalid/successfully removed development relationship kept: %+v", result)
				}
			} else {
				for _, key := range []string{"github:pr:acme/app:7", "branch:acme/app:existing-work"} {
					if !slices.Contains(ref.LinkingKeys, key) {
						t.Fatalf("lost previous relationship %q after %s: %+v", key, name, ref)
					}
				}
			}
		})
	}
}
