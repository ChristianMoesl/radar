// Package taskdb owns Radar's durable task authority. Notes contain no state.
package taskdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	"radar/internal/protocol"
)

const SchemaVersion = 1

type Task struct {
	ID, Title, State, Priority, CreatedAt, CompletedAt string
	CompletionBaseline                                 string
	Path, Vault                                        string
	Muted, AgentReview, Adopted, Trashed               bool
	Bindings                                           []protocol.SourceBinding
	Revision                                           int64

	// ManualCompletionStates is nil for automatic completion; an empty map is
	// an explicit Done with no known contributors. Keys are source-ref IDs.
	ManualCompletionStates map[string]string
}

func Path() (string, error) {
	if path := os.Getenv("RADAR_STATE"); path != "" {
		return path, nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "radar", "state.db"), nil
}

// Open never reads legacy JSON. The one-time importer is the only legacy reader.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("state database must be a regular file: %s", path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*sql.DB, error) {
		db.Close()
		return nil, fmt.Errorf("open state database %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON; PRAGMA synchronous=FULL;"); err != nil {
		return fail(err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version != 0 && version != SchemaVersion {
		return fail(fmt.Errorf("unsupported durable schema %d (expected %d); restore a compatible binary or migrate explicitly", version, SchemaVersion))
	}
	if version == 0 {
		tx, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return fail(err)
		}
		defer tx.Rollback()
		if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			return fail(err)
		}
		if version == 0 {
			var count int
			if err := tx.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
				return fail(err)
			}
			if count != 0 {
				return fail(fmt.Errorf("unversioned nonempty database; refusing to adopt it"))
			}
			if _, err := tx.Exec(`CREATE TABLE authored_tasks (id TEXT PRIMARY KEY, title TEXT NOT NULL, path TEXT NOT NULL, vault TEXT NOT NULL, trashed INTEGER NOT NULL, revision INTEGER NOT NULL, data BLOB NOT NULL);
CREATE UNIQUE INDEX active_task_titles ON authored_tasks(vault,title) WHERE trashed=0;
CREATE UNIQUE INDEX active_note_paths ON authored_tasks(path) WHERE trashed=0;
CREATE TABLE bindings (identity TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES authored_tasks(id) ON DELETE CASCADE);
CREATE TABLE documents (name TEXT PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE task_records (id TEXT PRIMARY KEY, numeric_id INTEGER NOT NULL UNIQUE, data BLOB NOT NULL);
CREATE TABLE observed_refs (id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES task_records(id), data BLOB NOT NULL);
CREATE TABLE file_moves (id TEXT PRIMARY KEY, source TEXT NOT NULL, destination TEXT NOT NULL, data BLOB NOT NULL);
PRAGMA user_version=1;`); err != nil {
				return fail(err)
			}
		} else if version != SchemaVersion {
			return fail(fmt.Errorf("schema changed while opening"))
		}
		if err := tx.Commit(); err != nil {
			return fail(err)
		}
	}

	return db, nil
}

func Current() (*sql.DB, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	legacy := filepath.Join(filepath.Dir(path), "tasks.json")
	hasLegacy := false
	if legacy != path {
		if _, err := os.Lstat(legacy); err == nil {
			hasLegacy = true
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return nil, fmt.Errorf("existing tasks.json requires explicit state migration")
			}
		}
	}
	db, err := Open(path)
	if err != nil {
		return nil, err
	}
	var pending int
	if err := db.QueryRow("SELECT count(*) FROM documents WHERE name IN ('migration_pending','rollback_pending')").Scan(&pending); err != nil {
		db.Close()
		return nil, err
	}
	if pending > 0 {
		db.Close()
		return nil, fmt.Errorf("state migration/rollback unfinished; use radar migrate-state --resume or --rollback")
	}
	if hasLegacy {
		var complete int
		if err := db.QueryRow("SELECT count(*) FROM documents WHERE name='migration_complete'").Scan(&complete); err != nil {
			db.Close()
			return nil, err
		}
		if complete == 0 {
			db.Close()
			return nil, fmt.Errorf("legacy state is not migrated")
		}
	}
	return db, nil
}

func List(vault string) ([]Task, error) {
	db, err := Current()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT data,revision FROM authored_tasks WHERE vault=? ORDER BY id", filepath.Clean(vault))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		var data []byte
		var revision int64
		if err := rows.Scan(&data, &revision); err != nil {
			return nil, err
		}
		var task Task
		if err := json.Unmarshal(data, &task); err != nil {
			return nil, err
		}
		task.Revision = revision
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func Find(id string) (Task, error) {
	db, err := Current()
	if err != nil {
		return Task{}, err
	}
	defer db.Close()
	var data []byte
	var revision int64
	if err := db.QueryRow("SELECT data,revision FROM authored_tasks WHERE id=?", id).Scan(&data, &revision); err != nil {
		return Task{}, err
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return Task{}, err
	}
	task.Revision = revision
	return task, nil
}

// Put uses optimistic revisions and validates unique source ownership in the
// same transaction as preferences/lifecycle. A failed write publishes no state.
func Put(task *Task) error {
	db, err := Current()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var moving int
	if err := tx.QueryRow("SELECT count(*) FROM file_moves WHERE id=?", task.ID).Scan(&moving); err != nil {
		return err
	}
	if moving > 0 {
		return fmt.Errorf("task has an interrupted file move; restart Radar to recover it")
	}
	updated := *task
	if err := PutTx(tx, &updated); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*task = updated
	return nil
}

func PutTx(tx *sql.Tx, task *Task) error {
	persisted := *task
	persisted.Revision++
	data, err := json.Marshal(persisted)
	if err != nil {
		return err
	}
	if task.Revision == 0 {
		_, err = tx.Exec("INSERT INTO authored_tasks(id,title,path,vault,trashed,revision,data) VALUES(?,?,?,?,?,1,?)", task.ID, task.Title, task.Path, filepath.Clean(task.Vault), task.Trashed, data)
	} else {
		var result sql.Result
		result, err = tx.Exec("UPDATE authored_tasks SET title=?,path=?,vault=?,trashed=?,revision=revision+1,data=? WHERE id=? AND revision=?", task.Title, task.Path, filepath.Clean(task.Vault), task.Trashed, data, task.ID, task.Revision)
		if err == nil {
			n, e := result.RowsAffected()
			if e != nil {
				return e
			}
			if n != 1 {
				return fmt.Errorf("task changed since collection: %s", task.ID)
			}
		}
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM bindings WHERE task_id=?", task.ID); err != nil {
		return err
	}
	bindings := task.Bindings
	if task.Trashed {
		bindings = nil
	}
	for _, binding := range bindings {
		if _, err = tx.Exec("INSERT INTO bindings(identity,task_id) VALUES(?,?)", binding.LinkingKey(), task.ID); err != nil {
			return fmt.Errorf("conflicting source binding ownership: %w", err)
		}
	}
	task.Revision++
	return nil
}

func ReadDocument(path, name string) ([]byte, error) {
	db, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var data []byte
	err = db.QueryRow("SELECT data FROM documents WHERE name=?", name).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return data, err
}

func WriteDocument(path, name string, data []byte) error {
	db, err := Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("INSERT INTO documents(name,data) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET data=excluded.data", name, data)
	return err
}

func Backup(path, destination string) error {
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return fmt.Errorf("backup destination must not exist: %s", destination)
	}
	db, err := Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err = db.Exec("VACUUM INTO ?", destination); err != nil {
		return err
	}
	return os.Chmod(destination, 0o600)
}

func Integrity(path string) error {
	db, err := Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("state database integrity check: %s", result)
	}
	return nil
}
