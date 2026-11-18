package integration

import (
	"context"
	"io"
	"os/exec"

	"radar/internal/protocol"
)

type SessionContext struct {
	Name string
	ID   string
	Path string
}

type Session struct {
	Name    string
	ID      string
	Path    string
	Created bool
}

// OpenSessionRequest opens a command in a new session or a new window of an
// existing session, optionally switching the client, as one admitted operation.
type OpenSessionRequest struct {
	Name    string // Exact reusable session name or a collected native session ID.
	Path    string
	Window  string
	Command string
	Switch  bool
}

type SessionTarget struct {
	Name string
	ID   string
}

// SessionAttachment owns navigation admission until the foreground process
// has exited/detached, or failed to start. The executor must call Release on
// every outcome, not when the command is merely constructed.
type SessionAttachment struct {
	Command  *exec.Cmd
	Validate func() error
	Release  func()
}

// Run is the terminal executor's handoff, not command construction. Validate
// again here: a delayed foreground command must not attach to a reused raw ID
// after an external tmux server restart. This starts tmux directly, never a
// shell or a Radar re-exec process.
func (a SessionAttachment) Run() error {
	if err := a.Validate(); err != nil {
		return err
	}
	return a.Command.Run()
}

func (a SessionAttachment) SetStdin(r io.Reader) {
	if a.Command.Stdin == nil {
		a.Command.Stdin = r
	}
}
func (a SessionAttachment) SetStdout(w io.Writer) {
	if a.Command.Stdout == nil {
		a.Command.Stdout = w
	}
}
func (a SessionAttachment) SetStderr(w io.Writer) {
	if a.Command.Stderr == nil {
		a.Command.Stderr = w
	}
}

type MultiplexerProvider interface {
	Source
	ClientActive() bool
	AttachCommand(context.Context, SessionTarget) (SessionAttachment, error)
	Current(ctx context.Context) (SessionContext, bool, error)
	OpenSession(ctx context.Context, req OpenSessionRequest) (Session, error)
	Switch(ctx context.Context, target SessionTarget) error
	Target(task protocol.Task) string
	MatchesCurrent(ref protocol.SourceRef, current protocol.CurrentContext) bool
}
