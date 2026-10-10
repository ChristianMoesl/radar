package main

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"radar/internal/client"
	"radar/internal/update"
)

func TestUpdateConfirmationDefaultsToNo(t *testing.T) {
	for _, input := range []string{"", "\n", "no\n", "yes\n"} {
		if confirmUpdate(bufio.NewScanner(strings.NewReader(input)), io.Discard, "Install?") {
			t.Fatalf("accepted %q", input)
		}
	}
	if !confirmUpdate(bufio.NewScanner(strings.NewReader("y\n")), io.Discard, "Install?") {
		t.Fatal("rejected confirmation")
	}
}

// No real host daemon/configuration/notifications or Pi profile is used. This
// tests actual differently-versioned executables, not a mocked health response.
func TestUpdateDaemonIdentityAndOldClientGuard(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS updater")
	}
	if testing.Short() {
		t.Skip("builds two isolated executables")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "radar")
	next := filepath.Join(root, "radar-next")
	for path, number := range map[string]string{binary: "v0.1.0", next: "v0.1.1"} {
		cmd := exec.Command("go", "build", "-ldflags", "-X radar/internal/version.Number="+number, "-o", path, ".")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, output)
		}
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("RADAR_PID", filepath.Join(root, "radar.pid"))
	t.Setenv("RADAR_STATE", filepath.Join(root, "tasks.json"))
	t.Setenv("RADAR_DISABLE_COLLECTION", "1")
	// Unix sockets have a short path limit; do not use the long test directory.
	socketDir, err := os.MkdirTemp("/tmp", "radar-update-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	path := filepath.Join(socketDir, "radar.sock")
	t.Setenv("RADAR_SOCKET", path)
	t.Cleanup(func() {
		if err := stopUpdateDaemon(path); err != nil {
			t.Error("stop isolated daemon:", err)
		}
	})
	firstHash, _ := update.FileDigest(binary)
	if err := startUpdateDaemon(path, binary, "v0.1.0+"+firstHash); err != nil {
		t.Fatal(err)
	}
	before, err := client.CallWithTimeout(path, "version", time.Second)
	if err != nil || before.PID <= 0 {
		t.Fatalf("identity: %+v %v", before, err)
	}
	if err := os.Rename(next, binary); err != nil {
		t.Fatal(err)
	}
	// A resident process must not act using old code against a replaced binary.
	response, err := client.CreateTask(path, "must not be created")
	if err != nil || response.OK || !strings.Contains(response.Error, "Radar was replaced") {
		t.Fatalf("old daemon mutation: %+v %v", response, err)
	}
	if err := stopUpdateDaemon(path); err != nil {
		t.Fatal(err)
	}
	secondHash, _ := update.FileDigest(binary)
	if err := startUpdateDaemon(path, binary, "v0.1.1+"+secondHash); err != nil {
		t.Fatal(err)
	}
	after, err := client.CallWithTimeout(path, "version", time.Second)
	if err != nil || after.PID == before.PID {
		t.Fatalf("new daemon: %+v %v", after, err)
	}
	if identity, err := restoredIdentity(binary, secondHash); err != nil || identity != after.Version {
		t.Fatalf("recovery identity: %q %v", identity, err)
	}
}

func TestFailedUpdateDaemonDoesNotWaitForUnrelatedHealth(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS updater")
	}
	file := filepath.Join(t.TempDir(), "failed-radar")
	if err := os.WriteFile(file, []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := startUpdateDaemon("/tmp/nonexistent-radar-update-test.sock", file, "v0.1.1+unknown"); err == nil {
		t.Fatal("accepted exited daemon")
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("failed daemon not recognized promptly")
	}
}

func TestNotificationSetupCLIStillSupportsDeferring(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS notification setup")
	}
	// Deferring prints guidance only; never launches a helper or system settings.
	stdout, stderr, code := outputCLI(t, []string{"setup", "notifications"}, "d\n")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "Your notification preference was not changed by deferring.") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestUpdateCLICommand(t *testing.T) {
	notInstalled := "Radar update: run the installed ~/.local/bin/radar"
	if runtime.GOOS != "darwin" {
		notInstalled = "Radar update: managed updates are macOS-only"
	}
	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"help", []string{"help"}, 0, "radar update"},
		{"update", []string{"update"}, 1, notInstalled},
		{"old name is not an alias", []string{"upgrade"}, 2, "radar update"},
		{"removed TUI flag", []string{"update", "--from-tui"}, 2, "flag provided but not defined: -from-tui"},
		{"JSON before command", []string{"--json", "update"}, 2, "radar update does not produce a JSON result"},
		{"JSON after command", []string{"update", "--json"}, 2, "radar update does not produce a JSON result"},
		{"unexpected argument", []string{"update", "extra"}, 2, "usage: radar update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := outputCLI(t, tc.args, "")
			output := stdout + stderr
			if code != tc.code || !strings.Contains(output, tc.want) {
				t.Fatalf("exit %d: %s", code, output)
			}
			if strings.Contains(output, "radar upgrade") || strings.Contains(output, "Press Enter") {
				t.Fatalf("obsolete workflow guidance: %s", output)
			}
			if tc.name == "update" && runtime.GOOS == "darwin" && !strings.Contains(output, "brew upgrade radar") {
				t.Fatal("missing Homebrew upgrade guidance")
			}
			if tc.name == "help" && !strings.Contains(output, "radar setup notifications") {
				t.Fatal("missing notification setup command")
			}
		})
	}
}
