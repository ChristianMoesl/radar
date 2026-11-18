package tmux

import (
	"context"
	"fmt"
	"strings"

	"radar/internal/integration"
	"radar/internal/integration/tmux/sessionlease"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
)

type cleanupKey struct{ ref, resource, title string }

type cleanupSession struct {
	request  sessionlease.Request
	identity sessionlease.Identity
}

type admittedCleanup struct {
	Source
	runner   workspace.Runner
	leases   *sessionlease.Set
	sessions map[cleanupKey]cleanupSession
}

func targetKey(target protocol.CleanupTarget) cleanupKey {
	return cleanupKey{target.SourceRefID, target.ResourceID, target.Title}
}

func (Source) AdmitCleanup(ctx context.Context, targets []protocol.CleanupTarget, mode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	return admitCleanup(ctx, workspace.ExecRunner{}, targets, mode)
}

func admitCleanup(ctx context.Context, runner workspace.Runner, targets []protocol.CleanupTarget, mode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	bound := &admittedCleanup{runner: runner, leases: sessionlease.New(true), sessions: make(map[cleanupKey]cleanupSession)}
	for _, target := range targets {
		if strings.TrimSpace(target.ResourceID) == "" {
			bound.leases.Close()
			return nil, nil, fmt.Errorf("tmux session is required")
		}
		selector, name := target.ResourceID, target.Title
		expected := target.SourceRefID
		if !strings.HasPrefix(expected, "tmux:session:") {
			expected = ""
		}
		if mode == integration.CleanupRetire {
			name, expected = target.ResourceID, target.Operation["runtime_id"]
			selector = "=" + name
		}
		// Cleanup names are exact, never prefix/glob targets. Collected numeric
		// IDs retain the reusable name so an absent runtime cannot authorize a
		// replacement now using the same registered name.
		if !strings.HasPrefix(selector, "$") {
			selector = "=" + strings.TrimPrefix(selector, "=")
			if name == "" {
				name = strings.TrimPrefix(selector, "=")
			}
		}
		request := sessionlease.Request{Selector: selector, Name: name, Expected: expected, AllowMissing: name != ""}
		identity, err := bound.leases.Admit(ctx, runner, request)
		if err != nil {
			bound.leases.Close()
			return nil, nil, err
		}
		bound.sessions[targetKey(target)] = cleanupSession{request: request, identity: identity}
	}
	return bound, bound.leases.Close, nil
}

func (b *admittedCleanup) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	session, ok := b.sessions[targetKey(req.Target)]
	if !ok {
		return req.Target, fmt.Errorf("tmux target was not admitted for cleanup")
	}
	verify := func() (sessionlease.Identity, error) {
		request := session.request
		if session.identity.ID != "" {
			request.Selector, request.Expected = session.identity.ID, session.identity.Key()
		}
		current, err := b.leases.Admit(ctx, b.runner, request)
		if err == nil && session.identity.ID == "" && current.ID != "" {
			err = fmt.Errorf("tmux runtime appeared after cleanup admission; refresh and retry")
		}
		return current, err
	}
	if req.Mode == integration.CleanupRetire {
		return retireSession(ctx, b.runner, req.Target, verify)
	}
	return cleanupSessionTarget(ctx, b.runner, req.Target, verify)
}

var _ integration.CleanupAdmissionProvider = Source{}
