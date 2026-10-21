package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/config"
	"radar/internal/integration/obsidian"
	"radar/internal/integration/obsidian/statemigration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/taskdb"
)

// PrepareTaskState runs before ordinary readers, under the daemon's exclusive
// state lock. Legacy readers are confined to this one-time startup cutover.
func PrepareTaskState(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	vault := strings.TrimSpace(cfg.Obsidian.VaultPath)
	if strings.HasPrefix(vault, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		vault = filepath.Join(home, vault[2:])
	}
	path, err := taskdb.Path()
	if err != nil {
		return err
	}
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		return err
	}
	if err := statemigration.Ensure(path, vault, root, logger); err != nil {
		return err
	}
	return obsidian.RequireMigratedNotes(vault)
}

func RecoverTaskMoves() error { return obsidian.RecoverTaskMoves() }
