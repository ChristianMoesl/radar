package filters

import (
	"strings"

	"radar/internal/protocol"
)

type Config struct {
	MuteRepos         []string `json:"mute_repos,omitempty"`
	DeprioritizeRepos []string `json:"deprioritize_repos,omitempty"`
	MuteUsers         []string `json:"mute_users,omitempty"`
	DeprioritizeUsers []string `json:"deprioritize_users,omitempty"`
	Rules             []Rule   `json:"rules,omitempty"`
}

type Rule struct {
	Name   string   `json:"name,omitempty"`
	Repos  []string `json:"repos,omitempty"`
	Users  []string `json:"users,omitempty"`
	Action string   `json:"action"`
}

const (
	actionKeep         = "keep"
	actionMute         = "mute"
	actionDeprioritize = "deprioritize"
	actionLowPriority  = "low_priority"
)

func Apply(items []protocol.Task, cfg Config) []protocol.Task {
	filtered := make([]protocol.Task, 0, len(items))
	for _, item := range items {
		action := actionFor(item, cfg)
		if action == actionMute {
			continue
		}

		if item.Attention != "done" && !hasPrimaryImmediateSignal(item) && (action == actionDeprioritize || action == actionLowPriority) {
			item.Attention = "low_priority"
			if item.Reason != "" && !strings.HasPrefix(item.Reason, "low priority") {
				item.Reason = "low priority: " + item.Reason
			} else if item.Reason == "" {
				item.Reason = "low priority"
			}
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func hasPrimaryImmediateSignal(item protocol.Task) bool {
	for _, ref := range item.SourceRefs {
		if ref.Authority == protocol.SourceRefAuthorityPrimary && ref.Signal == "immediate" {
			return true
		}
	}
	return false
}

func Summary(items []protocol.Task) protocol.Summary {
	var summary protocol.Summary
	for _, item := range items {
		switch item.Attention {
		case "immediate":
			summary.Immediate++
		case "attention":
			summary.Attention++
		case "in_progress":
			summary.InProgress++
		case "done":
			summary.Done++
		case "low_priority":
			summary.LowPriority++
		}
	}
	return summary
}

func actionFor(item protocol.Task, cfg Config) string {
	return actionForValues(repoValues(item), userValues(item), cfg)
}

// SuppressesActivity reports whether configured filters prevent an actor's
// activity from promoting work in the given repository to attention.
func SuppressesActivity(cfg Config, repo string, actorAliases []string) bool {
	return actionForValues([]string{repo}, actorAliases, cfg) != actionKeep
}

func actionForValues(repos []string, users []string, cfg Config) string {
	if matchesRuleValues(repos, users, Rule{Repos: cfg.MuteRepos}) || matchesRuleValues(repos, users, Rule{Users: cfg.MuteUsers}) {
		return actionMute
	}

	action := actionKeep
	if matchesRuleValues(repos, users, Rule{Repos: cfg.DeprioritizeRepos}) || matchesRuleValues(repos, users, Rule{Users: cfg.DeprioritizeUsers}) {
		action = actionDeprioritize
	}

	for _, rule := range cfg.Rules {
		if !matchesRuleValues(repos, users, rule) {
			continue
		}
		if normalized := normalizeAction(rule.Action); normalized == actionMute {
			return actionMute
		} else {
			action = normalized
		}
	}
	return action
}

func normalizeAction(action string) string {
	switch normalize(action) {
	case actionMute:
		return actionMute
	case actionDeprioritize, actionLowPriority:
		return actionDeprioritize
	case actionKeep, "":
		return actionKeep
	default:
		return actionKeep
	}
}

func matchesRuleValues(repos []string, users []string, rule Rule) bool {
	if len(rule.Repos) == 0 && len(rule.Users) == 0 {
		return false
	}
	if len(rule.Repos) > 0 && !matchesAny(repos, rule.Repos) {
		return false
	}
	if len(rule.Users) > 0 && !matchesAny(users, rule.Users) {
		return false
	}
	return true
}

func repoValues(item protocol.Task) []string {
	values := []string{item.Repo}
	for _, sourceRef := range item.SourceRefs {
		values = append(values, sourceRef.Repo)
	}
	return values
}

func userValues(item protocol.Task) []string {
	values := []string{metadata(item.Metadata, "user", "author", "login")}
	for _, sourceRef := range item.SourceRefs {
		values = append(values, metadata(sourceRef.Metadata, "user", "author", "login"))
	}
	return values
}

func matchesAny(values []string, patterns []string) bool {
	for _, value := range values {
		for _, pattern := range patterns {
			if wildcardMatch(pattern, value) {
				return true
			}
		}
	}
	return false
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

func metadata(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if values[key] != "" {
			return values[key]
		}
	}
	return ""
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
