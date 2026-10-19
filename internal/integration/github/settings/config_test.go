package settings

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestInvalidGitHubConfigurationFailsClearly(t *testing.T) {
	for _, input := range []string{
		"filters: {}", "pull_request_rules: [{repos: [acme/app], action: low_priority}]",
		"pull_request_rules: [{repos: [acme/app], action: typo}]",
		"pull_request_rules: [{action: mute}]",
		"pull_request_rules: [{authors: [''], action: keep}]",
		"pull_request_rules: [{actors: [bot], action: mute}]",
		"activity_rules: [{authors: [bot], action: ignore}]",
		"activity_rules: [{repos: [acme/app], action: mute}]",
		"activity_rules: [{actors: [], repos: [], action: ignore}]",
		"track: [{authors: [alice]}]",
		"track: [{repos: ['*/app']}]",
		"track: [{repos: ['acme/app'], authors: ['bot*']}]",
		"track: [{repos: ['acme/app'], action: keep}]",
		"track: [{repos: ['acme/app/other']}]",
	} {
		t.Run(input, func(t *testing.T) {
			cfg := Default()
			err := yaml.Unmarshal([]byte(input), &cfg)
			if err == nil {
				err = Validate(cfg)
			}
			if err == nil || !strings.Contains(err.Error(), "github") {
				t.Fatalf("config accepted or error missing path: %v", err)
			}
		})
	}
}

func TestPoliciesSupportAliasesAndIndependentSelectors(t *testing.T) {
	var doc struct {
		Defaults map[string]any `yaml:"defaults"`
		GitHub   Config         `yaml:"github"`
	}
	input := `defaults: &defaults
  repos: ["acme/*"]
  authors: [alice, bob]
  action: keep
github:
  track:
    - repos: [acme/app]
    - repos: ["acme/tools-*"]
      authors: ["renovate[bot]"]
  pull_request_rules:
    - <<: *defaults
    - repos: [acme/other]
      action: mute
    - authors: [bot]
      action: deprioritize
  activity_rules:
    - repos: [acme/app]
      actors: ["review-bot[bot]"]
      action: ignore
    - actors: [human]
      action: keep
`
	if err := yaml.Unmarshal([]byte(input), &doc); err != nil {
		t.Fatal(err)
	}
	if err := Validate(doc.GitHub); err != nil {
		t.Fatal(err)
	}
	if len(doc.GitHub.PullRequestRules[0].Authors) != 2 || len(doc.GitHub.Track[0].Authors) != 0 {
		t.Fatalf("selectors lost: %+v", doc.GitHub)
	}
}

func TestUnknownInheritedGitHubPolicyKeyRejected(t *testing.T) {
	var doc struct {
		Defaults map[string]any `yaml:"defaults"`
		GitHub   Config         `yaml:"github"`
	}
	if err := yaml.Unmarshal([]byte("defaults: &d {users: [bot], action: mute}\ngithub: {pull_request_rules: [{<<: *d}]}"), &doc); err == nil {
		t.Fatal("inherited obsolete selector accepted")
	}
}

func TestReviewRequestConfigurationDefaultsAndValidation(t *testing.T) {
	for _, input := range []string{"{}", "review_requests: {}", "review_requests: {include_merged_pull_requests: [\"Acme/team-*\"]}"} {
		var cfg Config
		if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
			t.Fatal(err)
		}
		if err := Validate(cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.ReviewRequests.MergedPullRequestMaxAgeDays != 30 {
			t.Fatalf("default age=%d", cfg.ReviewRequests.MergedPullRequestMaxAgeDays)
		}
	}
	cfg := Default()
	if len(cfg.ReviewRequests.IncludeMergedPullRequests) != 0 {
		t.Fatal("merged tracking enabled by default")
	}
	if err := yaml.Unmarshal([]byte(`review_requests: {include_merged_pull_requests: ["Acme/team-*"], merged_pull_request_max_age_days: 90}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.ReviewRequests.IncludesMerged("acme/TEAM-app") || cfg.ReviewRequests.IncludesMerged("other/team-app") || cfg.ReviewRequests.MergedPullRequestMaxAgeDays != 90 {
		t.Fatalf("review settings=%+v", cfg.ReviewRequests)
	}
	for _, input := range []string{
		"review_requests: {include_merged_repos: [acme/app]}",
		"review_requests: {include_merged_pull_requests: true}",
		"review_requests: {include_merged_pull_requests: ['']}",
		"review_requests: {include_merged_pull_requests: ['*/app']}",
		"review_requests: {include_merged_pull_requests: ['acme/app/other']}",
		"review_requests: {include_merged_pull_requests: ['acme/app is:open']}",
		"review_requests: {merged_pull_request_max_age_days: 0}",
		"review_requests: {merged_pull_request_max_age_days: -1}",
		"review_requests: {merged_pull_request_max_age_days: 999999999}",
		"review_requests: {merged_pull_request_max_age_days: '30d'}",
	} {
		t.Run(input, func(t *testing.T) {
			cfg := Default()
			err := yaml.Unmarshal([]byte(input), &cfg)
			if err == nil {
				err = Validate(cfg)
			}
			if err == nil || !strings.Contains(err.Error(), "github") {
				t.Fatalf("invalid review configuration accepted: %v", err)
			}
		})
	}
}
