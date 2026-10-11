package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"radar/internal/update"
)

const syncPackages = `["npm:@christianmoesl/pi-radar","npm:@christianmoesl/pi-sbx","npm:unrelated"]`

func syncFixture(t *testing.T, packages string) (string, func(string) error, func() []string) {
	t.Helper()
	profile, _ := releaseFixture(t, packages)
	t.Setenv("PI_OFFLINE", "")
	t.Setenv("SYNC_RADAR_VERSION", "0.1.1")
	t.Setenv("SYNC_FAIL", "")
	t.Setenv("SYNC_BLOCK", "")
	fakePi(t, `
[ "$1" = update ] && [ "$3" = --no-approve ] && [ "$#" = 3 ] || exit 9
[ "$(pwd -P)" = "$(cd "$HOME" && pwd -P)" ] || exit 10
name="${2#npm:}"
case "$name" in @christianmoesl/pi-radar|@christianmoesl/pi-sbx) ;; *) exit 11 ;; esac
mkdir "$PI_CODING_AGENT_DIR/updating" || exit 12
trap 'rmdir "$PI_CODING_AGENT_DIR/updating"' EXIT
printf '%s\n' "$name" >> "$PI_CODING_AGENT_DIR/calls"
if [ "$SYNC_BLOCK" = yes ]; then
  echo $$ > "$PI_CODING_AGENT_DIR/pi-pid"
  while [ ! -f "$PI_CODING_AGENT_DIR/proceed" ]; do sleep 0.01; done
fi
if [ "$SYNC_FAIL" = "$name" ]; then exit 1; fi
if [ "$name" = @christianmoesl/pi-radar ]; then version="$SYNC_RADAR_VERSION"; else version=0.6.3; fi
printf '{"name":"%s","version":"%s"}' "$name" "$version" > "$PI_CODING_AGENT_DIR/npm/node_modules/$name/package.json"
`)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	run := func(release string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return SyncPackages(ctx, profile, release, logger)
	}
	calls := func() []string {
		data, err := os.ReadFile(filepath.Join(profile, "calls"))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Fields(string(data))
	}
	return profile, run, calls
}

func TestSyncPackagesOncePerReleaseAndProfile(t *testing.T) {
	profile, run, calls := syncFixture(t, syncPackages)
	settings, _ := os.ReadFile(filepath.Join(profile, "settings.json"))
	if err := run("v0.1.1"); err != nil {
		t.Fatal(err)
	}
	if err := run("0.1.1"); err != nil {
		t.Fatal(err)
	}
	if got := calls(); !reflect.DeepEqual(got, []string{update.Package, SandboxPackage}) {
		t.Fatal(got)
	}
	t.Setenv("SYNC_RADAR_VERSION", "0.1.2")
	if err := run("v0.1.2"); err != nil {
		t.Fatal(err)
	}
	if got := calls(); !reflect.DeepEqual(got, []string{update.Package, SandboxPackage, update.Package, SandboxPackage}) {
		t.Fatal(got)
	}
	actual, _ := os.ReadFile(filepath.Join(profile, "settings.json"))
	if !bytes.Equal(actual, settings) {
		t.Fatal("changed package declarations")
	}
	receipt, err := readSyncReceipt(filepath.Join(profile, "radar/package-sync.json"))
	if err != nil || receipt.RadarVersion != "0.1.2" || receipt.Packages[update.Package] != "0.1.2" || receipt.Packages[SandboxPackage] != "0.6.3" {
		t.Fatalf("%+v %v", receipt, err)
	}
}

func TestSyncUsesCapturedProfileNotCurrentEnvironment(t *testing.T) {
	profile, run, calls := syncFixture(t, syncPackages)
	other := filepath.Join(t.TempDir(), "other-profile")
	writeFixture(t, filepath.Join(other, "settings.json"), `{"packages":[]}`)
	t.Setenv("PI_CODING_AGENT_DIR", other)
	if err := run("v0.1.1"); err != nil {
		t.Fatal(err)
	}
	if len(calls()) != 2 {
		t.Fatal(calls())
	}
	if _, err := os.Stat(filepath.Join(other, "radar")); !os.IsNotExist(err) {
		t.Fatal("touched another profile")
	}
	if _, err := os.Stat(filepath.Join(profile, "radar/package-sync.json")); err != nil {
		t.Fatal(err)
	}
	// A separate profile has no receipt just because the first was synchronized.
	for _, name := range []string{update.Package, SandboxPackage} {
		writeFixture(t, filepath.Join(other, "npm/node_modules", name, "package.json"), `{"name":"`+name+`","version":"0.1.0"}`)
	}
	writeFixture(t, filepath.Join(other, "settings.json"), `{"packages":`+syncPackages+`}`)
	if err := SyncPackages(context.Background(), other, "v0.1.1", slog.Default()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(other, "calls"))
	if len(strings.Fields(string(data))) != 2 {
		t.Fatal(string(data))
	}
}

