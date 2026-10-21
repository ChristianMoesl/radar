package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
	"radar/internal/setup"
)

func TestOldTerminalRunsDoNotFloodDiscovery(t *testing.T) {
	var runs []resource
	for _, status := range []string{"errored", "applied", "policy_soft_failed", "discarded", "planning", "policy_override"} {
		run := runFixture("run-"+status, status, "user-me")
		var attrs map[string]any
		_ = json.Unmarshal(run.Attributes, &attrs)
		attrs["created-at"] = time.Now().Add(-100 * 24 * time.Hour).Format(time.RFC3339)
		run.Attributes = raw(attrs)
		runs = append(runs, run)
	}
	updated := runs[0]
	updated.ID = "run-updated"
	var attrs map[string]any
	_ = json.Unmarshal(updated.Attributes, &attrs)
	attrs["status-timestamps"] = map[string]string{"errored-at": time.Now().Format(time.RFC3339)}
	updated.Attributes = raw(attrs)
	runs = append(runs, updated, runFixture("run-new", "planning", "user-me"))
	s, _, _ := fixtureSource(t, runs...)
	result := s.Collect(context.Background(), integration.CollectRequest{})
	if !result.Complete || len(result.Observations) != 4 {
		t.Fatal("old terminal history surfaced or active/recently updated runs lost", result)
	}
	for _, observation := range result.Observations {
		switch observation.Ref.Metadata["run_id"] {
		case "run-planning", "run-policy_override", "run-updated", "run-new":
		default:
			t.Fatal("unexpected historical outcome", observation.Ref.ID)
		}
	}
}

func TestRunScanSupportsThirteenWorkspacesBeyondOldBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, cfg := fixtureSource(t)
		started := time.Now()
		var requests []time.Time
		spaces := map[string]string{}
		for i := range 13 {
			spaces[fmt.Sprintf("ws-%02d", i)] = fmt.Sprintf("workspace-%02d", i)
		}
		c := client{profile: "default", output: func(_ context.Context, command setup.Command) (string, error) {
			u, _ := url.Parse(command.Args[1])
			if command.Args[0] != "api" || !strings.HasSuffix(u.Path, "/runs") || u.Query().Has("filter[status_group]") || u.Query().Has("filter[timeframe]") || u.Query().Get("filter[operation]") != operations || u.Query().Get("page[size]") != "100" {
				t.Fatal("scan changed run scope or duplicated status-group scans", command.Args)
			}
			if len(requests) > 0 && time.Since(requests[len(requests)-1]) < runListInterval {
				t.Fatal("run-list requests were not paced across pages/workspaces")
			}
			requests = append(requests, time.Now())
			spaceID := strings.Split(u.Path, "/")[2]
			if spaces[spaceID] == "" {
				t.Fatal("unselected workspace scanned", spaceID)
			}
			if u.Query().Get("page[number]") == "1" {
				// Empty does not mean final: Terraform may paginate an unfiltered
				// history even when an operation/status filter leaves no records.
				return encode(map[string]any{"data": []resource{}, "links": map[string]any{"next": "/api/v2" + u.Path + "?page%5Bnumber%5D=2"}}), nil
			}
			if u.Query().Get("page[number]") != "2" {
				t.Fatal("unexpected page", u)
			}
			active := runFixture("run-"+spaceID, "planning", "user-me")
			active.Relationships["workspace"] = rel(spaceID, "workspaces")
			terminal := active
			terminal.ID += "-done"
			terminal.Attributes = runFixture(terminal.ID, "applied", "user-me").Attributes
			return encode(map[string]any{"data": []resource{active, terminal}, "meta": map[string]any{"pagination": map[string]any{"next-page": nil}}}), nil
		}}
		result := s.collect(context.Background(), session{client: c, host: "app.terraform.io", userID: "user-me", spaces: spaces}, cfg, integration.CollectRequest{})
		if !result.Complete || result.SourceStatus.Status != "ok" || len(requests) != 26 || len(result.Observations) != 26 {
			t.Fatalf("incomplete multi-workspace scan: requests=%d result=%+v", len(requests), result)
		}
		if elapsed := time.Since(started); elapsed != 25*runListInterval {
			t.Fatal("unexpected scan duration", elapsed)
		}
	})
}

