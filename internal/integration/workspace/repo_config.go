package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/configfile"
	sbxsettings "radar/internal/integration/sbx/settings"
	"radar/internal/pi"
)

type RepoConfig struct {
	CopyFiles []string       `yaml:"copy_files,omitempty"`
	Setup     []string       `yaml:"setup,omitempty"`
	Model     string         `yaml:"model,omitempty"`
	Thinking  string         `yaml:"thinking,omitempty"`
	SBX       *SandboxConfig `yaml:"sbx,omitempty"`
}

type SandboxConfig struct {
	Enabled          *bool             `yaml:"enabled,omitempty"`
	ReadyCommand     *[]string         `yaml:"ready_command,omitempty"`
	EnvFile          *string           `yaml:"env_file,omitempty"`
	Kit              *SandboxKitConfig `yaml:"kit,omitempty"`
	AdditionalMounts []string          `yaml:"additional_mounts,omitempty"`
}

type SandboxKitConfig struct {
	Name string `yaml:"name"`
	Path string `yaml:"path,omitempty"`
}

func loadRepoConfig(repo string) (RepoConfig, error) {
	path := filepath.Join(repo, ".radar.yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return RepoConfig{}, nil
	}
	if err != nil {
		return RepoConfig{}, err
	}
	var cfg RepoConfig
	if err := configfile.Decode(data, &cfg); err != nil {
		return RepoConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	if err := validateRepoConfig(cfg); err != nil {
		return RepoConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	return cfg, nil
}

func validateRepoConfig(cfg RepoConfig) error {
	if cfg.SBX != nil && cfg.SBX.ReadyCommand != nil {
		if err := sbxsettings.ValidateReadyCommand(*cfg.SBX.ReadyCommand); err != nil {
			return err
		}
	}
	for _, path := range cfg.CopyFiles {
		if err := validateRelativeFilePath(path); err != nil {
			return fmt.Errorf("copy_files contains invalid path %q: %w", path, err)
		}
	}
	for _, command := range cfg.Setup {
		if strings.TrimSpace(command) == "" {
			return fmt.Errorf("setup contains an empty command")
		}
	}
	if cfg.Model != "" && strings.TrimSpace(cfg.Model) == "" {
		return fmt.Errorf("model is empty")
	}
	if err := pi.ValidateThinking(cfg.Thinking); err != nil {
		return err
	}
	if cfg.SBX != nil && cfg.SBX.Kit != nil && strings.TrimSpace(cfg.SBX.Kit.Name) == "" {
		return fmt.Errorf("sbx.kit.name is required")
	}
	if cfg.SBX != nil && cfg.SBX.EnvFile != nil {
		if _, err := resolveSandboxEnvFile(*cfg.SBX.EnvFile); err != nil {
			return err
		}
	}
	return nil
}

func validateRelativeFilePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path is empty")
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("absolute paths are not allowed")
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("paths must stay inside the repository")
	}
	return nil
}
