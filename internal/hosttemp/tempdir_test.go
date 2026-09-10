package hosttemp_test

import (
	"path/filepath"
	"testing"

	"radar/internal/hosttemp"
	"radar/internal/process"
	"radar/internal/socket"
)

func TestPiTempOverridePreservesRadarDaemonPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv(hosttemp.EnvironmentVariable, "")
	t.Setenv("TMPDIR", root)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("RADAR_SOCKET", "")
	t.Setenv("RADAR_PID", "")
	beforeSocket, err := socket.Path()
	if err != nil {
		t.Fatal(err)
	}
	beforePID, err := process.PIDPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(hosttemp.EnvironmentVariable, root)
	t.Setenv("TMPDIR", filepath.Join(root, "radar-workspaces", "current"))
	if hosttemp.Dir() != root {
		t.Fatal("lost original host temp root")
	}
	afterSocket, err := socket.Path()
	if err != nil || afterSocket != beforeSocket {
		t.Fatalf("socket moved: %s -> %s, %v", beforeSocket, afterSocket, err)
	}
	afterPID, err := process.PIDPath()
	if err != nil || afterPID != beforePID {
		t.Fatalf("PID file moved: %s -> %s, %v", beforePID, afterPID, err)
	}
	// Explicit runtime configuration still wins over either temp directory.
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	got, _ := socket.Path()
	if got != filepath.Join(runtime, "radar", "radar.sock") {
		t.Fatalf("ignored runtime directory: %s", got)
	}
}
