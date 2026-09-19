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

func (collectionStatusSource) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "runtime"}
}

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

type unavailableSource struct{ called *bool }

func (s unavailableSource) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "unavailable"}
}
func (s unavailableSource) Status(context.Context, *slog.Logger) integration.StatusResult {
	return integration.OptionalStatus("unavailable", new(false), "")
}
func (s unavailableSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	*s.called = true
	return integration.CollectResult{}
}
func (s unavailableSource) Reconcile(context.Context, integration.ReconcileRequest) []integration.Observation {
	*s.called = true
	return nil
}
func TestUnavailableSourceDoesNotCollectOrReconcile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	called := false
	result := Collect(context.Background(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []integration.Source{unavailableSource{&called}, collectionStatusSource{}})
	if called {
		t.Fatal("disabled integration was invoked")
	}
	if len(result.Sources) != 2 {
		t.Fatalf("sources = %+v", result.Sources)
	}
}
