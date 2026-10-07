package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"radar/internal/configfile"
	datadogsettings "radar/internal/integration/datadog/settings"
	githubsettings "radar/internal/integration/github/settings"
	jirasettings "radar/internal/integration/jira/settings"
	obsidiansettings "radar/internal/integration/obsidian/settings"
	sbxsettings "radar/internal/integration/sbx/settings"
	sessionlayout "radar/internal/integration/tmux/layout"
	"radar/internal/pi"
)

type Config struct {
	RepositoryDirs      []string             `yaml:"repository_dirs,omitempty"`
	Workspace           WorkspaceConfig      `yaml:"workspace"`
	Model               string               `yaml:"model,omitempty"`
	Thinking            string               `yaml:"thinking,omitempty"`
	LinkingMarkPrefixes []string             `yaml:"linking_mark_prefixes"`
	SBX                 SBXConfig            `yaml:"sbx"`
	Tmux                sessionlayout.Config `yaml:"tmux"`
	GitHub              GitHubConfig         `yaml:"github"`
	Jira                JiraConfig           `yaml:"jira"`
	Datadog             DatadogConfig        `yaml:"datadog"`
	Obsidian            ObsidianConfig       `yaml:"obsidian"`
}

type WorkspaceConfig struct {
	RootDir     string                 `yaml:"root_dir"`
	AutoConfirm bool                   `yaml:"auto_confirm"`
	Cleanup     WorkspaceCleanupConfig `yaml:"cleanup"`
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
	return filepath.Join(base, "radar", "config.yaml"), nil
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
	if err := configfile.Decode(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
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
	if err := Create(Default()); err != nil {
		return "", err
	}
	return path, nil
}

func Default() Config {
	cfg := Config{
		RepositoryDirs: []string{"~/workspace", "~/code", "~/src", "~/dev", "~/projects"},
		Workspace: WorkspaceConfig{
			RootDir:     defaultWorkspaceRoot(),
			AutoConfirm: true,
			Cleanup:     WorkspaceCleanupConfig{DisposableEntries: []string{}},
		},
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
	if err := githubsettings.Validate(cfg.GitHub); err != nil {
		return err
	}
	if err := sbxsettings.ValidateReadyCommand(cfg.SBX.ReadyCommand); err != nil {
		return err
	}
	if err := cfg.Workspace.Cleanup.validate(); err != nil {
		return err
	}
	if err := pi.ValidateThinking(cfg.Thinking); err != nil {
		return err
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
