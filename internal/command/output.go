package command

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// Output runs a provider CLI without stdin or a controlling terminal. Provider
// diagnostics may echo credentials or payloads, so only the exit status escapes.
// Login belongs to foreground setup and must never use this function.
func Output(ctx context.Context, name string, args, env []string) ([]byte, error) {
	data, err := OutputResult(ctx, name, args, env)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// OutputResult also retains bounded stdout on a nonzero exit. Callers may decode
// a provider's structured error result, but must never log this untrusted output.
func OutputResult(ctx context.Context, name string, args, env []string) ([]byte, error) {
	return outputResult(ctx, name, args, env, false)
}

// Version supports tools which print --version on stderr. It is deliberately
// limited to that non-secret command; ordinary requests discard diagnostics.
func Version(ctx context.Context, name string, env []string) ([]byte, error) {
	return outputResult(ctx, name, []string{"--version"}, env, true)
}

func outputResult(ctx context.Context, name string, args, env []string, version bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = env
	var out boundedOutput
	var diagnostic boundedOutput
	cmd.Stdout = &out
	if version {
		cmd.Stderr = &diagnostic
	}
	err := cmd.Run()
	if out.truncated || diagnostic.truncated {
		return nil, fmt.Errorf("%s response exceeded 20 MiB", name)
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s request interrupted: %w", name, ctx.Err())
	}
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return out.buffer.Bytes(), fmt.Errorf("%s exited with status %d; check CLI OAuth login and permissions with radar setup", name, exit.ExitCode())
		}
		return nil, fmt.Errorf("%s could not run; install it on PATH and rerun radar setup", name)
	}
	if version {
		return append(out.buffer.Bytes(), diagnostic.buffer.Bytes()...), nil
	}
	return out.buffer.Bytes(), nil
}

type boundedOutput struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (20 << 20) - b.buffer.Len()
	if len(p) > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}
