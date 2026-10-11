package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"radar/internal/client"
)

// Exercise the real daemon lifecycle without a network, actual Pi installation,
// user profile, or host daemon. The same binary works independently of its
// installation layout (including a Homebrew-style versioned Cellar directory).
func TestDaemonSynchronizesStartupProfileAfterListening(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an isolated daemon")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	profile := filepath.Join(root, "custom-profile")
	bin := filepath.Join(root, "bin")
	cellar := filepath.Join(root, "Cellar/radar/0.1.1/bin")
	for _, dir := range []string{home, profile, bin, cellar} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(cellar, "radar")
	build := exec.Command("go", "build", "-ldflags", "-X radar/internal/version.Number=v0.1.1", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(profile, "settings.json"), []byte(`{"packages":["npm:@christianmoesl/pi-radar","npm:@christianmoesl/pi-sbx"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pi-radar", "pi-sbx"} {
		dir := filepath.Join(profile, "npm/node_modules/@christianmoesl", name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"@christianmoesl/`+name+`","version":"0.1.0"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(`#!/bin/sh
if [ "$1" = --version ]; then echo 1.0.0; exit 0; fi
[ "$1" = update ] && [ "$3" = --no-approve ] || exit 9
printf '%s\n' "$2" >> "$PI_CODING_AGENT_DIR/calls"
if [ -f "$PI_CODING_AGENT_DIR/block" ]; then
  touch "$PI_CODING_AGENT_DIR/started"
  while :; do sleep 1; done
fi
name="${2#npm:}"
case "$name" in
  @christianmoesl/pi-radar) version=0.1.1 ;;
  @christianmoesl/pi-sbx) version=0.6.3 ;;
  *) exit 10 ;;
esac
printf '{"name":"%s","version":"%s"}' "$name" "$version" > "$PI_CODING_AGENT_DIR/npm/node_modules/$name/package.json"
`), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "node"), []byte("#!/bin/sh\necho v24.0.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "block"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "radar-sync-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	path := filepath.Join(socketDir, "s")
	logPath := filepath.Join(root, "daemon.log")
	// Isolate the subprocess environment, including the actual selected profile.
	env := append(os.Environ(),
		"HOME="+home, "PATH="+bin+":"+os.Getenv("PATH"), "PI_CODING_AGENT_DIR="+profile,
		"RADAR_SOCKET="+path, "RADAR_PID="+filepath.Join(root, "radar.pid"),
		"RADAR_STATE="+filepath.Join(root, "tasks.json"), "RADAR_LOG="+logPath,
		"PI_OFFLINE=", "RADAR_LOG_LEVEL=debug", "RADAR_LOG_COLOR=0", "RADAR_DISABLE_COLLECTION=1",
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_STATE_HOME="+filepath.Join(root, "state"))
	wait := func(description string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		logs, _ := os.ReadFile(logPath)
		t.Fatalf("timed out: %s\n%s", description, logs)
	}
	launch := func() func() {
		t.Helper()
		cmd := exec.Command(binary, "daemon")
		cmd.Env = env
		cmd.Dir = home
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
				t.Error("daemon did not cancel background work")
			}
		}
		t.Cleanup(stop)
		wait("daemon readiness", func() bool {
			res, err := client.CallWithTimeout(path, "version", 200*time.Millisecond)
			return err == nil && res.OK && res.PID == cmd.Process.Pid
		})
		return stop
	}
	stop := launch()
	wait("background update started", func() bool { _, err := os.Stat(filepath.Join(profile, "started")); return err == nil })
	// The fake package manager is blocked, but daemon health remains responsive.
	if res, err := client.CallWithTimeout(path, "version", time.Second); err != nil || !res.OK {
		t.Fatalf("startup was blocked: %+v %v", res, err)
	}
	stop()
	if _, err := os.Stat(filepath.Join(profile, "radar/package-sync.json")); !os.IsNotExist(err) {
		t.Fatal("interrupted update marked complete")
	}
	if err := os.Remove(filepath.Join(profile, "block")); err != nil {
		t.Fatal(err)
	}
	stop = launch()
	wait("both packages synchronized after restart", func() bool {
		data, _ := os.ReadFile(filepath.Join(profile, "radar/package-sync.json"))
		var receipt struct {
			RadarVersion string            `json:"radar_version"`
			Packages     map[string]string `json:"packages"`
		}
		return json.Unmarshal(data, &receipt) == nil && receipt.RadarVersion == "0.1.1" && len(receipt.Packages) == 2
	})
	stop()
	before, err := os.ReadFile(filepath.Join(profile, "calls"))
	if err != nil || len(strings.Fields(string(before))) != 3 {
		t.Fatalf("expected failed first attempt plus two successful updates: %s %v", before, err)
	}
	stop = launch()
	wait("completed release skipped", func() bool {
		data, _ := os.ReadFile(logPath)
		return strings.Count(string(data), "Pi package already synchronized") == 2
	})
	stop()
	after, _ := os.ReadFile(filepath.Join(profile, "calls"))
	if string(before) != string(after) {
		t.Fatal("ordinary restart repeated package updates")
	}
	if _, err := os.Stat(filepath.Join(home, ".pi")); !os.IsNotExist(err) {
		t.Fatal("touched default profile instead of selected startup profile")
	}
}
