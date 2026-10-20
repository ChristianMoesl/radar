package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
	"radar/internal/setup"
)

func TestRequestBudgetIsPartialAndOldFailuresDoNotFloodDiscovery(t *testing.T) {
	old := runFixture("run-old", "errored", "user-me")
	var attrs map[string]any
	_ = json.Unmarshal(old.Attributes, &attrs)
	attrs["created-at"] = time.Now().Add(-100 * 24 * time.Hour).Format(time.RFC3339)
	old.Attributes = raw(attrs)
	s, api, _ := fixtureSource(t, old, runFixture("run-new", "planning", "user-me"))
	result := s.Collect(context.Background(), integration.CollectRequest{})
	if !result.Complete || len(result.Observations) != 1 || result.Observations[0].Ref.Metadata["run_id"] != "run-new" {
		t.Fatal("old terminal history surfaced", result)
	}
	requests := 0
	api.pages = func(u *url.URL) (string, error) {
		requests++
		page, _ := strconv.Atoi(u.Query().Get("page[number]"))
		r := runFixture(fmt.Sprintf("run-page%d", page), "planning", "user-other")
		next := fmt.Sprintf("https://app.terraform.io/api/v2/workspaces/ws-one/runs?page%%5Bnumber%%5D=%d", page+1)
		return encode(map[string]any{"data": []resource{r}, "links": map[string]any{"next": next}}), nil
	}
	result = s.Collect(context.Background(), integration.CollectRequest{Previous: tasksFrom(result)})
	if result.Complete || requests != listPageBudget || len(result.Observations) != 1 || result.Observations[0].Signal == integration.SignalDone {
		t.Fatal("budget implied completion", result, requests)
	}
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
