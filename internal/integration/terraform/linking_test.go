package terraform

import (
	"context"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/github"
	"radar/internal/linking"
	"radar/internal/protocol"
	"radar/internal/setup"
)

func TestVerifiedLinkingAndSharedBranchIsolation(t *testing.T) {
	s := NewSource(github.NewSource())
	marks := linking.NewMarkMatcher([]string{"ABC"})
	git := ingressAttributes{Repository: "acme/app", CloneURL: "https://github.com/acme/app.git", Branch: "ABC-123-ingress", PRURL: "https://github.com/acme/app/pull/7", PRNumber: 7, IsPR: true, DefaultBranch: new(false)}
	ref := protocol.SourceRef{Metadata: map[string]string{}}
	if err := s.link(&ref, "ABC-999 incidental text", git, marks); err != nil {
		t.Fatal(err)
	}
	keys := strings.Join(ref.LinkingKeys, ",")
	if !strings.Contains(keys, "github:pr:acme/app:7") || !strings.Contains(keys, "mark:ABC-123") || strings.Contains(keys, "mark:ABC-999") {
		t.Fatal("incorrect association", keys)
	}
	if !s.CanSeedWorkspace(protocol.SourceRef{Source: "terraform", Kind: "run", Branch: ref.Branch, Metadata: ref.Metadata, Presentation: ref.Presentation}) {
		t.Fatal("verified branch cannot seed workspace")
	}
	git.DefaultBranch = new(true)
	git.Branch = "main"
	git.IsPR = false
	a := protocol.SourceRef{Metadata: map[string]string{}}
	b := protocol.SourceRef{Metadata: map[string]string{}}
	if err := s.link(&a, "deploy one", git, marks); err != nil {
		t.Fatal(err)
	}
	if err := s.link(&b, "deploy two", git, marks); err != nil {
		t.Fatal(err)
	}
	if len(a.LinkingKeys) != 0 || len(b.LinkingKeys) != 0 || a.Presentation.WorkspaceName != "" {
		t.Fatal("shared branch merged unrelated deployments")
	}
	git.Branch = "ABC-123-and-ABC-456"
	git.IsPR = true
	c := protocol.SourceRef{Metadata: map[string]string{}}
	if err := s.link(&c, "ABC-999", git, marks); err != nil || len(c.LinkingKeys) != 0 {
		t.Fatal("ambiguous branch should remain standalone")
	}
	git.Branch = "ABC-123"
	git.Repository = "acme/other"
	git.DefaultBranch = new(false)
	if err := s.link(&c, "", git, marks); err == nil {
		t.Fatal("conflicting structured PR identity accepted")
	}
}

func TestOptionalPlanCountsAndSafeOpenActions(t *testing.T) {
	r := runFixture("run-one", "planning", "user-me")
	r.Relationships["plan"] = rel("plan-one", "plans")
	s, _, cfg := fixtureSource(t, r)
	session := session{host: "app.terraform.io", userID: "user-me", spaces: map[string]string{"ws-one": "production-network"}}
	session.client = client{profile: "default", output: func(context.Context, setup.Command) (string, error) { return `{"data":null}`, nil }}
	plan := resource{ID: "plan-one", Type: "plans", Attributes: raw(map[string]any{"resource-additions": 0, "resource-changes": 2, "resource-destructions": 1})}
	observation, owned, err := s.observe(context.Background(), session, cfg, r, []resource{plan}, integration.CollectRequest{})
	if err != nil || !owned || !strings.Contains(observation.Ref.Presentation.Label, "+0 ~2 −1") {
		t.Fatal(observation, owned, err)
	}
	plan.Attributes = raw(map[string]any{"resource-additions": 0})
	observation, _, err = s.observe(context.Background(), session, cfg, r, []resource{plan}, integration.CollectRequest{})
	if err != nil || observation.Ref.Metadata["changes"] != "" || strings.Contains(observation.Ref.Presentation.Label, "~0") {
		t.Fatal("missing counts became zero")
	}
	ref := observation.Ref
	if _, ok := actionURL(ref, "terraform.open_run"); !ok {
		t.Fatal("valid run URL rejected")
	}
	ref.URL = "https://evil.example/app/acme/workspaces/production-network/runs/run-one"
	if _, ok := actionURL(ref, "terraform.open_run"); ok {
		t.Fatal("cross-service action URL accepted")
	}
	ref.Metadata["workspace_url"] = "https://app.terraform.io/app/acme/workspaces/production-network/runs/run-other"
	if _, ok := actionURL(ref, "terraform.open_workspace"); ok {
		t.Fatal("run URL accepted as workspace")
	}
}
