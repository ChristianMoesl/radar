package app

import (
	"os"
	"path/filepath"
	"radar/internal/config"
	"radar/internal/integration/obsidian"
	"strings"
)

func RequireMigratedNotes() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	vault := cfg.Obsidian.VaultPath
	if strings.HasPrefix(vault, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		vault = filepath.Join(home, vault[2:])
	}
	return obsidian.RequireMigratedNotes(vault)
}

func RecoverTaskMoves() error { return obsidian.RecoverTaskMoves() }