func TestSyncRetriesOnlyFailedPackagesAndLogsFailure(t *testing.T) {
	profile, run, calls := syncFixture(t, syncPackages)
	t.Setenv("SYNC_FAIL", update.Package)
	var logs bytes.Buffer
	err := SyncPackages(context.Background(), profile, "v0.1.1", slog.New(slog.NewTextHandler(&logs, nil)))
	if err == nil || !strings.Contains(err.Error(), update.Package) {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "retry with radar restart") || !strings.Contains(logs.String(), "Pi package synchronized") {
		t.Fatal(logs.String())
	}
	receipt, err := readSyncReceipt(filepath.Join(profile, "radar/package-sync.json"))
	if err != nil || len(receipt.Packages) != 1 || receipt.Packages[SandboxPackage] != "0.6.3" {
		t.Fatalf("%+v %v", receipt, err)
	}
	t.Setenv("SYNC_FAIL", "")
	if err := run("v0.1.1"); err != nil {
		t.Fatal(err)
	}
	if got := calls(); !reflect.DeepEqual(got, []string{update.Package, SandboxPackage, update.Package}) {
		t.Fatal(got)
	}
}

func TestSyncVersionMismatchRemainsRetryable(t *testing.T) {
	_, run, calls := syncFixture(t, syncPackages)
	t.Setenv("SYNC_RADAR_VERSION", "0.1.0")
	if err := run("v0.1.1"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatal(err)
	}
	t.Setenv("SYNC_RADAR_VERSION", "0.1.1")
	if err := run("v0.1.1"); err != nil {
		t.Fatal(err)
	}
	if got := calls(); !reflect.DeepEqual(got, []string{update.Package, SandboxPackage, update.Package}) {
		t.Fatal(got)
	}
}

func TestSyncSkipsPinsWithoutRecordingThemAndAllowsManualUnpin(t *testing.T) {
	profile, run, calls := syncFixture(t, `["npm:@christianmoesl/pi-radar@0.1.0","npm:@christianmoesl/pi-sbx"]`)
	if err := run("v0.1.1"); err != nil {
		t.Fatal(err)
	}
	if got := calls(); !reflect.DeepEqual(got, []string{SandboxPackage}) {
		t.Fatal(got)
	}
	writeFixture(t, filepath.Join(profile, "settings.json"), `{"packages":`+syncPackages+`}`)
	if err := run("v0.1.1"); err != nil {
		t.Fatal(err)
	}
	if got := calls(); !reflect.DeepEqual(got, []string{SandboxPackage, update.Package}) {
		t.Fatal(got)
	}
}

func TestSyncDoesNotCreateMissingProfilesOrUpdateDevelopmentBuilds(t *testing.T) {
	profile, run, calls := syncFixture(t, syncPackages)
	for _, version := range []string{"dev", "", "unknown", "v0.1.1-2-gabc", "v0.1.2-beta.1"} {
		if err := run(version); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(profile, "radar")); !os.IsNotExist(err) {
		t.Fatal("created sync files for dev build")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := SyncPackages(context.Background(), missing, "v0.1.1", slog.Default()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("created missing profile")
	}
}

func TestSyncRejectsMalformedReceiptsWithoutUpdating(t *testing.T) {
	for _, data := range []string{"{", "null", `{}`, `{"radar_version":"0.1.1","packages":{"unrelated":"1.0.0"}}`, `{"radar_version":"0.1.1","packages":{"@christianmoesl/pi-radar":"dev"}}`} {
		t.Run(data, func(t *testing.T) {
			profile, run, calls := syncFixture(t, syncPackages)
			writeFixture(t, filepath.Join(profile, "radar/package-sync.json"), data)
			if err := run("v0.1.1"); err == nil || !strings.Contains(err.Error(), "inspect/remove") {
				t.Fatal(err)
			}
			if len(calls()) != 0 {
				t.Fatal(calls())
			}
			actual, _ := os.ReadFile(filepath.Join(profile, "radar/package-sync.json"))
			if string(actual) != data {
				t.Fatal("changed malformed state")
			}
		})
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for", path)
}

func TestConcurrentSyncStartsSerializeAndRecheckReceipt(t *testing.T) {
	profile, run, calls := syncFixture(t, syncPackages)
	t.Setenv("SYNC_BLOCK", "yes")
	done := make(chan error, 2)
	go func() { done <- run("v0.1.1") }()
	waitForFile(t, filepath.Join(profile, "pi-pid"))
	go func() { done <- run("v0.1.1") }()
	writeFixture(t, filepath.Join(profile, "proceed"), "")
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("sync did not finish")
		}
	}
	if got := calls(); !reflect.DeepEqual(got, []string{update.Package, SandboxPackage}) {
		t.Fatal(got)
	}
}

