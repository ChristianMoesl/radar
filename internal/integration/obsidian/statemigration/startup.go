package statemigration

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"radar/internal/integration/obsidian"
	"radar/internal/taskdb"
)

// Ensure upgrades existing data before ordinary readers start. The caller must
// hold the exclusive state lock until startup has finished (the daemon retains
// it for its lifetime). Collection never calls this importer.
func Ensure(database, vault, root string, logger *slog.Logger) error {
	legacy := filepath.Join(filepath.Dir(database), "tasks.json")
	if database == legacy {
		return fmt.Errorf("RADAR_STATE must point to state.db, not tasks.json; the adjacent JSON will migrate automatically")
	}
	exists := func(path string) (bool, error) {
		_, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return false, nil
		}
		return err == nil, err
	}
	hasDatabase, err := exists(database)
	if err != nil {
		return err
	}
	pending := false
	if hasDatabase {
		for _, name := range []string{"rollback_pending", "migration_pending", "migration_complete"} {
			data, err := taskdb.ReadDocument(database, name)
			if err != nil {
				return err
			}
			if len(data) == 0 {
				continue
			}
			switch name {
			case "rollback_pending":
				return fmt.Errorf("state rollback is unfinished; run radar migrate-state --rollback")
			case "migration_pending":
				pending = true
			case "migration_complete":
				if !pending {
					return nil
				}
			}
		}
	}
	backup := database + ".migration-backup"
	hasBackup, err := exists(backup)
	if err != nil {
		return err
	}
	if pending || hasBackup {
		info, err := os.Lstat(backup)
		if err != nil {
			return fmt.Errorf("migration backup unavailable: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("migration backup must be a real directory: %s", backup)
		}
		data, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
		if err != nil {
			return fmt.Errorf("read migration recovery plan: %w", err)
		}
		var p Plan
		if err := json.Unmarshal(data, &p); err != nil {
			return err
		}
		if p.Version != 1 || p.Database != database || p.Vault != vault || p.WorkspaceRoot != root {
			return fmt.Errorf("migration configuration changed; restore its original paths or inspect radar migrate-state --resume")
		}
		logger.Info("resuming task state migration", "backup", backup)
		if err := Resume(database); err != nil {
			return fmt.Errorf("automatic state migration could not resume: %w", err)
		}
		logger.Info("task state migration completed", "notes", len(p.Notes), "backup", backup)
		return nil
	}
	hasLegacy, err := exists(legacy)
	if err != nil {
		return err
	}
	noteErr := obsidian.RequireMigratedNotes(vault)
	if noteErr != nil && !errors.Is(noteErr, obsidian.ErrUnmigratedNotes) {
		return noteErr
	}
	if !hasLegacy && noteErr == nil {
		return nil
	}
	if hasDatabase {
		return fmt.Errorf("unmigrated notes/JSON alongside an existing database; refusing to merge task authority automatically; inspect radar migrate-state")
	}
	p, err := Preflight(database, vault, legacy, root)
	if err != nil {
		return fmt.Errorf("automatic state migration preflight: %w", err)
	}
	logger.Info("migrating existing task state", "notes", len(p.Notes), "legacy_cache", len(p.Cache) > 0, "backup", backup)
	for _, warning := range p.Warnings {
		logger.Warn("retaining observation without a new durable binding", "detail", warning)
	}
	if err := p.Apply(); err != nil {
		return fmt.Errorf("automatic state migration: %w", err)
	}
	logger.Info("task state migration completed", "notes", len(p.Notes), "backup", backup)
	return nil
}
