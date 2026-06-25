package integration

import (
	"context"
	"log/slog"

	"radar/internal/filters"
	"radar/internal/protocol"
)

type WorkSignal string

const (
	SignalInProgress WorkSignal = "in_progress"
	SignalAttention  WorkSignal = "attention"
	SignalImmediate  WorkSignal = "immediate"
	SignalDone       WorkSignal = "done"
)

type CollectRequest struct {
	Previous []protocol.Task
	Filters  filters.Config
	Logger   *slog.Logger
}

type Observation struct {
	Ref    protocol.SourceRef
	Signal WorkSignal
	Reason string
}

type CollectResult struct {
	Observations []Observation
	Complete     bool
}

func ObserveRefs(refs []protocol.SourceRef, signal WorkSignal) []Observation {
	observations := make([]Observation, 0, len(refs))
	for _, ref := range refs {
		observations = append(observations, Observation{Ref: ref, Signal: signal})
	}
	return observations
}

type StatusResult struct {
	Status protocol.SourceStatus
	CanRun bool
}

type Source interface {
	Name() string
	Collect(ctx context.Context, req CollectRequest) CollectResult
}

type LocalSource interface {
	Local() bool
}

type StatusReporter interface {
	Status(ctx context.Context, logger *slog.Logger) StatusResult
}

type ReconcileRequest struct {
	Previous []protocol.Task
	Active   []protocol.Task
	Result   CollectResult
	Logger   *slog.Logger
}

type Reconciler interface {
	ReconcileDone(ctx context.Context, req ReconcileRequest) []protocol.Task
}

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

type DeletePreviewRequest struct {
	Task    protocol.Task
	Current protocol.CurrentContext
	Logger  *slog.Logger
}

type DeleteProvider interface {
	PreviewDelete(ctx context.Context, req DeletePreviewRequest) (protocol.DeletePreview, bool, error)
	Delete(ctx context.Context, preview protocol.DeletePreview) (protocol.DeleteResult, error)
}

type Workspace struct {
	Name        string `json:"name,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Base        string `json:"base,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Path        string `json:"path"`
	SessionName string `json:"session_name"`
	SandboxName string `json:"sandbox_name,omitempty"`
}

type CreateWorkspaceRequest struct {
	Repo            string
	Name            string
	Branch          string
	Base            string
	Path            string
	SessionName     string
	WorkspaceRoot   string
	Model           string
	Thinking        string
	SandboxTemplate string
	Switch          bool
	ForkPiSession   string
}

type DeleteWorkspaceRequest struct {
	Path        string
	SessionName string
	Force       bool
}

type WorkspaceProvider interface {
	Source
	Current(ctx context.Context, cwd string) (Workspace, bool, error)
	Create(ctx context.Context, req CreateWorkspaceRequest) (Workspace, error)
	DeleteWorkspace(ctx context.Context, req DeleteWorkspaceRequest) (Workspace, error)
}

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

type RuntimeProvider interface {
	Source
	ActionProvider
	DeleteProvider
}

type CodeReviewProvider interface {
	Source
	Reconciler
}

type WorkTracker interface {
	Source
	Reconciler
}

type Set struct {
	Sources         []Source
	Workspace       WorkspaceProvider
	Multiplexer     MultiplexerProvider
	ActionProviders []ActionProvider
	DeleteProviders []DeleteProvider
}
