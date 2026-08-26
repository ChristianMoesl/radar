package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	datadogsettings "radar/internal/integration/datadog/settings"
	githubsettings "radar/internal/integration/github/settings"
	jirasettings "radar/internal/integration/jira/settings"
	obsidiansettings "radar/internal/integration/obsidian/settings"
	sbxsettings "radar/internal/integration/sbx/settings"
	sessionlayout "radar/internal/integration/tmux/layout"
	"radar/internal/pi"
)

type Config struct {
	RepositoryDirs      []string             `json:"repository_dirs,omitempty"`
	Workspace           WorkspaceConfig      `json:"workspace"`
	Model               string               `json:"model,omitempty"`
	Thinking            string               `json:"thinking,omitempty"`
	LinkingMarkPrefixes []string             `json:"linking_mark_prefixes"`
	SBX                 SBXConfig            `json:"sbx"`
	Tmux                sessionlayout.Config `json:"tmux"`
	GitHub              GitHubConfig         `json:"github"`
	Jira                JiraConfig           `json:"jira"`
	Datadog             DatadogConfig        `json:"datadog"`
	Obsidian            ObsidianConfig       `json:"obsidian"`
}

type WorkspaceConfig struct {
	RootDir     string `json:"root_dir"`
	AutoConfirm bool   `json:"auto_confirm"`
}

type SBXConfig = sbxsettings.Config
type SBXKitConfig = sbxsettings.KitConfig
type GitHubConfig = githubsettings.Config
type JiraConfig = jirasettings.Config
type DatadogConfig = datadogsettings.Config
type ObsidianConfig = obsidiansettings.Config

func ObsidianTaskRoot(vaultPath string) string {
	return obsidiansettings.TaskRoot(vaultPath)
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
		Workspace:           WorkspaceConfig{RootDir: defaultWorkspaceRoot()},
		LinkingMarkPrefixes: []string{},
		SBX:                 sbxsettings.Default(),
		Tmux:                sessionlayout.Default(),
		Jira:                jirasettings.Default(),
		Datadog:             datadogsettings.Default(),
		GitHub:              githubsettings.Default(),
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
	if strings.TrimSpace(cfg.Workspace.RootDir) == "" {
		cfg.Workspace.RootDir = defaults.Workspace.RootDir
	}
	sbxsettings.ApplyDefaults(&cfg.SBX)
	jirasettings.ApplyDefaults(&cfg.Jira)
	datadogsettings.ApplyDefaults(&cfg.Datadog)
	for i := range cfg.LinkingMarkPrefixes {
		cfg.LinkingMarkPrefixes[i] = strings.ToUpper(strings.TrimSpace(cfg.LinkingMarkPrefixes[i]))
	}
	cfg.Tmux = sessionlayout.WithDefaults(cfg.Tmux)
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
	if err := jirasettings.Validate(cfg.Jira); err != nil {
		return err
	}
	if err := datadogsettings.Validate(cfg.Datadog); err != nil {
		return err
	}
	return sessionlayout.Validate(cfg.Tmux)
}
