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
