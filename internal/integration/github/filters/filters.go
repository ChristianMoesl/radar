package filters

import (
	"radar/internal/protocol"
	"strings"
)

type PullRequestRule struct {
	Name    string                      `yaml:"name,omitempty"`
	Repos   []string                    `yaml:"repos,omitempty"`
	Authors []string                    `yaml:"authors,omitempty"`
	Action  protocol.ContributionAction `yaml:"action"`
}

type ActivityRule struct {
	Name   string   `yaml:"name,omitempty"`
	Repos  []string `yaml:"repos,omitempty"`
	Actors []string `yaml:"actors,omitempty"`
	Action string   `yaml:"action"`
}

func Apply(items []protocol.Task, rules []PullRequestRule) []protocol.Task {
	filtered := make([]protocol.Task, 0, len(items))
	for _, item := range items {
		projected, visible := protocol.ProjectAttention(item, func(ref protocol.SourceRef) protocol.ContributionAction {
			return ActionFor(ref, rules)
		})
		if visible {
			filtered = append(filtered, projected)
		}
	}
	return filtered
}

func ActionFor(ref protocol.SourceRef, rules []PullRequestRule) protocol.ContributionAction {
	if ref.Source != "github" || ref.Kind != "pull_request" {
		return protocol.ContributionKeep
	}
	authors := ActorAliases(ref.Metadata["author"], ref.Metadata["author_type"])
	for _, rule := range rules {
		if matches(ref.Repo, authors, rule.Repos, rule.Authors) {
			return rule.Action
		}
	}
	return protocol.ContributionKeep
}

// SuppressesActivity only filters otherwise relevant comments/reviews. Keeping
// an actor neither subscribes to unrelated discussions nor overrides PR policy.
func SuppressesActivity(rules []ActivityRule, repo string, actors []string) bool {
	for _, rule := range rules {
		if matches(repo, actors, rule.Repos, rule.Actors) {
			return rule.Action == "ignore"
		}
	}
	return false
}

func matches(repo string, users, repos, patterns []string) bool {
	return (len(repos) > 0 || len(patterns) > 0) &&
		(len(repos) == 0 || matchesAny([]string{repo}, repos)) &&
		(len(patterns) == 0 || matchesAny(users, patterns))
}

func matchesAny(values, patterns []string) bool {
	for _, value := range values {
		for _, pattern := range patterns {
			if MatchPattern(pattern, value) {
				return true
			}
		}
	}
	return false
}

// Only confirmed bot identities get equivalent login and login[bot] aliases.
func ActorAliases(login, actorType string) []string {
	login = strings.TrimSpace(login)
	if login == "" {
		return nil
	}
	botSuffix := strings.HasSuffix(strings.ToLower(login), "[bot]")
	if !botSuffix && !strings.EqualFold(actorType, "Bot") {
		return []string{login}
	}
	base := login
	if botSuffix {
		base = strings.TrimSpace(login[:len(login)-len("[bot]")])
	}
	if botSuffix {
		return []string{login, base}
	}
	return []string{login, base + "[bot]"}
}

func MatchPattern(pattern string, value string) bool {
	return wildcardMatch(pattern, value)
}

func wildcardMatch(pattern string, value string) bool {
	pattern = normalize(pattern)
	value = normalize(value)
	if pattern == "" || value == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}

	parts := strings.Split(pattern, "*")
	pos := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		idx := strings.Index(value[pos:], part)
		if idx < 0 {
			return false
		}
		if i == 0 && idx != 0 {
			return false
		}
		pos += idx + len(part)
	}

	last := parts[len(parts)-1]
	return last == "" || strings.HasSuffix(value, last)
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
