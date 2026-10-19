package setup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"time"

	"radar/internal/command"
)

type RealSystem struct{}

func (RealSystem) LookPath(name string) bool { _, err := exec.LookPath(name); return err == nil }
func (RealSystem) Platform() string          { return runtime.GOOS }
func (RealSystem) Output(ctx context.Context, c Command) (string, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	var out, diagnostic commandOutput
	cmd := command.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	err := cmd.Run()
	if out.truncated {
		return "", errors.New("command output exceeded the setup diagnostic limit")
	}
	if err != nil {
		return diagnostic.String(), err
	}
	return out.String(), nil
}
func (RealSystem) Run(ctx context.Context, c Command) error {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

type commandOutput struct {
	bytes.Buffer
	truncated bool
}

func (b *commandOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := (1 << 20) - b.Len()
	if len(data) > remaining {
		b.truncated = true
		data = data[:remaining]
	}
	_, _ = b.Buffer.Write(data)
	return n, nil
}
