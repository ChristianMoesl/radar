// Package command bounds cancellation of non-interactive integration commands.
package command

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// CommandContext isolates a command and its helpers in a process group so
// cancellation stops both, rather than leaving helpers holding output pipes.
// WaitDelay also bounds pipe draining if a descendant escapes the group or the
// parent exits before cancellation. Interactive commands must retain their
// terminal's process group and should not use this constructor.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 250 * time.Millisecond
	return cmd
}
