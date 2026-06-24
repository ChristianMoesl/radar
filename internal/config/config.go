package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/filters"
	"radar/internal/pi"
)

type Config struct {
	RepositoryDirs  []string       `json:"repository_dirs,omitempty"`
	WorkspaceRoot   string         `json:"workspace_root,omitempty"`
	Model           string         `json:"model,omitempty"`
	Thinking        string         `json:"thinking,omitempty"`
	SandboxTemplate string         `json:"sandbox_template,omitempty"`
	Filters         filters.Config `json:"filters,omitempty"`
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
		RepositoryDirs:  []string{"~/workspace", "~/code", "~/src", "~/dev", "~/projects"},
		WorkspaceRoot:   "~/workspaces",
		SandboxTemplate: "christianmoesl/radar-sandbox:latest",
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
	}
	return cfg
}

func applyDefaults(cfg *Config) {
	defaults := Default()
	if len(cfg.RepositoryDirs) == 0 {
		cfg.RepositoryDirs = defaults.RepositoryDirs
	}
	if strings.TrimSpace(cfg.WorkspaceRoot) == "" {
		cfg.WorkspaceRoot = defaults.WorkspaceRoot
	}
	if strings.TrimSpace(cfg.SandboxTemplate) == "" {
		cfg.SandboxTemplate = defaults.SandboxTemplate
	}
}

func validate(cfg Config) error {
	return pi.ValidateThinking(cfg.Thinking)
}
