package workspacegroup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNoteLockWaitsForFilesystemLockAndReleasesAfterFailure(t *testing.T) {
	root := t.TempDir()
	lock, err := os.OpenFile(filepath.Join(root, ".radar-notes.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	entered := make(chan struct{})
	finished := make(chan error, 1)
	failure := errors.New("operation failed")
	go func() {
		finished <- WithNoteLock(root, func() error { close(entered); return failure })
	}()
	select {
	case <-entered:
		t.Fatal("operation bypassed the filesystem lock")
	case <-time.After(25 * time.Millisecond):
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, failure) {
			t.Fatalf("callback error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("note lock did not unblock")
	}
	if err := WithNoteLock(root, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
