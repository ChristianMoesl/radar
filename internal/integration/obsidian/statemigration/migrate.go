// Package statemigration implements the explicit, offline frontmatter/JSON cutover.
// It is not used by runtime collection or persistence readers.
package statemigration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"radar/internal/integration/obsidian"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/state"
	"radar/internal/taskdb"
)

type File struct {
	Path          string
	Before, After []byte
	Mode          os.FileMode
}
type Plan struct {
	Warnings                                   []string
	Version                                    int
	Database, Vault, LegacyPath, WorkspaceRoot string
	Notes                                      []obsidian.LegacyNote
	Inputs                                     []File
	Cache                                      []byte
}

// Preflight reads inputs without opening/creating SQLite, locks or directories.
func Preflight(database, vault, legacy, root string) (Plan, error) {
	if database == legacy {
		return Plan{}, fmt.Errorf("RADAR_STATE must point to the new state.db, not the legacy tasks.json")
	}
	if _, err := os.Lstat(database); err == nil {
		return Plan{}, fmt.Errorf("destination database already exists; use --resume for an interrupted migration")
	} else if !os.IsNotExist(err) {
		return Plan{}, err
	}
	p := Plan{Version: 1, Database: database, Vault: vault, LegacyPath: legacy, WorkspaceRoot: root}
	var err error
	if vault != "" {
		if err := realDirectory(filepath.Join(vault, "Tasks")); err != nil {
			return p, err
		}
		p.Notes, err = obsidian.PlanLegacy(vault)
		if err != nil {
			return p, err
		}
	}
	for _, path := range []string{legacy, workspacegroup.Path(root)} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return p, err
		}
		if !info.Mode().IsRegular() {
			return p, fmt.Errorf("migration input must be regular: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return p, err
		}
		p.Inputs = append(p.Inputs, File{Path: path, Before: data, After: data, Mode: info.Mode().Perm()})
		if path == legacy {
			if err := state.ValidateLegacy(data); err != nil {
				return p, err
			}
			p.Cache = data
			continue
		}
		var registry workspacegroup.Registry
		if err := json.Unmarshal(data, &registry); err != nil {
			return p, err
		}
		if registry.Version != workspacegroup.Version {
			return p, fmt.Errorf("unsupported workspace registry %d", registry.Version)
		}
		byID := map[string]string{}
		for _, n := range p.Notes {
			byID["obsidian:task:"+n.Task.ID] = n.Task.Path
		}
		for _, w := range registry.Workspaces {
			if w.NotePath != "" && byID[w.NoteKey()] != w.NotePath {
				return p, fmt.Errorf("workspace %s has an unresolved note association %s", w.ID, w.NotePath)
			}
		}
	}
	associations, warnings, err := state.LegacyBindings(p.Cache)
	if err != nil {
		return p, err
	}
	p.Warnings = warnings
	noteIDs := map[string]bool{}
	for _, n := range p.Notes {
		noteIDs[n.Task.ID] = true
	}
	for id := range associations {
		if !noteIDs[id] {
			return p, fmt.Errorf("legacy task %s has no note in the configured vault; repair the input before migration", id)
		}
	}
	owned := map[string]string{}
	for i := range p.Notes {
		task := &p.Notes[i].Task
		known := map[string]bool{}
		for _, binding := range task.Bindings {
			known[binding.LinkingKey()] = true
		}
		for _, binding := range associations[task.ID] {
			if !known[binding.LinkingKey()] {
				task.Bindings = append(task.Bindings, binding)
				known[binding.LinkingKey()] = true
			}
		}
		if len(task.Bindings) > 0 {
			task.Adopted = true
		}
		for _, binding := range task.Bindings {
			key := binding.LinkingKey()
			if owner := owned[key]; owner != "" && owner != task.ID {
				return p, fmt.Errorf("conflicting imported binding ownership: %s", key)
			}
			owned[key] = task.ID
		}
	}
	return p, nil
}

func realDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("migration directory must be real: %s", path)
	}
	return nil
}

