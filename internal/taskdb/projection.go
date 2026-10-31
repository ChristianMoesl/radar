package taskdb

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"radar/internal/protocol"
)

// The reconciliation algorithm consumes a snapshot, but durable identity/ack
// records and collected source observations occupy separate SQLite tables.
type projection struct {
	Version    int               `json:"version"`
	NextTaskID int               `json:"next_task_id"`
	Records    []json.RawMessage `json:"task_records"`
	SourceRefs []json.RawMessage `json:"source_refs"`
	Sources    json.RawMessage   `json:"sources,omitempty"`
}

func WriteProjectionTx(tx *sql.Tx, data []byte) error {
	var p projection
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM observed_refs; DELETE FROM task_records;"); err != nil {
		return err
	}
	for _, record := range p.Records {
		var id struct {
			ID        string `json:"id"`
			NumericID int    `json:"numeric_id"`
		}
		if err := json.Unmarshal(record, &id); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO task_records(id,numeric_id,data) VALUES(?,?,?)", id.ID, id.NumericID, []byte(record)); err != nil {
			return err
		}
	}
	for _, ref := range p.SourceRefs {
		var id struct {
			ID     string `json:"id"`
			TaskID string `json:"task_record_id"`
		}
		if err := json.Unmarshal(ref, &id); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO observed_refs(id,task_id,data) VALUES(?,?,?)", id.ID, id.TaskID, []byte(ref)); err != nil {
			return err
		}
	}
	p.Records = nil
	p.SourceRefs = nil
	meta, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO documents(name,data) VALUES('projection_meta',?) ON CONFLICT(name) DO UPDATE SET data=excluded.data", meta)
	return err
}

func WriteProjection(path string, data []byte) error {
	db, err := Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := WriteProjectionTx(tx, data); err != nil {
		return err
	}
	return tx.Commit()
}

// WriteProjectionMetadata preserves task and reference rows when only source
// health changed. It uses the existing projection document and schema.
func WriteProjectionMetadata(path string, version, nextTaskID int, sources []protocol.SourceStatus) error {
	statuses, err := json.Marshal(sources)
	if err != nil {
		return err
	}
	meta, err := json.Marshal(projection{Version: version, NextTaskID: nextTaskID, Sources: statuses})
	if err != nil {
		return err
	}
	db, err := Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("INSERT INTO documents(name,data) VALUES('projection_meta',?) ON CONFLICT(name) DO UPDATE SET data=excluded.data", meta)
	return err
}

func ReadProjection(path string) ([]byte, error) {
	db, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return ReadProjectionTx(tx)
}

func ReadProjectionTx(tx *sql.Tx) ([]byte, error) {
	var data []byte
	err := tx.QueryRow("SELECT data FROM documents WHERE name='projection_meta'").Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p projection
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	p.Records = []json.RawMessage{}
	p.SourceRefs = []json.RawMessage{}
	for _, entry := range []struct {
		table  string
		target *[]json.RawMessage
	}{{"task_records", &p.Records}, {"observed_refs", &p.SourceRefs}} {
		rows, err := tx.Query("SELECT data FROM " + entry.table + " ORDER BY rowid")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row []byte
			if err := rows.Scan(&row); err != nil {
				rows.Close()
				return nil, err
			}
			if !json.Valid(row) {
				rows.Close()
				return nil, fmt.Errorf("invalid record in %s", entry.table)
			}
			*entry.target = append(*entry.target, json.RawMessage(row))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(p)
}
