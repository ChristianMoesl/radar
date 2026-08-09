package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"radar/internal/filters"
	"radar/internal/pi"
	"radar/internal/tmuxlayout"
)

type Config struct {
	RepositoryDirs      []string          `json:"repository_dirs,omitempty"`
	WorkspaceRoot       string            `json:"workspace_root,omitempty"`
	Model               string            `json:"model,omitempty"`
	Thinking            string            `json:"thinking,omitempty"`
	LinkingMarkPrefixes []string          `json:"linking_mark_prefixes"`
	SBX                 SBXConfig         `json:"sbx"`
	Tmux                tmuxlayout.Config `json:"tmux"`
	GitHub              GitHubConfig      `json:"github"`
	Jira                JiraConfig        `json:"jira"`
	Datadog             DatadogConfig     `json:"datadog"`
	Obsidian            ObsidianConfig    `json:"obsidian"`
}

type SBXConfig struct {
	Enabled          bool         `json:"enabled"`
	Kit              SBXKitConfig `json:"kit"`
	AdditionalMounts []string     `json:"additional_mounts"`
}

type SBXKitConfig struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

type GitHubConfig struct {
	Filters filters.Config `json:"filters"`
}

type JiraConfig struct {
	AuthoritativeIssueTypes []string          `json:"authoritative_issue_types"`
	StatusMapping           map[string]string `json:"status_mapping,omitempty"`
	UnmappedStatus          string            `json:"unmapped_status,omitempty"`
	unmappedStatusSet       bool
}

func (c *JiraConfig) UnmarshalJSON(data []byte) error {
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

func (c JiraConfig) SignalForStatus(status string) string {
	status = strings.TrimSpace(status)
	for name, signal := range c.StatusMapping {
		if strings.EqualFold(strings.TrimSpace(name), status) {
			return signal
		}
	}
	return c.UnmappedStatus
}

func (c JiraConfig) IsAuthoritativeIssueType(issueType string) bool {
	issueType = strings.TrimSpace(issueType)
	for _, configured := range c.AuthoritativeIssueTypes {
		if strings.EqualFold(configured, issueType) {
			return true
		}
	}
	return false
}

type DatadogConfig struct {
	MonitorQuery    string   `json:"monitor_query"`
	MonitorStatuses []string `json:"monitor_statuses"`
}

type ObsidianConfig struct {
	VaultPath string `json:"vault_path"`
}

func (c ObsidianConfig) ValidateAndPrepare() (string, error) {
	path := strings.TrimSpace(c.VaultPath)
	if path == "" {
		return "", fmt.Errorf("obsidian.vault_path is required")
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand obsidian.vault_path: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("obsidian.vault_path must be absolute or start with ~/")
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("obsidian vault %s: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("obsidian vault is not a directory: %s", path)
	}
	if info, err := os.Stat(filepath.Join(path, ".obsidian")); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return "", fmt.Errorf("obsidian vault %s does not contain .obsidian/: %w", path, err)
	}
	taskRoot := ObsidianTaskRoot(path)
	if err := os.MkdirAll(taskRoot, 0o755); err != nil {
		return "", fmt.Errorf("create Obsidian task root %s: %w", taskRoot, err)
	}
	return path, nil
}

func ObsidianTaskRoot(vaultPath string) string {
	return filepath.Join(vaultPath, "Tasks")
}

func Path() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "radar", "config.json"), nil
}

func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, validate(cfg)
		}
		return Config{}, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	applyDefaults(&cfg)
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func EnsureFile() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(Default(), "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func Default() Config {
	cfg := Config{
		RepositoryDirs:      []string{"~/workspace", "~/code", "~/src", "~/dev", "~/projects"},
		WorkspaceRoot:       defaultWorkspaceRoot(),
		LinkingMarkPrefixes: []string{},
		SBX: SBXConfig{
			Kit:              SBXKitConfig{Name: "shell"},
			AdditionalMounts: []string{},
		},
		Tmux: tmuxlayout.Default(),
		Jira: JiraConfig{
			AuthoritativeIssueTypes: []string{"Task", "Bug", "Sub-task"},
			StatusMapping: map[string]string{
				"In Progress": "in_progress",
				"In Review":   "in_progress",
			},
			UnmappedStatus: "low_priority",
		},
		Datadog: DatadogConfig{
			MonitorStatuses: []string{"Alert", "Warn", "No Data"},
		},
		GitHub: GitHubConfig{
			Filters: filters.Config{
				MuteRepos:         []string{},
				DeprioritizeRepos: []string{},
				MuteUsers:         []string{},
				DeprioritizeUsers: []string{},
				Rules: []filters.Rule{
					{
						Name:   "Track bot PRs in selected repos",
						Repos:  []string{"example-org/*"},
						Users:  []string{"dependabot[bot]", "renovate[bot]"},
						Action: "deprioritize",
					},
				},
			},
		},
	}
	return cfg
}

