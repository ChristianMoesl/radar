package agentservice

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"radar/internal/protocol"
)

// Intercept only this read, never global state. Real descriptor/path metadata
// makes replacement during capture deterministic without racing a writer.
type diagnosticCheckpointReadFixture struct {
	*os.File
	afterRead func()
	readErr   error
	short     bool
	statErrAt int
	stats     int
}

func (f *diagnosticCheckpointReadFixture) Read(p []byte) (int, error) {
	if f.short {
		n, _ := f.File.Read(p[:1])
		return n, io.EOF
	}
	n, err := f.File.Read(p)
	if f.afterRead != nil {
		mutate := f.afterRead
		f.afterRead = nil
		mutate()
	}
	if f.readErr != nil {
		return n, f.readErr
	}
	return n, err
}

func (f *diagnosticCheckpointReadFixture) Stat() (os.FileInfo, error) {
	f.stats++
	if f.stats == f.statErrAt {
		return nil, errors.New("private-stat-error")
	}
	return f.File.Stat()
}

func TestDiagnosticCheckpointCaptureRejectsObservedChanges(t *testing.T) {
	for _, kind := range []string{"replace_same_size_time", "replace_with_symlink", "replace_with_directory", "remove", "grow", "truncate", "same_size_write", "short_read", "read_error", "initial_stat_error", "final_stat_error", "stable"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "checkpoint.json")
			initial := []byte(`{"private":"initial-body"}`)
			if err := os.WriteFile(path, initial, 0600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			before, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			reader := &diagnosticCheckpointReadFixture{File: file}
			reader.afterRead = func() {
				var err error
				switch kind {
				case "replace_same_size_time", "replace_with_symlink", "replace_with_directory":
					if err = os.Rename(path, path+".old"); err != nil {
						t.Fatal(err)
					}
					switch kind {
					case "replace_same_size_time":
						err = os.WriteFile(path, initial, 0600)
						if err == nil {
							err = os.Chtimes(path, before.ModTime(), before.ModTime())
						}
					case "replace_with_symlink":
						err = os.Symlink(path+".old", path)
					case "replace_with_directory":
						err = os.Mkdir(path, 0700)
					}
				case "remove":
					err = os.Remove(path)
				case "grow":
					err = os.WriteFile(path, append(bytes.Clone(initial), ' '), 0600)
				case "truncate":
					err = os.Truncate(path, int64(len(initial)-1))
				case "same_size_write":
					err = os.WriteFile(path, bytes.Repeat([]byte{' '}, len(initial)), 0600)
					if err == nil {
						// Explicitly distinguish timestamps on coarse filesystems.
						changed := before.ModTime().Add(time.Second)
						err = os.Chtimes(path, changed, changed)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "short_read":
				reader.short = true
			case "read_error":
				reader.readErr = errors.New("private-read-error")
			case "initial_stat_error":
				reader.statErrAt = 1
			case "final_stat_error":
				reader.statErrAt = 2
			}
			data, info, err := readDiagnosticCheckpoint(reader, path)
			if kind == "stable" {
				if err != nil || !bytes.Equal(data, initial) || info == nil || !os.SameFile(before, info) || !before.ModTime().Equal(info.ModTime()) {
					t.Fatal("stable capture or timestamp lost")
				}
				if err := os.WriteFile(path, []byte("later"), 0600); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(data, initial) || !before.ModTime().Equal(info.ModTime()) {
					t.Fatal("later file changes mutated the captured bytes/metadata")
				}
				return
			}
			if err == nil || data != nil || info != nil {
				t.Fatal("observed changed/incomplete checkpoint became usable bytes or timestamp")
			}
			if strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), path) {
				t.Fatal("read failure exposed private error or path")
			}
		})
	}
}

func TestDiagnosticCheckpointAcquisitionBoundAndUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"empty", "exact_bound", "over_bound", "missing", "directory", "symlink", "dangling_symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			const child = "RADAR_TEST_DIAGNOSTIC_CHECKPOINT_FIFO_CHILD"
			if kind == "fifo" && os.Getenv(child) != "1" {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDiagnosticCheckpointAcquisitionBoundAndUnsafeFiles$/fifo$", "-test.count=1")
				cmd.Env = append(os.Environ(), child+"=1")
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("isolated FIFO refusal failed or blocked: %s", output)
				}
				return // CommandContext collected the child before cleanup.
			}
			path := filepath.Join(t.TempDir(), "checkpoint.json")
			var content []byte
			var err error
			switch kind {
			case "empty", "exact_bound", "over_bound":
				if kind != "empty" {
					content = bytes.Repeat([]byte{' '}, protocol.AgentMaxFrameBytes)
					if kind == "over_bound" {
						content = append(content, ' ')
					}
				}
				err = os.WriteFile(path, content, 0600)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "symlink", "dangling_symlink":
				target := path + ".target"
				if kind == "symlink" {
					if err = os.WriteFile(target, []byte("private-content"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				err = os.Symlink(target, path)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.Lstat(path)
			data, info, err := diagnosticCheckpoint(path)
			switch kind {
			case "empty", "exact_bound":
				if err != nil || !bytes.Equal(data, content) || info == nil || !os.SameFile(before, info) {
					t.Fatal("valid exact-bound/empty acquisition lost")
				}
			default:
				if err == nil || data != nil || info != nil || (kind == "missing" && !os.IsNotExist(err)) {
					t.Fatal("unsafe acquisition supplied bytes/metadata or lost missing semantics")
				}
			}
			after, _ := os.Lstat(path)
			if (before == nil) != (after == nil) || (before != nil && (!os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()))) {
				t.Fatal("capture created or changed checkpoint")
			}
		})
	}
}
