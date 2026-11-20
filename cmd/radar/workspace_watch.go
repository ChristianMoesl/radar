package main

import (
	"context"
	"log/slog"

	"radar/internal/integration/workspace"
	"radar/internal/protocol"
	"radar/internal/workspacewatch"
)

func watchWorkspacePresence(ctx context.Context, snapshot func() []protocol.Task, request func(), logger *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	if collectionDisabled() {
		close(done)
		return done
	}
	root, err := workspace.DefaultRoot()
	if err != nil {
		logger.Warn("workspace presence watcher unavailable; periodic reconciliation retained", "error", err)
		request()
		close(done)
		return done
	}
	go func() {
		defer close(done)
		workspacewatch.Run(ctx, root, func() []string {
			var anchors []string
			for _, task := range snapshot() {
				for _, ref := range task.SourceRefs {
					if ref.Source == "workspace" && ref.Kind == "workspace" {
						anchors = append(anchors, ref.Path)
					}
				}
			}
			return anchors
		}, request, logger)
	}()
	return done
}
