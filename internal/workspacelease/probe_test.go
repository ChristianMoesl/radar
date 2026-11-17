package workspacelease

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProbeExclusiveDoesNotCreateMissingState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RADAR_SOCKET", filepath.Join(root, "absent", "radar.sock"))
	path := filepath.Join(root, "workspace")
	for _, existingDir := range []bool{false, true} {
		dir, name, err := location(path)
		if err != nil {
			t.Fatal(err)
		}
		if existingDir {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		value, first := ProbeExclusive(context.Background(), path)
		_, second := ProbeExclusive(context.Background(), path)
		if value != "unknown_no_lock_file" || first != second {
			t.Fatalf("%s unstable observation", value)
		}
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatal("probe created lock file", err)
		}
		if !existingDir {
			if _, err := os.Lstat(filepath.Dir(dir)); !os.IsNotExist(err) {
				t.Fatal("probe created directory", err)
			}
		}
	}
}

func TestProbeExclusiveMatchesNativeLocksWithoutKeepingOrChangingThem(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	path := t.TempDir()
	for _, exclusive := range []bool{false, true} {
		release, err := Acquire(path, exclusive)
		if err != nil {
			t.Fatal(err)
		}
		value, held := ProbeExclusive(context.Background(), path)
		if value != "contended_at_probe" {
			t.Fatalf("exclusive=%t evidence=%s", exclusive, value)
		}
		if acquired, err := Acquire(path, true); err == nil {
			acquired()
			t.Fatal("probe released original holder")
		}
		release()
		value, free := ProbeExclusive(context.Background(), path)
		if value != "exclusive_available_at_probe" || held == free {
			t.Fatalf("released evidence=%s", value)
		}
		acquired, err := Acquire(path, true)
		if err != nil {
			t.Fatal("probe retained its lock", err)
		}
		acquired()
	}
	dir, name, _ := location(path)
	file := filepath.Join(dir, name)
	private := []byte("private-lock-contents-not-read-or-returned")
	if err := os.WriteFile(file, private, 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(file)
	value, first := ProbeExclusive(context.Background(), filepath.Join(path, "."))
	_, second := ProbeExclusive(context.Background(), path)
	after, _ := os.Stat(file)
	data, _ := os.ReadFile(file)
	if value != "exclusive_available_at_probe" || first != second || string(data) != string(private) || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() || before.Size() != after.Size() {
		t.Fatal("probe changed file or native identity")
	}
	if err := os.Rename(file, file+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, private, 0600); err != nil {
		t.Fatal(err)
	}
	value, replaced := ProbeExclusive(context.Background(), path)
	if value != "exclusive_available_at_probe" || replaced == first {
		t.Fatal("same-status inode replacement not distinguished")
	}
}

func TestProbeExclusiveUnsafeFilesAreUnavailableAndNeverBlock(t *testing.T) {
	for _, kind := range []string{"symlink-dir", "symlink-file", "fifo", "directory", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
			path := filepath.Join(root, "workspace")
			dir, name, _ := location(path)
			private := filepath.Join(root, "private")
			if err := os.WriteFile(private, []byte("private"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink-dir" {
				if err := os.Symlink(root, dir); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(dir, name)
				var err error
				switch kind {
				case "symlink-file":
					err = os.Symlink(private, file)
				case "fifo":
					err = unix.Mkfifo(file, 0600)
				case "directory":
					err = os.Mkdir(file, 0700)
				case "hardlink":
					err = os.Link(private, file)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			value, fingerprint := ProbeExclusive(context.Background(), path)
			if value != "unavailable" || fingerprint != [32]byte{} {
				t.Fatalf("unsafe observation: %s", value)
			}
			data, _ := os.ReadFile(private)
			if string(data) != "private" {
				t.Fatal("probe changed private file")
			}
		})
	}
}

func TestProbeExclusiveCancellationAndInvalidPathsRemainUnknown(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, item := range []struct {
		ctx  context.Context
		path string
	}{{ctx, t.TempDir()}, {context.Background(), "relative"}, {context.Background(), "/" + strings.Repeat("x", 4096)}} {
		if value, fingerprint := ProbeExclusive(item.ctx, item.path); value != "unavailable" || fingerprint != [32]byte{} {
			t.Fatalf("invalid evidence=%s", value)
		}
	}
}

func TestProbeExclusiveMissingFileDoesNotProveUnlinkedLeaseAbsent(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	path := t.TempDir()
	release, err := Acquire(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	dir, name, _ := location(path)
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	if value, _ := ProbeExclusive(context.Background(), path); value != "unknown_no_lock_file" {
		t.Fatalf("unlinked live inode became absence: %s", value)
	}
}
