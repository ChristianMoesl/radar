package integration

import (
	"context"

	"radar/internal/protocol"
)

type Action struct {
	PreferredKey string
	Source       string
	Label        string
	Detail       string
	ID           string
	Ref          protocol.SourceRef
}

type ActionRequest struct {
	Task  protocol.Task
	Ref   protocol.SourceRef
	Label string
}

type RunActionRequest struct {
	Task         protocol.Task
	ActionID     string
	Ref          protocol.SourceRef
	SwitchClient bool
	Multiplexer  MultiplexerProvider
}

type ActionResult struct {
	Message string
	Refresh bool
	Quit    bool
}

type ActionProvider interface {
	Actions(ctx context.Context, req ActionRequest) []Action
	RunAction(ctx context.Context, req RunActionRequest) (ActionResult, error)
}