func defaultWorkspaceRoot() string {
	if base := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(base) {
		return filepath.Join(base, "radar", "workspaces")
	}
	return "~/.local/share/radar/workspaces"
}

func applyDefaults(cfg *Config) {
	defaults := Default()
	if len(cfg.RepositoryDirs) == 0 {
		cfg.RepositoryDirs = defaults.RepositoryDirs
	}
	if strings.TrimSpace(cfg.WorkspaceRoot) == "" {
		cfg.WorkspaceRoot = defaults.WorkspaceRoot
	}
	if strings.TrimSpace(cfg.SBX.Kit.Name) == "" {
		cfg.SBX.Kit.Name = defaults.SBX.Kit.Name
	}
	if cfg.Jira.StatusMapping == nil {
		cfg.Jira.StatusMapping = defaults.Jira.StatusMapping
	}
	if cfg.Datadog.MonitorStatuses == nil {
		cfg.Datadog.MonitorStatuses = defaults.Datadog.MonitorStatuses
	}
	for i := range cfg.LinkingMarkPrefixes {
		cfg.LinkingMarkPrefixes[i] = strings.ToUpper(strings.TrimSpace(cfg.LinkingMarkPrefixes[i]))
	}
	for i := range cfg.Jira.AuthoritativeIssueTypes {
		cfg.Jira.AuthoritativeIssueTypes[i] = strings.TrimSpace(cfg.Jira.AuthoritativeIssueTypes[i])
	}
	for i, status := range cfg.Datadog.MonitorStatuses {
		trimmed := strings.TrimSpace(status)
		if canonical, ok := canonicalDatadogMonitorStatus(trimmed); ok {
			trimmed = canonical
		}
		cfg.Datadog.MonitorStatuses[i] = trimmed
	}
	if !cfg.Jira.unmappedStatusSet && strings.TrimSpace(cfg.Jira.UnmappedStatus) == "" {
		cfg.Jira.UnmappedStatus = defaults.Jira.UnmappedStatus
	}
	cfg.Tmux = tmuxlayout.WithDefaults(cfg.Tmux)
}

func validate(cfg Config) error {
	if err := pi.ValidateThinking(cfg.Thinking); err != nil {
		return err
	}
	if len(cfg.LinkingMarkPrefixes) == 0 {
		return fmt.Errorf("linking_mark_prefixes must not be empty")
	}
	markPrefixes := map[string]string{}
	validMarkPrefix := regexp.MustCompile(`^[A-Z][A-Z0-9]*$`)
	for i, prefix := range cfg.LinkingMarkPrefixes {
		if !validMarkPrefix.MatchString(prefix) {
			return fmt.Errorf("linking_mark_prefixes[%d] must start with a letter and contain only letters and numbers", i)
		}
		if previous, exists := markPrefixes[prefix]; exists {
			return fmt.Errorf("linking_mark_prefixes values %q and %q match case-insensitively", previous, prefix)
		}
		markPrefixes[prefix] = prefix
	}
	issueTypes := map[string]string{}
	for i, issueType := range cfg.Jira.AuthoritativeIssueTypes {
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
	for status, signal := range cfg.Jira.StatusMapping {
		trimmed := strings.TrimSpace(status)
		if trimmed == "" {
			return fmt.Errorf("jira.status_mapping status names must not be empty")
		}
		normalized := strings.ToLower(trimmed)
		if previous, exists := statusNames[normalized]; exists {
			return fmt.Errorf("jira.status_mapping status names %q and %q match case-insensitively", previous, status)
		}
		statusNames[normalized] = status
		if !validJiraSignal(signal) {
			return fmt.Errorf("jira.status_mapping[%q] has unsupported value %q", status, signal)
		}
	}
	if !validJiraSignal(cfg.Jira.UnmappedStatus) {
		return fmt.Errorf("jira.unmapped_status has unsupported value %q", cfg.Jira.UnmappedStatus)
	}
	if len(cfg.Datadog.MonitorStatuses) == 0 {
		return fmt.Errorf("datadog.monitor_statuses must not be empty")
	}
	datadogStatuses := map[string]string{}
	for i, status := range cfg.Datadog.MonitorStatuses {
		canonical, ok := canonicalDatadogMonitorStatus(status)
		if !ok {
			return fmt.Errorf("datadog.monitor_statuses[%d] has unsupported value %q", i, status)
		}
		normalized := strings.ToLower(canonical)
		if previous, exists := datadogStatuses[normalized]; exists {
			return fmt.Errorf("datadog.monitor_statuses values %q and %q match case-insensitively", previous, status)
		}
		datadogStatuses[normalized] = status
	}
	return tmuxlayout.Validate(cfg.Tmux)
}

func canonicalDatadogMonitorStatus(status string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "alert":
		return "Alert", true
	case "warn":
		return "Warn", true
	case "no data":
		return "No Data", true
	default:
		return "", false
	}
}

func validJiraSignal(signal string) bool {
	switch signal {
	case "low_priority", "in_progress", "attention", "immediate":
		return true
	default:
		return false
	}
}
