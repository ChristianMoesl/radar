package obsidian

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

var legacyFields = []string{"radar-ignored", "radar-id", "radar-title", "radar-state", "radar-priority", "radar-created-at", "radar-completed-at", "radar-completion-baseline", "radar-muted", "radar-agent-review", "radar-source-refs"}

func hasLegacyFields(content string) bool {
	lines := strings.Split(content, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return false
	}
	end := len(lines)
	for i, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			end = i + 1
			break
		}
	}
	var document yaml.Node
	if yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &document) == nil && len(document.Content) == 1 && document.Content[0].Kind == yaml.MappingNode {
		root := document.Content[0]
		for i := 0; i < len(root.Content); i += 2 {
			for _, key := range legacyFields {
				if root.Content[i].Value == key {
					return true
				}
			}
		}
	}
	for _, line := range lines[1:end] {
		for _, field := range legacyFields {
			if strings.HasPrefix(line, field+":") {
				return true
			}
		}
	}
	return false
}

// Updates describe application fields only; they never edit a Markdown file.
func applyStateUpdates(current note, updates map[string]string) (note, error) {
	updated := current
	for field, value := range updates {
		switch field {
		case "state":
			updated.State = value
		case "priority":
			updated.Priority = value
		case "completed_at":
			updated.CompletedAt = value
		case "completion_baseline":
			updated.CompletionBaseline = value
		case "muted":
			if value != "true" && value != "false" {
				return current, fmt.Errorf("muted must be boolean")
			}
			if err := yaml.Unmarshal([]byte(value), &updated.Muted); err != nil {
				return current, err
			}
		case "agent_review":
			if value != "true" && value != "false" {
				return current, fmt.Errorf("review must be boolean")
			}
			if err := yaml.Unmarshal([]byte(value), &updated.AgentReview); err != nil {
				return current, err
			}
		case "bindings":
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(value), &node); err != nil {
				return current, err
			}
			bindings, err := parseBindings(node.Content[0])
			if err != nil {
				return current, err
			}
			updated.Bindings = bindings
			updated.Adopted = true
		default:
			return current, fmt.Errorf("unknown task state field %s", field)
		}
	}
	if updated.State != "open" && updated.State != "done" {
		return current, fmt.Errorf("invalid task lifecycle")
	}
	if updated.Priority != "normal" && updated.Priority != "urgent" {
		return current, fmt.Errorf("invalid task priority")
	}
	if updated.AgentReview && updated.State == "done" {
		return current, fmt.Errorf("review hold must be cleared before completing task")
	}
	if err := validTimestamp("completed_at", updated.CompletedAt, updated.State == "open"); err != nil {
		return current, err
	}
	if updated.State == "open" && updated.CompletedAt != "" {
		return current, fmt.Errorf("open task has completion timestamp")
	}
	if updated.CompletionBaseline != "" && updated.CompletionBaseline != "pending" && !validCompletionBaseline.MatchString(updated.CompletionBaseline) {
		return current, fmt.Errorf("invalid completion baseline")
	}
	return updated, nil
}
