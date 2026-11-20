package workspacewatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Runs on each native supported Go CI platform. No mock OS events or skip for
// missing watcher support: a backend failure on Linux/macOS is a test failure.
func TestNativeWatchRearmsAfterRootAndParentRecreation(t *testing.T) {
	base := t.TempDir()
	parent, away := filepath.Join(base, "parent"), filepath.Join(base, "away")
	root := filepath.Join(parent, "workspaces")
	anchor := filepath.Join(root, "ABC-123")
	if err := os.MkdirAll(anchor, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	requests := make(chan struct{}, 100)
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx, root, func() []string { return []string{anchor} }, func() { requests <- struct{}{} }, func(err error) {
			if err != nil {
				t.Errorf("native watch coverage: %v", err)
			}
		}, openNative)
	}()
	t.Cleanup(func() { cancel(); <-done })
	next := func() {
		t.Helper()
		select {
		case <-requests:
		case <-ctx.Done():
			t.Fatal("native filesystem invalidation missing")
		}
	}
	next()
	next() // Initial coverage and post-arming observation.

	// Let the authoritative-request sink collect synchronously in this fixture:
	// settle waits for the fixed production coalescing window, not a target or
	// pass/fail timing budget. Later anchor mutations prove actual reattachment.
	settle := func() {
		t.Helper()
		for {
			select {
			case <-requests:
			case <-time.After(150 * time.Millisecond):
				return
			case <-ctx.Done():
				t.Fatal("watcher failed to settle")
			}
		}
	}
	for _, path := range []string{root, parent} {
		if err := os.Rename(path, away); err != nil {
			t.Fatal(err)
		}
		next()
		settle()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("watcher created missing directories")
		}
		if err := os.MkdirAll(anchor, 0o700); err != nil {
			t.Fatal(err)
		}
		next()
		settle()
		if err := os.Remove(anchor); err != nil {
			t.Fatal(err)
		}
		next()
		settle()
		if err := os.Mkdir(anchor, 0o700); err != nil {
			t.Fatal(err)
		}
		next()
		settle()
		if err := os.RemoveAll(away); err != nil {
			t.Fatal(err)
		}
	}
	// Atomically replace the now-empty root with a different directory inode.
	// A later child event must come from the new root, not the old watch.
	if err := os.Remove(anchor); err != nil {
		t.Fatal(err)
	}
	next()
	settle()
	replacement := filepath.Join(parent, "replacement")
	if err := os.MkdirAll(filepath.Join(replacement, "ABC-123"), 0o700); err != nil {
		t.Fatal(err)
	}
	// os.Rename deliberately refuses an existing directory even when the
	// native rename syscall can atomically replace that empty directory.
	if err := unix.Rename(replacement, root); err != nil {
		t.Fatal(err)
	}
	next()
	settle()
	if err := os.Remove(anchor); err != nil {
		t.Fatal(err)
	}
	next()
}