func TestSyncCancellationStopsPackageGroupWithoutReceipt(t *testing.T) {
	profile, _, _ := syncFixture(t, syncPackages)
	t.Setenv("SYNC_BLOCK", "yes")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- SyncPackages(ctx, profile, "v0.1.1", slog.Default()) }()
	waitForFile(t, filepath.Join(profile, "pi-pid"))
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("package cancellation hung")
	}
	if _, err := os.Stat(filepath.Join(profile, "radar/package-sync.json")); !os.IsNotExist(err) {
		t.Fatal("recorded interrupted update")
	}
	lock, err := os.OpenFile(filepath.Join(profile, "radar/package-sync.lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal("lock leaked after cancellation:", err)
	}
}

func TestSyncLockWaitCanBeCancelled(t *testing.T) {
	profile, _, calls := syncFixture(t, syncPackages)
	path := filepath.Join(profile, "radar/package-sync.lock")
	writeFixture(t, path, "")
	lock, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := SyncPackages(ctx, profile, "v0.1.1", slog.Default()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if len(calls()) != 0 {
		t.Fatal(calls())
	}
}

// An isolated helper models a daemon crash. Its fake Pi child keeps descriptor 3
// and therefore the lock until it exits, even after the daemon process is killed.
func TestSyncLockSurvivesDaemonCrash(t *testing.T) {
	if os.Getenv("RADAR_SYNC_LOCK_HELPER") == "1" {
		profile := os.Getenv("PI_CODING_AGENT_DIR")
		if err := SyncPackages(context.Background(), profile, "v0.1.1", slog.Default()); err != nil {
			t.Fatal(err)
		}
		return
	}
	profile, _, _ := syncFixture(t, syncPackages)
	fakePi(t, `echo $$ > "$PI_CODING_AGENT_DIR/pi-pid"
exec sleep 30
`)
	cmd := exec.Command(os.Args[0], "-test.run=^TestSyncLockSurvivesDaemonCrash$")
	cmd.Env = append(os.Environ(), "RADAR_SYNC_LOCK_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	waitForFile(t, filepath.Join(profile, "pi-pid"))
	var pid int
	data, _ := os.ReadFile(filepath.Join(profile, "pi-pid"))
	if err := json.Unmarshal(bytes.TrimSpace(data), &pid); err != nil || pid <= 0 {
		t.Fatalf("%q %v", data, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	lock, err := os.OpenFile(filepath.Join(profile, "radar/package-sync.lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
		t.Fatal("Pi child did not retain the profile lock:", err)
	}
}

func TestSyncPrerequisitesRemainRetryable(t *testing.T) {
	for _, problem := range []string{"offline", "old-node", "old-pi"} {
		t.Run(problem, func(t *testing.T) {
			profile, run, calls := syncFixture(t, syncPackages)
			switch problem {
			case "offline":
				t.Setenv("PI_OFFLINE", "true")
			case "old-node", "old-pi":
				bin := t.TempDir()
				name, version := "node", "v20.0.0"
				if problem == "old-pi" {
					name, version = "pi", "0.1.0"
				}
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho "+version+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			}
			if err := run("v0.1.1"); err == nil {
				t.Fatal("recorded success without prerequisites")
			}
			if len(calls()) != 0 {
				t.Fatal(calls())
			}
			if _, err := os.Stat(filepath.Join(profile, "radar/package-sync.json")); !os.IsNotExist(err) {
				t.Fatal("wrote success receipt")
			}
		})
	}
}
