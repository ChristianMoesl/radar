package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type RepoConfig struct {
	CopyFiles []string `json:"copy_files,omitempty"`
	Setup     []string `json:"setup,omitempty"`
}

func loadRepoConfig(repo string) (RepoConfig, error) {
	path := filepath.Join(repo, ".radar.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return RepoConfig{}, nil
	}
	if err != nil {
		return RepoConfig{}, err
	}
	var cfg RepoConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return RepoConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	if err := validateRepoConfig(cfg); err != nil {
		return RepoConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	return cfg, nil
}

func validateRepoConfig(cfg RepoConfig) error {
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
