package collector

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type timedReconciler struct{ fakeSource }

func (timedReconciler) Reconcile(context.Context, integration.ReconcileRequest) []integration.Observation {
	return []integration.Observation{{Ref: protocol.SourceRef{ID: "remote:done", Kind: "work_item"}}}
}

func TestCollectionLogsPerSourceAndReconciliationDurations(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	Collect(context.Background(), nil, logger, []integration.Source{timedReconciler{fakeSource{name: "remote"}}})

	output := logs.String()
	for _, want := range []string{
		`msg="source collection finished"`,
		`source=remote`,
		`status_duration=`,
		`collect_duration=`,
		`msg="source reconciliation finished"`,
		`duration=`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("logs missing %q:\n%s", want, output)
		}
	}
}
