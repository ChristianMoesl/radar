package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandContextPreservesOutputAndExitStatus(t *testing.T) {
	cmd := CommandContext(context.Background(), "/bin/sh", "-c", "printf output; printf diagnostic >&2; exit 7")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("error = %v, want exit status 7", err)
	}
	if string(output) != "output" || stderr.String() != "diagnostic" {
		t.Fatalf("stdout = %q, stderr = %q", output, stderr.String())
	}
}

func TestCommandContextAlreadyCanceledDoesNotStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); !errors.Is(err, context.Canceled) || cmd.Process != nil {
		t.Fatalf("error = %v, process = %v; canceled command must not start", err, cmd.Process)
	}
}

func TestCommandContextCancellationStopsHelpers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	helperFile := filepath.Join(t.TempDir(), "helper.pid")
	grandchildFile := filepath.Join(t.TempDir(), "grandchild.pid")
	cmd := CommandContext(ctx, "/bin/sh", "-c", `/bin/sh -c 'sleep 30 & echo "$!" > "$1"; wait' sh "$1" & echo "$!" > "$2"; wait`, "sh", grandchildFile, helperFile)
	done := make(chan error, 1)
	go func() { _, err := cmd.CombinedOutput(); done <- err }()
	// Even a failing assertion must not leave these disposable helpers alive.
	t.Cleanup(func() {
		cancel()
		for _, path := range []string{helperFile, grandchildFile} {
			if data, err := os.ReadFile(path); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		}
	})
	helper, grandchild := waitPID(t, helperFile), waitPID(t, grandchildFile)

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled command unexpectedly succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("command waited for helpers to close their output pipes")
	}
	t.Logf("command and helper cancellation returned in %v", time.Since(start))
	for _, pid := range []int{helper, grandchild} {
		assertStopped(t, pid)
	}
}

func TestCommandContextBoundsInheritedPipesAfterParentExits(t *testing.T) {
	childFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := CommandContext(context.Background(), "/bin/sh", "-c", `sleep 30 & echo "$!" > "$1"; printf complete`, "sh", childFile)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	})
	start := time.Now()
	output, err := cmd.CombinedOutput()
	if !errors.Is(err, exec.ErrWaitDelay) || string(output) != "complete" {
		t.Fatalf("output = %q, error = %v; want bounded pipe drain after successful parent exit", output, err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("inherited pipe took %v to close", elapsed)
	}
	_ = waitPID(t, childFile)
}

func waitPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper did not publish its PID to %s", path)
	return 0
}

func assertStopped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		// Minimal container init processes may not reap orphans immediately.
		// A zombie has exited and cannot keep a pipe open or do further work.
		output, _ := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		status := strings.TrimSpace(string(output))
		if status == "" || strings.HasPrefix(status, "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper %d is still running after cancellation", pid)
}
