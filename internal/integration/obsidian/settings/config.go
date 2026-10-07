package settings

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	VaultPath string `yaml:"vault_path"`
}

func (c Config) ValidateAndPrepare() (string, error) {
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

	taskRoot := TaskRoot(path)
	if err := os.MkdirAll(taskRoot, 0o755); err != nil {
		return "", fmt.Errorf("create Obsidian task root %s: %w", taskRoot, err)
	}
	return path, nil
}

func TaskRoot(vaultPath string) string {
	return filepath.Join(vaultPath, "Tasks")
}