func TestCanceledRunScanIsPartialAndRetainsKnownRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, api, _ := fixtureSource(t, runFixture("run-known", "planning", "user-me"))
		first := s.Collect(context.Background(), integration.CollectRequest{})
		requests := 0
		api.pages = func(u *url.URL) (string, error) {
			requests++
			page, _ := strconv.Atoi(u.Query().Get("page[number]"))
			r := runFixture(fmt.Sprintf("run-page%d", page), "planning", "user-other")
			next := fmt.Sprintf("https://app.terraform.io/api/v2/workspaces/ws-one/runs?page%%5Bnumber%%5D=%d", page+1)
			return encode(map[string]any{"data": []resource{r}, "links": map[string]any{"next": next}}), nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*runListInterval+runListInterval/2)
		defer cancel()
		result := s.Collect(ctx, integration.CollectRequest{Previous: tasksFrom(first)})
		if result.Complete || result.SourceStatus.Status != "partial" || requests != 3 || len(result.Observations) != 1 || result.Observations[0].Signal != integration.SignalInProgress {
			t.Fatal("cancellation implied completion or lost known work", result, requests)
		}
		if !strings.Contains(result.SourceStatus.Detail, "interrupted") {
			t.Fatal("missing cancellation diagnostic", result.SourceStatus)
		}
	})
}

func TestRunPagePacingAllowsSlowRequestsAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		if err := waitForRunPage(context.Background(), time.Time{}); err != nil || time.Now() != start {
			t.Fatal("first request should be immediate", err)
		}
		time.Sleep(runListInterval + time.Second)
		now := time.Now()
		if err := waitForRunPage(context.Background(), start); err != nil || time.Now() != now {
			t.Fatal("slow requests should not incur another full interval", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := waitForRunPage(ctx, time.Time{}); err != context.Canceled || time.Now() != now {
			t.Fatal("canceled scan should not make an initial request", err)
		}
	})
}

func TestUnavailableImmutableContextRetainsKnownAssociations(t *testing.T) {
	s, _, cfg := fixtureSource(t)
	r := runFixture("run-one", "planning", "user-me")
	r.Relationships["configuration-version"] = rel("cv-one", "configuration-versions")
	r.Relationships["plan"] = rel("plan-one", "plans")
	git := ingressAttributes{Branch: "ABC-123", Repository: "acme/app", CloneURL: "https://github.com/acme/app.git", PRURL: "https://github.com/acme/app/pull/7", PRNumber: 7, IsPR: true, DefaultBranch: new(false)}
	included := []resource{{ID: "cv-one", Type: "configuration-versions", Attributes: raw(map[string]any{}), Relationships: map[string]json.RawMessage{"ingress-attributes": rel("ia-one", "ingress-attributes")}}, {ID: "ia-one", Type: "ingress-attributes", Attributes: raw(git)}, {ID: "plan-one", Type: "plans", Attributes: raw(map[string]any{"resource-additions": 1, "resource-changes": 2, "resource-destructions": 0})}}
	session := session{host: "app.terraform.io", userID: "user-me", spaces: map[string]string{"ws-one": "production-network"}, client: client{profile: "default", output: func(context.Context, setup.Command) (string, error) { return "", fmt.Errorf("temporarily unavailable") }}}
	req := integration.CollectRequest{LinkingMarks: linking.NewMarkMatcher([]string{"ABC"})}
	first, owned, err := s.observe(context.Background(), session, cfg, r, included, req)
	if err != nil || !owned {
		t.Fatal(err)
	}
	req.Previous = []protocol.Task{{SourceRefs: []protocol.SourceRef{first.Ref}}}
	second, _, err := s.observe(context.Background(), session, cfg, r, nil, req)
	if err != nil || !strings.Contains(strings.Join(second.Ref.LinkingKeys, ","), "github:pr:acme/app:7") || second.Ref.Metadata["changes"] != "2" {
		t.Fatal("transient enrichment failure lost confirmed facts", second, err)
	}
	r.Relationships["configuration-version"] = rel("cv-other", "configuration-versions")
	third, _, err := s.observe(context.Background(), session, cfg, r, nil, req)
	if err != nil || strings.Contains(strings.Join(third.Ref.LinkingKeys, ","), "github:pr:") {
		t.Fatal("context reused across configuration identity")
	}
}
