package settings

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
	"radar/internal/integration/github/filters"
	"radar/internal/protocol"
)

type Config struct {
	Enabled          *bool                     `yaml:"enabled,omitempty"`
	Track            []TrackingScope           `yaml:"track"`
	PullRequestRules []filters.PullRequestRule `yaml:"pull_request_rules"`
	ActivityRules    []filters.ActivityRule    `yaml:"activity_rules"`
}

type TrackingScope struct {
	Repos   []string `yaml:"repos"`
	Authors []string `yaml:"authors,omitempty"`
}

func Default() Config {
	return Config{Track: []TrackingScope{}, PullRequestRules: []filters.PullRequestRule{}, ActivityRules: []filters.ActivityRule{}}
}

// Reject unknown/obsolete GitHub keys instead of silently dropping policies.
// The enclosing document still preserves settings owned by other integrations.
func (cfg *Config) UnmarshalYAML(node *yaml.Node) error {
	type plain Config
	value := plain(*cfg)
	if err := node.Decode(&value); err != nil {
		return fmt.Errorf("github: %w", err)
	}
	schema := map[string][]string{
		"enabled":            nil,
		"track":              {"repos", "authors"},
		"pull_request_rules": {"name", "repos", "authors", "action"},
		"activity_rules":     {"name", "repos", "actors", "action"},
	}
	if err := checkKeys(node, "github", schema); err != nil {
		return err
	}
	*cfg = Config(value)
	return nil
}

// Decode above validates types/alias cycles first. Inspect the original nodes
// (not a re-encoded subtree) so anchors elsewhere in the document still work.
func checkKeys(node *yaml.Node, path string, schema map[string][]string) error {
	if node.Kind == yaml.AliasNode {
		return checkKeys(node.Alias, path, schema)
	}
	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if err := checkKeys(child, path, schema); err != nil {
				return err
			}
		}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Value == "<<" {
			if err := checkKeys(value, path, schema); err != nil {
				return err
			}
			continue
		}
		fields, known := schema[key.Value]
		if !known {
			return fmt.Errorf("%s.%s (line %d) is not supported; GitHub policies use track, pull_request_rules and activity_rules", path, key.Value, key.Line)
		}
		if len(fields) > 0 {
			childSchema := map[string][]string{}
			for _, field := range fields {
				childSchema[field] = nil
			}
			if err := checkKeys(value, path+"."+key.Value, childSchema); err != nil {
				return err
			}
		}
	}
	return nil
}

func Validate(cfg Config) error {
	for i, scope := range cfg.Track {
		path := fmt.Sprintf("github.track[%d]", i)
		if len(scope.Repos) == 0 {
			return fmt.Errorf("%s.repos requires at least one repository", path)
		}
		if err := patterns(path+".repos", scope.Repos); err != nil {
			return err
		}
		if err := patterns(path+".authors", scope.Authors); err != nil {
			return err
		}
		for _, repo := range scope.Repos {
			owner, name, ok := strings.Cut(strings.TrimSpace(repo), "/")
			if !ok || owner == "" || name == "" || strings.Contains(owner, "*") || strings.Contains(name, "/") {
				return fmt.Errorf("%s.repos: %q must have a concrete owner and a repository name/pattern, e.g. acme/app-*", path, repo)
			}
		}
		for _, author := range scope.Authors {
			if strings.Contains(author, "*") {
				return fmt.Errorf("%s.authors: %q must be an exact login; omit authors to track everyone", path, author)
			}
		}
	}
	for i, rule := range cfg.PullRequestRules {
		path := fmt.Sprintf("github.pull_request_rules[%d]", i)
		if err := selectors(path, rule.Repos, rule.Authors); err != nil {
			return err
		}
		switch rule.Action {
		case protocol.ContributionKeep, protocol.ContributionMute, protocol.ContributionDeprioritize:
		default:
			return fmt.Errorf("%s.action must be keep, mute or deprioritize", path)
		}
	}
	for i, rule := range cfg.ActivityRules {
		path := fmt.Sprintf("github.activity_rules[%d]", i)
		if err := selectors(path, rule.Repos, rule.Actors); err != nil {
			return err
		}
		if rule.Action != "keep" && rule.Action != "ignore" {
			return fmt.Errorf("%s.action must be keep or ignore", path)
		}
	}
	return nil
}

func selectors(path string, repos, users []string) error {
	if len(repos) == 0 && len(users) == 0 {
		return fmt.Errorf("%s requires at least one nonempty selector", path)
	}
	if err := patterns(path, repos); err != nil {
		return err
	}
	return patterns(path, users)
}

func patterns(path string, values []string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s contains an empty pattern", path)
		}
	}
	return nil
}
