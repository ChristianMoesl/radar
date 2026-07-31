package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/filters"
	"radar/internal/pi"
	"radar/internal/tmuxlayout"
)

type Config struct {
	RepositoryDirs []string          `json:"repository_dirs,omitempty"`
	WorkspaceRoot  string            `json:"workspace_root,omitempty"`
	Model          string            `json:"model,omitempty"`
	Thinking       string            `json:"thinking,omitempty"`
	SBX            SBXConfig         `json:"sbx"`
	Tmux           tmuxlayout.Config `json:"tmux"`
	GitHub         GitHubConfig      `json:"github"`
	Jira           JiraConfig        `json:"jira"`
	Datadog        DatadogConfig     `json:"datadog"`
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
	IssueTypes     []string          `json:"issue_types"`
	StatusMapping  map[string]string `json:"status_mapping"`
	UnmappedStatus string            `json:"unmapped_status"`
}

func (c *JiraConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		IssueTypes     []string        `json:"issue_types"`
		StatusMapping  json.RawMessage `json:"status_mapping"`
		UnmappedStatus string          `json:"unmapped_status"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.IssueTypes != nil {
		c.IssueTypes = raw.IssueTypes
	}
	if raw.StatusMapping != nil {
		var mapping map[string]string
		if err := json.Unmarshal(raw.StatusMapping, &mapping); err != nil {
			return err
		}
		c.StatusMapping = mapping
	}
	if raw.UnmappedStatus != "" {
		c.UnmappedStatus = raw.UnmappedStatus
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

type DatadogConfig struct {
	MonitorQuery string `json:"monitor_query"`
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
			return cfg, nil
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
		RepositoryDirs: []string{"~/workspace", "~/code", "~/src", "~/dev", "~/projects"},
		WorkspaceRoot:  defaultWorkspaceRoot(),
		SBX: SBXConfig{
			Kit:              SBXKitConfig{Name: "shell"},
			AdditionalMounts: []string{},
		},
		Tmux: tmuxlayout.Default(),
		Jira: JiraConfig{
			IssueTypes: []string{},
			StatusMapping: map[string]string{
				"In Progress": "in_progress",
				"In Review":   "in_progress",
			},
			UnmappedStatus: "low_priority",
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
	if strings.TrimSpace(cfg.Jira.UnmappedStatus) == "" {
		cfg.Jira.UnmappedStatus = defaults.Jira.UnmappedStatus
	}
	cfg.Tmux = tmuxlayout.WithDefaults(cfg.Tmux)
}

func validate(cfg Config) error {
	if err := pi.ValidateThinking(cfg.Thinking); err != nil {
		return err
	}
	for i, issueType := range cfg.Jira.IssueTypes {
		if strings.TrimSpace(issueType) == "" {
			return fmt.Errorf("jira.issue_types[%d] must not be empty", i)
		}
	}
	for status, signal := range cfg.Jira.StatusMapping {
		if strings.TrimSpace(status) == "" {
			return fmt.Errorf("jira.status_mapping status names must not be empty")
		}
		if !validJiraSignal(signal) {
			return fmt.Errorf("jira.status_mapping[%q] has unsupported value %q", status, signal)
		}
	}
	if !validJiraSignal(cfg.Jira.UnmappedStatus) {
		return fmt.Errorf("jira.unmapped_status has unsupported value %q", cfg.Jira.UnmappedStatus)
	}
	return tmuxlayout.Validate(cfg.Tmux)
}

func validJiraSignal(signal string) bool {
	switch signal {
	case "low_priority", "in_progress", "attention", "immediate":
		return true
	default:
		return false
	}
}
