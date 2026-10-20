package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/config"
	"radar/internal/integration/obsidian/statemigration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/process"
	"radar/internal/taskdb"
)

func runMigrateState(args []string) {
	flags := outputFlags("radar migrate-state")
	apply := flags.Bool("apply", false, "Back up and migrate existing frontmatter and tasks.json")
	rollback := flags.Bool("rollback", false, "Roll back a migration before new state has been accepted")
	resume := flags.Bool("resume", false, "Resume an interrupted migration")
	if err := flags.Parse(args); err != nil {
		fatal(err)
	}
	if flags.NArg() != 0 || ((*apply && *resume) || (*apply && *rollback) || (*resume && *rollback)) {
		fatal(fmt.Errorf("usage: radar migrate-state [--apply|--resume|--rollback]"))
	}
	path, err := taskdb.Path()
	if err != nil {
		fatal(err)
	}
	if *apply || *resume || *rollback {
		lock, err := taskdb.Lock(path, true)
		if err != nil {
			fatal(err)
		}
		defer lock.Close()
		pids, err := process.DaemonPIDs()
		if err != nil {
			fatal(err)
		}
		if len(pids) > 0 {
			fatal(fmt.Errorf("stop Radar before migrating state (daemon PIDs %v)", pids))
		}
	}
	if *rollback {
		if err := statemigration.Rollback(path); err != nil {
			fatal(err)
		}
		printResult(pathResult{Path: path + ".migration-backup"})
		return
	}
	if *resume {
		if err := statemigration.Resume(path); err != nil {
			fatal(err)
		}
		if jsonOutput {
			printResult(map[string]bool{"completed": true})
		} else {
			fmt.Println("State migration completed.")
		}
		return
	}
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		fatal(err)
	}
	vault := cfg.Obsidian.VaultPath
	if strings.HasPrefix(vault, "~/") {
		home, e := os.UserHomeDir()
		if e != nil {
			fatal(e)
		}
		vault = filepath.Join(home, vault[2:])
	}
	p, err := statemigration.Preflight(path, vault, filepath.Join(filepath.Dir(path), "tasks.json"), root)
	if err != nil {
		fatal(err)
	}
	if *apply {
		if err := p.Apply(); err != nil {
			fatal(err)
		}
	}
	if jsonOutput {
		printResult(map[string]any{"database": path, "notes": len(p.Notes), "legacy_cache": len(p.Cache) > 0, "applied": *apply, "backup": p.BackupPath(), "warnings": p.Warnings})
	} else {
		fmt.Printf("State migration: %d notes, legacy cache: %t, applied: %t\nDatabase: %s\nBackup: %s\n", len(p.Notes), len(p.Cache) > 0, *apply, path, p.BackupPath())
		for _, warning := range p.Warnings {
			fmt.Fprintf(os.Stderr, "Retained without a new durable binding: %s\n", warning)
		}
	}
}

func runStateBackup(args []string) {
	args = positionalArgs("state-backup", args, 1)
	path, err := taskdb.Path()
	if err != nil {
		fatal(err)
	}
	ready, err := taskdb.Current()
	if err != nil {
		fatal(err)
	}
	ready.Close()
	if err := taskdb.Backup(path, args[0]); err != nil {
		fatal(err)
	}
	printResult(pathResult{Path: args[0]})
}

func runStateExport(args []string) {
	args = positionalArgs("state-export", args, 0)

	db, err := taskdb.Current()
	if err != nil {
		fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		fatal(err)
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT data,revision FROM authored_tasks ORDER BY id")
	if err != nil {
		fatal(err)
	}
	tasks := []taskdb.Task{}
	for rows.Next() {
		var data []byte
		var task taskdb.Task
		var revision int64
		if err := rows.Scan(&data, &revision); err != nil {
			fatal(err)
		}
		if err := json.Unmarshal(data, &task); err != nil {
			fatal(err)
		}
		task.Revision = revision
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		fatal(err)
	}
	rows.Close()
	projection, err := taskdb.ReadProjectionTx(tx)
	if err != nil {
		fatal(err)
	}
	if len(projection) == 0 {
		projection = []byte("null")
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": taskdb.SchemaVersion, "tasks": tasks, "projection": json.RawMessage(projection)}); err != nil {
		fatal(err)
	}
}
