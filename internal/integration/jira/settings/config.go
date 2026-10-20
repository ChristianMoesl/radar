package settings

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	BaseURL                 string            `yaml:"base_url,omitempty"`
	CloudID                 string            `yaml:"cloud_id,omitempty"`
	Profile                 string            `yaml:"profile"`
	AccountID               string            `yaml:"account_id,omitempty"`
	Enabled                 *bool             `yaml:"enabled,omitempty"`
	AuthoritativeIssueTypes []string          `yaml:"authoritative_issue_types"`
	StatusMapping           map[string]string `yaml:"status_mapping"`
	UnmappedStatus          string            `yaml:"unmapped_status,omitempty"`
	unmappedStatusSet       bool
}

// Decode into fresh maps so explicit mappings replace defaults, including {}.
func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	type plain Config
	next := plain(*c)
	next.StatusMapping = nil
	if err := node.Decode(&next); err != nil {
		return err
	}
	if next.AuthoritativeIssueTypes == nil {
		next.AuthoritativeIssueTypes = c.AuthoritativeIssueTypes
	}
	var fields map[string]yaml.Node
	if err := node.Decode(&fields); err != nil {
		return err
	}
	for _, key := range []string{"email", "api_token", "access_token", "api_base_url"} {
		if _, exists := fields[key]; exists {
			return fmt.Errorf("jira.%s is no longer supported; remove it manually and run radar setup for TWG OAuth", key)
		}
	}
	if field, ok := fields["unmapped_status"]; ok && field.Tag != "!!null" {
		next.unmappedStatusSet = true
	}
	*c = Config(next)
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
		Profile:                 "radar",
		AuthoritativeIssueTypes: []string{"Story", "Task", "Bug", "Sub-task"},
		StatusMapping: map[string]string{
			"In Progress": "in_progress",
			"In Review":   "in_progress",
		},
		UnmappedStatus: "low_priority",
	}
}

func ApplyDefaults(config *Config) {
	if config.Profile == "" {
		config.Profile = "radar"
	}
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
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`).MatchString(config.Profile) || strings.EqualFold(config.Profile, "default") {
		return fmt.Errorf("jira.profile must select a named TWG OAuth profile")
	}
	if config.CloudID != "" && !regexp.MustCompile(`^[A-Za-z0-9-]+$`).MatchString(config.CloudID) {
		return fmt.Errorf("jira.cloud_id must be a Cloud ID, not a hostname or URL")
	}
	if config.BaseURL != "" {
		u, err := url.Parse(config.BaseURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("jira.base_url must be an HTTPS site URL without credentials, path, query or fragment")
		}
	}

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
