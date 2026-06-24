package sbx

import (
	"context"
	"log/slog"

	"radar/internal/ingestion"
)

type Source struct{}

func NewSource() Source {
	return Source{}
}

func (Source) Name() string {
	return "sbx"
}

func (Source) Local() bool {
	return true
}

func (Source) Status(ctx context.Context, logger *slog.Logger) ingestion.StatusResult {
	status := SourceStatus(ctx)
	return ingestion.StatusResult{Status: status, CanRun: status.Status == "ok"}
}

func (Source) Ingest(ctx context.Context, req ingestion.Request) ingestion.Result {
	sourceRefs, status := FetchSandboxes(ctx, req.Logger)
	if status.Status == "error" {
		req.Logger.Warn("sbx sandbox collection failed", "detail", status.Detail)
		return ingestion.Result{SourceRefs: sourceRefs}
	}
	return ingestion.Result{SourceRefs: sourceRefs, Complete: status.Status == "ok"}
}

var _ ingestion.Source = Source{}
var _ ingestion.LocalSource = Source{}
var _ ingestion.StatusReporter = Source{}