func verify(path string, before, after []byte, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		return fmt.Errorf("migration input type/permissions changed: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, before) && !bytes.Equal(data, after) {
		return fmt.Errorf("migration input changed: %s; restore or reconcile it before resuming", path)
	}
	return nil
}

func (p Plan) Verify() error {
	if p.Vault != "" {
		known := map[string]bool{}
		for _, n := range p.Notes {
			known[filepath.Clean(n.Task.Path)] = true
		}
		root := filepath.Join(p.Vault, "Tasks")
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("task inventory gained a symlink")
			}
			if !entry.IsDir() {
				continue
			}
			children, err := os.ReadDir(filepath.Join(root, entry.Name()))
			if err != nil {
				return err
			}
			for _, child := range children {
				if !child.IsDir() && strings.EqualFold(filepath.Ext(child.Name()), ".md") && !known[filepath.Join(root, entry.Name(), child.Name())] {
					return fmt.Errorf("task inventory changed; migration refuses to omit new notes")
				}
			}
		}
	}

	for _, f := range p.Inputs {
		if err := verify(f.Path, f.Before, f.After, f.Mode); err != nil {
			return err
		}
	}
	for _, n := range p.Notes {
		if err := realDirectory(filepath.Dir(n.Task.Path)); err != nil {
			return err
		}
		if err := verify(n.Task.Path, n.Before, n.After, n.Mode); err != nil {
			return err
		}
	}
	return nil
}

func (p Plan) BackupPath() string { return p.Database + ".migration-backup" }

// Apply commits all imported authority plus the recovery plan before any note is
// stripped. Resume accepts only exact before/after bytes, never overwriting edits.
func (p Plan) Apply() error {
	return workspacegroup.WithNoteLock(p.WorkspaceRoot, p.apply)
}

func (p Plan) apply() error {
	if err := p.Verify(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.Database), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.Mkdir(p.BackupPath(), 0o700); err != nil {
		return fmt.Errorf("create backup (use resume for an interrupted migration): %w", err)
	}
	if err := writeNew(filepath.Join(p.BackupPath(), "manifest.json"), data); err != nil {
		return err
	}
	return p.importRecords(data)
}

