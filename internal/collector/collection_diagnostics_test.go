package collector

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
)

type diagnosticAttemptSource struct {
	canRun, complete, boundComplete bool
	status                          string
	cancel                          context.CancelFunc
	calls, boundCalls               int
}

func (*diagnosticAttemptSource) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "example"}
}
func (s *diagnosticAttemptSource) Status(context.Context, *slog.Logger) integration.StatusResult {
	return integration.StatusResult{CanRun: s.canRun, Status: protocol.SourceStatus{Name: "example", Status: s.status, Detail: "private-provider-error"}}
}
func (s *diagnosticAttemptSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	s.calls++
	if s.cancel != nil {
		s.cancel()
	}
	return integration.CollectResult{Complete: s.complete}
}
func (s *diagnosticAttemptSource) ResolveBindings(context.Context, integration.BindingRequest) integration.CollectResult {
	s.boundCalls++
	return integration.CollectResult{Complete: s.boundComplete}
}

func TestCollectionAttemptNativeEnvelopeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, status, outcome                         string
		canRun, complete, boundComplete, bind, cancel bool
	}{
		{name: "complete", status: "ok", outcome: "complete", canRun: true, complete: true},
		{name: "incomplete", status: "ok", outcome: "incomplete", canRun: true},
		{name: "partial", status: "partial", outcome: "incomplete", canRun: true, complete: true},
		{name: "disabled", status: "disabled", outcome: "skipped_before_collection"},
		{name: "unavailable", status: "error", outcome: "skipped_before_collection"},
		{name: "cancelled", status: "ok", outcome: "cancelled_or_timed_out", canRun: true, complete: true, cancel: true},
		{name: "bound-failure", status: "ok", outcome: "incomplete", canRun: true, complete: true, bind: true},
		{name: "bound-success", status: "ok", outcome: "complete", canRun: true, complete: true, bind: true, boundComplete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source := &diagnosticAttemptSource{canRun: tc.canRun, complete: tc.complete, boundComplete: tc.boundComplete, status: tc.status}
			if tc.cancel {
				source.cancel = cancel
			}
			var bindings []protocol.SourceBinding
			if tc.bind {
				bindings = []protocol.SourceBinding{{Source: "example", ID: "example:ABC-123"}}
			}
			before := time.Now()
			result := collectSource(ctx, source, source.Descriptor(), nil, bindings, linking.NewMarkMatcher(nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
			after := time.Now()
			a := result.attempt
			start, e1 := time.Parse(time.RFC3339Nano, a.StartedAt)
			finish, e2 := time.Parse(time.RFC3339Nano, a.FinishedAt)
			if e1 != nil || e2 != nil || start.Before(before) || finish.After(after) || finish.Before(start) || a.Outcome != tc.outcome {
				t.Fatalf("attempt=%+v", a)
			}
			if tc.canRun {
				collected, err := time.Parse(time.RFC3339Nano, a.CollectionStartedAt)
				if err != nil || collected.Before(start) || finish.Before(collected) || source.calls != 1 {
					t.Fatalf("collection=%+v", a)
				}
			} else if a.CollectionStartedAt != "" || source.calls != 0 {
				t.Fatal("skipped status became collection")
			}
			if tc.bind && source.boundCalls != 1 {
				t.Fatal("envelope omitted native bound lookup")
			}
		})
	}
}

func TestCollectionAttemptSurvivesNativeResultConstruction(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, run := range []struct {
		name    string
		collect func(context.Context, []protocol.Task, *slog.Logger, []integration.Source) Result
	}{{"full", Collect}, {"remote", CollectRemote}} {
		t.Run(run.name, func(t *testing.T) {
			source := &diagnosticAttemptSource{canRun: true, complete: true, status: "ok"}
			result := run.collect(context.Background(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []integration.Source{source})
			if len(result.Attempts) != 1 || result.Attempts["example"].Outcome != "complete" {
				t.Fatalf("attempt lost: %+v", result)
			}
		})
	}
}

func TestCollectionAttemptExpiredPrecheckIsNotACollectionOrComplete(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	source := &diagnosticAttemptSource{status: "error"}
	result := collectSource(ctx, source, source.Descriptor(), nil, nil, linking.NewMarkMatcher(nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if result.attempt.Outcome != "cancelled_or_timed_out" || result.attempt.CollectionStartedAt != "" || source.calls != 0 {
		t.Fatalf("expired precheck=%+v", result.attempt)
	}
}
