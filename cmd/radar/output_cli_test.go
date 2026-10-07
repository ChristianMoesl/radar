package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
	"radar/internal/version"
)

func TestOutputCLIProcess(t *testing.T) {
	if raw := os.Getenv("RADAR_OUTPUT_TEST_ARGS"); raw != "" {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			panic(err)
		}
		os.Args = append([]string{"radar"}, args...)
		main()
		os.Exit(0)
	}
}

func outputCLI(t *testing.T, args []string, input string, extraEnv ...string) (string, string, int) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestOutputCLIProcess$")
	raw, _ := json.Marshal(args)
	home := t.TempDir()
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + home, "XDG_CONFIG_HOME=" + home, "XDG_STATE_HOME=" + home, "RADAR_DISABLE_COLLECTION=1", "RADAR_OUTPUT_TEST_ARGS=" + string(raw)}, extraEnv...)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("CLI timed out: %s", stderr.String())
	}
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

func TestOutputCLIInformationalCommands(t *testing.T) {
	for _, test := range []struct {
		args []string
		json bool
		want string
		code int
	}{
		{args: []string{"version"}, want: "radar dev"},
		{args: []string{"version", "--json"}, json: true, want: `"version":"dev"`},
		{args: []string{"--json", "version"}, json: true, want: `"version":"dev"`},
		{args: []string{"version", "--json=false"}, want: "radar dev"},
		{args: []string{"state-path"}, want: "tasks.json"},
		{args: []string{"state-path", "--json"}, json: true, want: `"path":`},
		{args: []string{"workspace-context", "--registration-only"}, want: "Not inside a registered Radar workspace"},
		{args: []string{"workspace-context", "--registration-only", "--json"}, json: true, want: `{"registered":false}`},
		{args: []string{"version", "unexpected"}, code: 2},
		{args: []string{"version", "--typo"}, code: 2},
		{args: []string{"--json"}, code: 2},
		{args: []string{"fork", "--json"}, code: 2},
		{args: []string{"setup", "--json"}, code: 2},
		{args: []string{"setup", "notifications", "--json"}, code: 2},
		{args: []string{"--json", "setup", "notifications"}, code: 2},
		{args: []string{"setup", "notifications", "extra"}, code: 2},
		{args: []string{"update", "--json"}, code: 2},
		{args: []string{"--json", "update"}, code: 2},
		{args: []string{"update", "unexpected"}, code: 2},
		{args: []string{"daemon", "--json"}, code: 2},
		{args: []string{"create", "--json"}, code: 2},
		{args: []string{"create", "unexpected"}, code: 2},
		{args: []string{"ack", "0", "--json"}, code: 2},
		{args: []string{"task", "done", "42", "--typo"}, code: 2},
		{args: []string{"reconcile-workspace", "--request", "not json", "--json"}, code: 1},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			stdout, stderr, code := outputCLI(t, test.args, "")
			if code != test.code {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if code != 0 {
				if stdout != "" || stderr == "" {
					t.Fatalf("error streams: stdout=%q stderr=%q", stdout, stderr)
				}
				if code == 1 && !json.Valid([]byte(stderr)) {
					t.Fatalf("runtime error is not JSON: %q", stderr)
				}
				return
			}
			if stderr != "" || !strings.Contains(stdout, test.want) || json.Valid([]byte(stdout)) != test.json {
				t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}

func outputDaemonFixture(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "radar-output-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	currentVersion := version.Current()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var request protocol.Request
			if json.NewDecoder(conn).Decode(&request) != nil {
				_ = conn.Close()
				continue
			}
			task := protocol.Task{ID: 42, Title: "CLI fixture", Attention: "attention"}
			response := protocol.Response{OK: true, Summary: &protocol.Summary{Attention: 1}, Tasks: []protocol.Task{task}}
			switch request.Method {
			case "version":
				response = protocol.Response{OK: true, Version: currentVersion}
			case "gc":
				response = protocol.Response{OK: true, GarbageCollectionResult: &protocol.GarbageCollectionResult{Deleted: []protocol.GarbageCollectionItem{}, Skipped: []protocol.GarbageCollectionItem{}}}
			case "cleanup-preview":
				response = protocol.Response{OK: true, CleanupPreview: &protocol.CleanupPreview{TaskID: 42, TaskTitle: task.Title}}
			case "cleanup":
				response = protocol.Response{OK: true, CleanupResult: &protocol.CleanupResult{TaskID: 42}}
			case "task-delete-preview":
				response = protocol.Response{OK: true, TaskDeletionPreview: &protocol.TaskDeletionPreview{TaskID: 42, TaskTitle: task.Title, Path: "/notes/task.md", TrashDirectory: "/notes/.trash"}}
			case "task-delete":
				response = protocol.Response{OK: true, TaskDeletionResult: &protocol.TaskDeletionResult{TaskID: 42, TrashPath: "/notes/.trash/task.md"}}
			case "task-create":
				task.Title = request.TaskMutation.Title
				response = protocol.Response{OK: true, Task: &task}
			case "task-done", "task-reopen", "task-mute", "task-unmute", "task-priority":
				response = protocol.Response{OK: true, Task: &task}
			}
			_ = json.NewEncoder(conn).Encode(response)
			_ = conn.Close()
		}
	}()
	return "RADAR_SOCKET=" + path
}

func TestOutputCLIDaemonCommands(t *testing.T) {
	socket := outputDaemonFixture(t)
	for _, test := range []struct {
		args   []string
		want   string
		prompt bool
	}{
		{[]string{"status"}, "Tasks: 0 immediate, 1 need attention", false},
		{[]string{"tasks"}, "CLI fixture", false},
		{[]string{"refresh"}, "Radar refreshed.", false},
		{[]string{"reset"}, "Radar state reset and refreshed.", false},
		{[]string{"ack", "42"}, "Acknowledged task 42.", false},
		{[]string{"gc"}, "0 deleted, 0 skipped", false},
		{[]string{"task", "create", "--title", "--json"}, "Task 42: --json", false},
		{[]string{"task", "done", "42"}, "Task 42: CLI fixture", false},
		{[]string{"task", "reopen", "42"}, "Task 42: CLI fixture", false},
		{[]string{"task", "mute", "42"}, "Task 42: CLI fixture", false},
		{[]string{"task", "unmute", "42"}, "Task 42: CLI fixture", false},
		{[]string{"task", "priority", "42", "urgent"}, "Task 42: CLI fixture", false},
		{[]string{"task", "delete", "42"}, "Deleted authored task 42", true},
		{[]string{"cleanup", "42"}, "Cleaned up 0 local resource(s)", true},
	} {
		for _, asJSON := range []bool{false, true} {
			args := append([]string{}, test.args...)
			if asJSON {
				args = append(args, "--json")
			}
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				stdout, stderr, code := outputCLI(t, args, "y\n", socket)
				if code != 0 || json.Valid([]byte(stdout)) != asJSON {
					t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
				}
				if !asJSON && !strings.Contains(stdout, test.want) {
					t.Fatalf("stdout=%q, want %q", stdout, test.want)
				}
				if test.prompt {
					if !strings.Contains(stderr, "[y/N]") || strings.Contains(stdout, "[y/N]") {
						t.Fatalf("confirmation streams: stdout=%q stderr=%q", stdout, stderr)
					}
				} else if stderr != "" {
					t.Fatalf("unexpected stderr: %q", stderr)
				}
			})
		}
	}
}

func TestJSONDoesNotConfirmDestructiveCommands(t *testing.T) {
	socket := outputDaemonFixture(t)
	for _, args := range [][]string{{"cleanup", "42", "--json"}, {"task", "delete", "42", "--json"}} {
		for _, input := range []string{"", "n\n"} {
			stdout, stderr, code := outputCLI(t, args, input, socket)
			if code != 0 || stdout != "" || !strings.Contains(stderr, "cancelled") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		}
	}
}