func (p Plan) importRecords(data []byte) error {
	if err := p.Verify(); err != nil {
		return err
	}
	db, err := taskdb.Open(p.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow("SELECT (SELECT count(*) FROM authored_tasks)+(SELECT count(*) FROM documents)").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("destination contains existing task data; refusing to replace it")
	}
	for _, n := range p.Notes {
		task := n.Task
		if err := taskdb.PutTx(tx, &task); err != nil {
			return err
		}
	}
	if len(p.Cache) > 0 {
		if err := taskdb.WriteProjectionTx(tx, p.Cache); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("INSERT INTO documents(name,data) VALUES('migration_pending',?)", data); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return p.finish()
}

func (p Plan) finish() error {
	if err := p.Verify(); err != nil {
		return err
	}
	for _, n := range p.Notes {
		current, err := os.ReadFile(n.Task.Path)
		if err != nil {
			return err
		}
		if bytes.Equal(current, n.After) {
			continue
		}
		if err := replace(n.Task.Path, n.After, n.Mode); err != nil {
			return err
		}
	}
	db, err := taskdb.Open(p.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("BEGIN IMMEDIATE; DELETE FROM documents WHERE name='migration_pending'; INSERT INTO documents(name,data) VALUES('migration_complete','true'); COMMIT;")
	return err
}

func Resume(database string) error {
	if _, err := os.Stat(filepath.Join(database+".migration-backup", "rolled-back")); err == nil {
		return fmt.Errorf("this migration was rolled back; create a fresh migration plan")
	}
	backward, e := taskdb.ReadDocument(database, "rollback_pending")
	if e != nil {
		return e
	}
	if len(backward) > 0 {
		return fmt.Errorf("rollback is unfinished; run radar migrate-state --rollback")
	}
	data, err := taskdb.ReadDocument(database, "migration_pending")
	if err != nil {
		return err
	}
	if len(data) == 0 {
		completed, e := taskdb.ReadDocument(database, "migration_complete")
		if e != nil {
			return e
		}
		if len(completed) > 0 {
			return fmt.Errorf("migration already completed")
		}
		data, e = os.ReadFile(filepath.Join(database+".migration-backup", "manifest.json"))
		if e != nil {
			return fmt.Errorf("no migration recovery plan: %w", e)
		}
		var p Plan
		if err := json.Unmarshal(data, &p); err != nil {
			return err
		}
		if p.Database != database || p.Version != 1 {
			return fmt.Errorf("invalid recovery plan")
		}
		return workspacegroup.WithNoteLock(p.WorkspaceRoot, func() error { return p.importRecords(data) })
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if p.Database != database || p.Version != 1 {
		return fmt.Errorf("invalid migration recovery plan")
	}
	return workspacegroup.WithNoteLock(p.WorkspaceRoot, p.finish)
}

// Rollback is limited to a cutover that has not accepted new application state.
// Restoring a later backup is a separate operational action, not a data merge.
func Rollback(database string) error {
	data, err := os.ReadFile(filepath.Join(database+".migration-backup", "manifest.json"))
	if err != nil {
		return err
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if p.Database != database || p.Version != 1 {
		return fmt.Errorf("invalid rollback manifest")
	}
	return workspacegroup.WithNoteLock(p.WorkspaceRoot, func() error {
		if err := p.Verify(); err != nil {
			return err
		}
		tasks, err := taskdb.List(p.Vault)
		// Pending migrations deliberately disable ordinary readers.
		if err != nil {
			db, e := taskdb.Open(database)
			if e != nil {
				return e
			}
			rows, e := db.Query("SELECT data,revision FROM authored_tasks ORDER BY id")
			if e != nil {
				db.Close()
				return e
			}
			for rows.Next() {
				var raw []byte
				var task taskdb.Task
				if e = rows.Scan(&raw, &task.Revision); e != nil {
					rows.Close()
					db.Close()
					return e
				}
				revision := task.Revision
				if e = json.Unmarshal(raw, &task); e != nil {
					rows.Close()
					db.Close()
					return e
				}
				task.Revision = revision
				tasks = append(tasks, task)
			}
			e = rows.Err()
			rows.Close()
			db.Close()
			if e != nil {
				return e
			}
		}
		if len(tasks) != len(p.Notes) {
			return fmt.Errorf("database contains new task state; rollback would lose data")
		}
		expected := map[string][]byte{}
		for _, n := range p.Notes {
			task := n.Task
			task.Revision = 1
			expected[task.ID], _ = json.Marshal(task)
		}
		for _, task := range tasks {
			actual, _ := json.Marshal(task)
			if !bytes.Equal(actual, expected[task.ID]) {
				return fmt.Errorf("task %s changed after migration; restore a coordinated backup instead", task.ID)
			}
		}
		projection, err := taskdb.ReadProjection(database)
		if err != nil {
			return err
		}
		var oldValue, newValue any
		if len(p.Cache) > 0 {
			if err := json.Unmarshal(p.Cache, &oldValue); err != nil {
				return err
			}
		}
		if len(projection) > 0 {
			if err := json.Unmarshal(projection, &newValue); err != nil {
				return err
			}
		}
		if !reflect.DeepEqual(oldValue, newValue) {
			return fmt.Errorf("collected state changed after migration; rollback would lose data")
		}
		if err := taskdb.WriteDocument(database, "rollback_pending", data); err != nil {
			return err
		}
		for _, n := range p.Notes {
			if err := replace(n.Task.Path, n.Before, n.Mode); err != nil {
				return err
			}
		}
		if err := os.Remove(database); err != nil {
			return err
		}
		if err := writeNew(filepath.Join(p.BackupPath(), "rolled-back"), []byte("true\n")); err != nil {
			return err
		}
		return syncDir(filepath.Dir(database))
	})
}

func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func replace(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".radar-migrate-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
