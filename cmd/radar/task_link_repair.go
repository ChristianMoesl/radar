package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/process"
	"radar/internal/state"
	"radar/internal/taskdb"
)

func readLinkRepairPlan(path string) (state.LinkRepairPlan, error) {
	var plan state.LinkRepairPlan
	f, err := os.Open(path)
	if err != nil {
		return plan, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 2*1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return plan, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return plan, fmt.Errorf("repair plan must contain one JSON object")
	}
	return plan, nil
}

// Preview never creates a database/directory, chmods state or starts a daemon.
func previewTaskLinkRepair(path string) (state.LinkRepairPlan, error) {
	if info, err := os.Lstat(path); err != nil {
		return state.LinkRepairPlan{}, err
	} else if !info.Mode().IsRegular() {
		return state.LinkRepairPlan{}, fmt.Errorf("state database must be a regular file")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return state.LinkRepairPlan{}, err
	}
	u := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return state.LinkRepairPlan{}, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return state.LinkRepairPlan{}, err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return state.LinkRepairPlan{}, err
	}
	if version != taskdb.SchemaVersion {
		return state.LinkRepairPlan{}, fmt.Errorf("unsupported durable state version %d", version)
	}
	data, err := taskdb.ReadProjectionTx(tx)
	if err != nil {
		return state.LinkRepairPlan{}, err
	}
	return state.PlanLinkRepair(data)
}

func applyTaskLinkRepair(path, backup string, plan state.LinkRepairPlan) error {
	if backup == "" {
		return fmt.Errorf("repair requires --backup <new database path>")
	}
	lock, err := taskdb.Lock(path, true)
	if err != nil {
		return err
	}
	defer lock.Close()
	pids, err := process.DaemonPIDs()
	if err != nil {
		return err
	}
	if len(pids) > 0 {
		return fmt.Errorf("stop Radar before repairing task links")
	}
	fresh, err := previewTaskLinkRepair(path)
	if err != nil {
		return err
	}
	if fresh.ProjectionSHA256 != plan.ProjectionSHA256 {
		return fmt.Errorf("projection changed; create a fresh repair plan")
	}
	if err := taskdb.Backup(path, backup); err != nil {
		return err
	}
	db, err := taskdb.Current()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	data, err := taskdb.ReadProjectionTx(tx)
	if err != nil {
		return err
	}
	for _, conflict := range fresh.Conflicts {
		for _, id := range conflict.AuthoredIDs {
			var count int
			if !strings.HasPrefix(id, "obsidian:task:") {
				return fmt.Errorf("unsupported authored identity %s; retain the backup and recover its provider provenance", id)
			}
			if err := tx.QueryRow("SELECT count(*) FROM authored_tasks WHERE id=?", strings.TrimPrefix(id, "obsidian:task:")).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("missing authored task %s; retain the backup and recover its authority before repair", id)
			}
		}
	}
	rows, err := tx.Query("SELECT identity,task_id FROM bindings")
	if err != nil {
		return err
	}
	owners := map[string]string{}
	for rows.Next() {
		var key, owner string
		if err := rows.Scan(&key, &owner); err != nil {
			rows.Close()
			return err
		}
		owners[key] = "obsidian:task:" + owner
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	repaired, err := state.RepairTaskLinks(data, plan, owners)
	if err != nil {
		return err
	}
	journal, err := json.Marshal(map[string]any{"before": json.RawMessage(data), "plan": plan, "backup": backup})
	if err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO documents(name,data) VALUES(?,?)", "task_link_repair:"+plan.ProjectionSHA256, journal); err != nil {
		return err
	}
	if err := taskdb.WriteProjectionTx(tx, repaired); err != nil {
		return err
	}
	return tx.Commit()
}

func runTaskLinkRepair(args []string) {
	flags := outputFlags("radar repair-task-links")
	apply := flags.String("apply", "", "Apply a fully attributed JSON plan offline")
	backup := flags.String("backup", "", "New SQLite backup path, required for apply")
	_ = parseFlags(flags, args)
	if flags.NArg() != 0 || *apply == "" && *backup != "" {
		fatal(fmt.Errorf("usage: radar repair-task-links [--apply plan.json --backup /new/backup.db] [--json]"))
	}
	path, err := taskdb.Path()
	if err != nil {
		fatal(err)
	}
	if *apply != "" {
		plan, err := readLinkRepairPlan(*apply)
		if err != nil {
			fatal(err)
		}
		if err := applyTaskLinkRepair(path, *backup, plan); err != nil {
			fatal(err)
		}
		if jsonOutput {
			printResult(map[string]any{"repaired": true, "backup": *backup})
		} else {
			fmt.Printf("Task links repaired. Original state backup: %s\n", *backup)
		}
		return
	}
	plan, err := previewTaskLinkRepair(path)
	if err != nil {
		fatal(err)
	}
	// A single editable representation is used for both human and machine use.
	if err := json.NewEncoder(os.Stdout).Encode(plan); err != nil {
		fatal(err)
	}
}
