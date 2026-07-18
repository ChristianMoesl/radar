package collector

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type collectionStatusSource struct{}

func (collectionStatusSource) Name() string { return "runtime" }

func (collectionStatusSource) Status(context.Context, *slog.Logger) integration.StatusResult {
	return integration.StatusResult{
		Status: protocol.SourceStatus{Name: "runtime", Status: "ok", Detail: "available"},
		CanRun: true,
	}
}

func (collectionStatusSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	status := protocol.SourceStatus{Name: "runtime", Status: "error", Detail: "collection failed"}
	return integration.CollectResult{SourceStatus: &status}
}

func TestCollectSourcesUsesRuntimeSourceStatus(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collected := CollectSources(context.Background(), nil, logger, []integration.Source{collectionStatusSource{}})

	if len(collected.Sources) != 1 {
		t.Fatalf("source statuses = %+v, want one", collected.Sources)
	}
	status := collected.Sources[0]
	if status.Name != "runtime" || status.Status != "error" || status.Detail != "collection failed" {
		t.Fatalf("source status = %+v, want runtime collection error", status)
	}
}
