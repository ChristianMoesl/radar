package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"radar/internal/client"
	"radar/internal/taskdb"
)

func TestDaemonAutomaticallyMigratesLegacyStateBeforeServing(t *testing.T) {
	root := t.TempDir()
	vault, workspace := filepath.Join(root, "vault"), filepath.Join(root, "workspaces")
	configHome := filepath.Join(root, "config")
	database := filepath.Join(root, "state.db")
	id := "12345678-1234-4234-8234-123456789abc"
	note := filepath.Join(vault, "Tasks", "Plan--12345678", "Plan.md")
	for _, dir := range []string{filepath.Dir(note), filepath.Join(configHome, "radar")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	content := []byte("---\nradar-id: " + id + "\nradar-title: Plan\nradar-state: open\nradar-priority: urgent\nradar-muted: true\nradar-created-at: 2026-10-01T10:00:00Z\nradar-completed-at:\nowner: Example\n---\nUser content.\n")
	files := map[string][]byte{
		note: content,
		filepath.Join(configHome, "radar", "config.yaml"): []byte(fmt.Sprintf("obsidian: {vault_path: %q}\nworkspace: {root_dir: %q}\n", vault, workspace)),
		filepath.Join(root, "tasks.json"):                 []byte(`{"version":8,"next_task_id":7,"task_records":[{"id":"obsidian:task:` + id + `","numeric_id":7,"canonical_key":"obsidian:task:` + id + `","state":"active","source_ref_ids":[],"ack":{"cursor":"2026-10-01T10:00:00Z"},"snapshot":{}}],"source_refs":[]}`),
	}
	for path, data := range files {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	socketDir, err := os.MkdirTemp("/tmp", "radar-migrate-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "s")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_STATE", database)
	launch := func() func() {
		t.Helper()
		cmd := exec.Command(executable, "-test.run=^TestOutputCLIProcess$")
		cmd.Dir = root
		cmd.Env = []string{
			"HOME=" + root, "PATH=" + root, "GOMAXPROCS=2", "XDG_CONFIG_HOME=" + configHome,
			"XDG_STATE_HOME=" + filepath.Join(root, "state"), "RADAR_STATE=" + database,
			"RADAR_PID=" + filepath.Join(root, "radar.pid"), "RADAR_SOCKET=" + socketPath,
			"RADAR_LOG=" + filepath.Join(root, "daemon.log"), "RADAR_LOG_COLOR=0",
			"RADAR_DISABLE_COLLECTION=1", `RADAR_OUTPUT_TEST_ARGS=["daemon"]`,
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		stop := func() {
			t.Helper()
			if stopped {
				return
			}
			stopped = true
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-done:
				if err != nil {
					t.Error("daemon shutdown:", err)
				}
			case <-time.After(3 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("daemon did not stop")
			}
		}
		t.Cleanup(stop)
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			res, err := client.CallWithTimeout(socketPath, "version", 100*time.Millisecond)
			if err == nil && res.OK && res.PID == cmd.Process.Pid {
				return stop
			}
			time.Sleep(20 * time.Millisecond)
		}
		logs, _ := os.ReadFile(filepath.Join(root, "daemon.log"))
		t.Fatalf("daemon failed to migrate/start:\n%s", logs)
		return nil
	}
	stop := launch()
	after, err := os.ReadFile(note)
	if err != nil || !bytes.Equal(after, []byte("---\nowner: Example\n---\nUser content.\n")) {
		t.Fatalf("migration altered note content: %q %v", after, err)
	}
	task, err := taskdb.Find(id)
	if err != nil || task.Title != "Plan" || !task.Muted || task.Priority != "urgent" {
		t.Fatalf("imported task: %+v %v", task, err)
	}
	backup, err := os.ReadFile(filepath.Join(database+".migration-backup", "manifest.json"))
	var plan struct{ Notes []struct{ Before []byte } }
	if err != nil || json.Unmarshal(backup, &plan) != nil || len(plan.Notes) != 1 || !bytes.Equal(plan.Notes[0].Before, content) {
		t.Fatal("original note was not backed up", err)
	}
	if lock, err := taskdb.Lock(database, true); err == nil {
		lock.Close()
		t.Fatal("running daemon did not exclude offline migration")
	}
	res, err := client.CallWithTimeout(socketPath, "reset", 5*time.Second)
	if err != nil || !res.OK {
		t.Fatalf("reset: %+v %v", res, err)
	}
	stop()
	stop = launch()
	defer stop()
	projection, err := taskdb.ReadProjection(database)
	if err != nil || !bytes.Contains(projection, []byte(`"numeric_id":7`)) || !bytes.Contains(projection, []byte("2026-10-01T10:00:00Z")) {
		t.Fatalf("restart/reset lost imported identity or acknowledgement: %s %v", projection, err)
	}
	logs, _ := os.ReadFile(filepath.Join(root, "daemon.log"))
	if strings.Count(string(logs), "task state migration completed") != 1 {
		t.Fatalf("migration was not performed exactly once:\n%s", logs)
	}
}
