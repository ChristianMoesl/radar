package integration

import "context"

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

type EnsureSessionRequest struct {
	Name          string
	Path          string
	FirstWindow   string
	FirstCommand  string
	Switch        bool
	ForkPiSession string
}

type OpenWindowRequest struct {
	SessionName string
	Name        string
	Path        string
	Command     string
}

type SessionTarget struct {
	Name string
	ID   string
}

type MultiplexerProvider interface {
	Source
	Current(ctx context.Context) (SessionContext, bool, error)
	EnsureSession(ctx context.Context, req EnsureSessionRequest) (Session, error)
	OpenWindow(ctx context.Context, req OpenWindowRequest) error
	Switch(ctx context.Context, target SessionTarget) error
	DeleteSession(ctx context.Context, target SessionTarget) error
}
