package integration

import (
	"context"

	"radar/internal/protocol"
)

type AuthenticationRequest struct {
	Operation      string
	SourceStatuses []protocol.SourceStatus
	CleanupTargets []protocol.CleanupTarget
}

type AuthenticationResult struct {
	Changed bool
}

type InteractiveAuthenticator interface {
	Integration
	EnsureAuthentication(ctx context.Context, req AuthenticationRequest) (AuthenticationResult, error)
}
