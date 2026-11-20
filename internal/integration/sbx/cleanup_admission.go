package sbx

import (
	"context"
	"fmt"
	"maps"
	"reflect"

	"radar/internal/integration"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
)

// Service acquires every canonical path and reusable sandbox-name deletion lease
// before provider admission. Bind only those exact targets, avoiding duplicate
// acquisition of Service's exclusive leases. Direct Cleanup does not use this
// path and must acquire the same resources itself.
type admittedSandboxCleanup struct {
	Source
	runner  workspace.Runner
	targets []protocol.CleanupTarget
	mode    integration.CleanupMode
}

func (Source) AdmitCleanup(_ context.Context, targets []protocol.CleanupTarget, mode integration.CleanupMode) (integration.CleanupProvider, func(), error) {
	return admitSandboxCleanup(workspace.ExecRunner{}, targets, mode), func() {}, nil
}

func admitSandboxCleanup(runner workspace.Runner, targets []protocol.CleanupTarget, mode integration.CleanupMode) *admittedSandboxCleanup {
	bound := &admittedSandboxCleanup{runner: runner, mode: mode}
	for _, target := range targets {
		target.Operation = maps.Clone(target.Operation)
		bound.targets = append(bound.targets, target)
	}
	return bound
}

func (b *admittedSandboxCleanup) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	for _, target := range b.targets {
		if b.mode == req.Mode && reflect.DeepEqual(target, req.Target) {
			return cleanupWithRunner(ctx, b.runner, req, true)
		}
	}
	return req.Target, fmt.Errorf("sandbox target was not admitted for cleanup")
}

var _ integration.CleanupAdmissionProvider = Source{}
