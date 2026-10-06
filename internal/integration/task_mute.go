package integration

import (
	"context"
	"log/slog"
	"radar/internal/linking"
	"radar/internal/protocol"
)

// TaskMuteProvider authors a durable preference, adopting source-only tasks
// without provisioning workspace resources.
type TaskMuteProvider interface {
	Source
	SetMuted(context.Context, protocol.Task, bool) (AuthoredTaskIdentity, error)
}

// TaskBindingProvider keeps explicitly adopted tasks associated with new work.
type TaskBindingProvider interface {
	Source
	ReconcileBindings(context.Context, protocol.SourceRef, protocol.Task) (*Observation, error)
}

// BoundSourceResolver resolves explicit authored tracking intent not covered by
// active discovery. Missing identities are never fabricated as active or done.
type BoundSourceResolver interface {
	Source
	ResolveBindings(context.Context, BindingRequest) CollectResult
}

type BindingRequest struct {
	Bindings     []protocol.SourceBinding
	Previous     []protocol.Task
	Result       CollectResult
	LinkingMarks linking.MarkMatcher
	Logger       *slog.Logger
}
