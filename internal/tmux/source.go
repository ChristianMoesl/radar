package tmux

import (
	"context"
	"fmt"
	"log/slog"

	"radar/internal/ingestion"
	"radar/internal/protocol"
)

type Source struct{}

func NewSource() Source {
	return Source{}
}

func (Source) Name() string {
	return "tmux"
}

func (Source) Local() bool {
	return true
}

func (Source) Status(ctx context.Context, logger *slog.Logger) ingestion.StatusResult {
	status := SourceStatus(ctx)
	return ingestion.StatusResult{Status: status, CanRun: status.Status == "ok"}
}

func (Source) Ingest(ctx context.Context, req ingestion.Request) ingestion.Result {
	sourceRefs, status := FetchSessions(ctx, req.Logger)
	if status.Status == "error" {
		req.Logger.Warn("tmux session collection failed", "detail", status.Detail)
		return ingestion.Result{SourceRefs: sourceRefs}
	}
	return ingestion.Result{SourceRefs: sourceRefs, Complete: status.Status == "ok"}
}

func (Source) PreviewDelete(ctx context.Context, req ingestion.DeletePreviewRequest) (protocol.DeletePreview, bool, error) {
	_ = ctx
	for _, ref := range req.Task.SourceRefs {
		if ref.Source != "tmux" || ref.Kind != "session" {
			continue
		}
		if !req.Current.Empty() && !SessionRefMatchesCurrent(ref, req.Current) {
			continue
		}
		target := SessionTarget(protocol.Task{Kind: "session", SourceRefs: []protocol.SourceRef{ref}})
		if target == "" {
			return protocol.DeletePreview{}, true, fmt.Errorf("tmux session has no delete target")
		}
		return protocol.DeletePreview{
			TaskID:      req.Task.ID,
			SourceRefID: ref.ID,
			Source:      "tmux",
			Kind:        "session",
			Title:       ref.Title,
			Path:        ref.Path,
			SessionName: target,
			SessionOnly: true,
		}, true, nil
	}
	return protocol.DeletePreview{}, false, nil
}

func (Source) Delete(ctx context.Context, preview protocol.DeletePreview) (protocol.DeleteResult, error) {
	deleted, err := DeleteSession(ctx, preview.SessionName)
	if err != nil {
		return protocol.DeleteResult{}, err
	}
	deleted.SourceRefID = preview.SourceRefID
	deleted.Title = preview.Title
	return deleted, nil
}
