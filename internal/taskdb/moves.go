package taskdb

import (
	"encoding/json"
	"fmt"
	"os"
)

// Move journals the intended final association before a no-overwrite filesystem
// move. Recovery distinguishes an unexecuted move from a completed rename.
func Move(task *Task, source, destination string, rename func(string, string) error) error {
	db, err := Current()
	if err != nil {
		return err
	}
	data, err := json.Marshal(task)
	if err != nil {
		db.Close()
		return err
	}
	_, err = db.Exec("INSERT INTO file_moves(id,source,destination,data) VALUES(?,?,?,?)", task.ID, source, destination, data)
	db.Close()
	if err != nil {
		return err
	}
	if err := rename(source, destination); err != nil {
		// A failed atomic rename leaves the source intact. Remove only our
		// unexecuted intent, preserving a pre-existing destination.
		db, e := Current()
		if e == nil {
			_, _ = db.Exec("DELETE FROM file_moves WHERE id=?", task.ID)
			db.Close()
		}
		return err
	}
	return finishMove(task)
}

func finishMove(task *Task) error {
	db, err := Current()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updated := *task
	if err := PutTx(tx, &updated); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM file_moves WHERE id=?", task.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*task = updated
	return nil
}

func RecoverMoves(rename func(string, string) error) error {
	db, err := Current()
	if err != nil {
		return err
	}
	rows, err := db.Query("SELECT source,destination,data FROM file_moves")
	if err != nil {
		db.Close()
		return err
	}
	type move struct {
		source, destination string
		data                []byte
	}
	var moves []move
	for rows.Next() {
		var m move
		if err := rows.Scan(&m.source, &m.destination, &m.data); err != nil {
			rows.Close()
			db.Close()
			return err
		}
		moves = append(moves, m)
	}
	err = rows.Err()
	rows.Close()
	db.Close()
	if err != nil {
		return err
	}
	for _, m := range moves {
		_, sourceErr := os.Lstat(m.source)
		_, destinationErr := os.Lstat(m.destination)
		if sourceErr == nil && os.IsNotExist(destinationErr) {
			if err := rename(m.source, m.destination); err != nil {
				return err
			}
		} else if !os.IsNotExist(sourceErr) || destinationErr != nil {
			return fmt.Errorf("ambiguous interrupted task move: %s -> %s", m.source, m.destination)
		}
		var task Task
		if err := json.Unmarshal(m.data, &task); err != nil {
			return err
		}
		if err := finishMove(&task); err != nil {
			return err
		}
	}
	return nil
}
