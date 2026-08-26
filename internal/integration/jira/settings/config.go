package settings

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Config struct {
	AuthoritativeIssueTypes []string          `json:"authoritative_issue_types"`
	StatusMapping           map[string]string `json:"status_mapping,omitempty"`
	UnmappedStatus          string            `json:"unmapped_status,omitempty"`
	unmappedStatusSet       bool
}

func (c *Config) UnmarshalJSON(data []byte) error {
	var raw struct {
		AuthoritativeIssueTypes []string        `json:"authoritative_issue_types"`
		StatusMapping           json.RawMessage `json:"status_mapping"`
		UnmappedStatus          *string         `json:"unmapped_status"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.AuthoritativeIssueTypes != nil {
		c.AuthoritativeIssueTypes = raw.AuthoritativeIssueTypes
	}
	if raw.StatusMapping != nil {
		var mapping map[string]string
		if err := json.Unmarshal(raw.StatusMapping, &mapping); err != nil {
			return err
		}
		c.StatusMapping = mapping
	}
	if raw.UnmappedStatus != nil {
		c.UnmappedStatus = *raw.UnmappedStatus
		c.unmappedStatusSet = true
	}
	return nil
}

func (c Config) SignalForStatus(status string) string {
	status = strings.TrimSpace(status)
	for name, signal := range c.StatusMapping {
		if strings.EqualFold(strings.TrimSpace(name), status) {
			return signal
		}
	}
	return c.UnmappedStatus
}

func (c Config) IsAuthoritativeIssueType(issueType string) bool {
	issueType = strings.TrimSpace(issueType)
	for _, configured := range c.AuthoritativeIssueTypes {
		if strings.EqualFold(configured, issueType) {
			return true
		}
	}
	return false
}

func Default() Config {
	return Config{
		AuthoritativeIssueTypes: []string{"Task", "Bug", "Sub-task"},
		StatusMapping: map[string]string{
			"In Progress": "in_progress",
			"In Review":   "in_progress",
		},
		UnmappedStatus: "low_priority",
	}
}

func ApplyDefaults(config *Config) {
	defaults := Default()
	if config.StatusMapping == nil {
		config.StatusMapping = defaults.StatusMapping
	}
	for i := range config.AuthoritativeIssueTypes {
		config.AuthoritativeIssueTypes[i] = strings.TrimSpace(config.AuthoritativeIssueTypes[i])
	}
	if !config.unmappedStatusSet && strings.TrimSpace(config.UnmappedStatus) == "" {
		config.UnmappedStatus = defaults.UnmappedStatus
	}
}

func Validate(config Config) error {
	issueTypes := map[string]string{}
	for i, issueType := range config.AuthoritativeIssueTypes {
		if issueType == "" {
			return fmt.Errorf("jira.authoritative_issue_types[%d] must not be empty", i)
		}
		normalized := strings.ToLower(issueType)
		if previous, exists := issueTypes[normalized]; exists {
			return fmt.Errorf("jira.authoritative_issue_types values %q and %q match case-insensitively", previous, issueType)
		}
		issueTypes[normalized] = issueType
	}
	statusNames := map[string]string{}
	for status, signal := range config.StatusMapping {
		trimmed := strings.TrimSpace(status)
		if trimmed == "" {
			return fmt.Errorf("jira.status_mapping status names must not be empty")
		}
		normalized := strings.ToLower(trimmed)
		if previous, exists := statusNames[normalized]; exists {
			return fmt.Errorf("jira.status_mapping status names %q and %q match case-insensitively", previous, status)
		}
		statusNames[normalized] = status
		if !validSignal(signal) {
			return fmt.Errorf("jira.status_mapping[%q] has unsupported value %q", status, signal)
		}
	}
	if !validSignal(config.UnmappedStatus) {
		return fmt.Errorf("jira.unmapped_status has unsupported value %q", config.UnmappedStatus)
	}
	return nil
}

func validSignal(signal string) bool {
	switch signal {
	case "low_priority", "in_progress", "attention", "immediate":
		return true
	default:
		return false
	}
}
