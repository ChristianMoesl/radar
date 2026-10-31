package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type remoteEventSource struct{ fakeSource }

func (s remoteEventSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{Complete: true}
}
func (remoteEventSource) CollectionItems([]integration.Observation) []integration.CollectionItem {
	return []integration.CollectionItem{{}}
}

type localEventObserver struct {
	fakeSource
	collected bool
	observed  bool
	err       error
}

func (*localEventObserver) Local() bool { return true }
func (s *localEventObserver) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	s.collected = true
	return integration.CollectResult{Complete: true}
}
func (s *localEventObserver) ObserveCollection(context.Context, string, []integration.CollectionItem) error {
	s.observed = true
	return s.err
}
func TestRemoteCollectionNotifiesLocalObserversWithoutCollectingThem(t *testing.T) {
	for _, fail := range []bool{false, true} {
		observer := &localEventObserver{fakeSource: fakeSource{name: "local"}}
		if fail {
			observer.err = errors.New("event persistence failed")
		}
		result := CollectRemote(context.Background(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)), []integration.Source{observer, remoteEventSource{fakeSource{name: "remote"}}})
		if observer.collected || !observer.observed {
			t.Fatal("local observer was collected or missed remote events")
		}
		found := false
		for _, status := range result.Sources {
			if status.Name == "local" && status.Status == "error" {
				found = true
			}
		}
		if found != fail {
			t.Fatalf("observer error status=%t, want %t", found, fail)
		}
	}
}
func TestForSourceIsolatesObservationsAndKeepsEmptyCompleteness(t *testing.T) {
	result := Result{Tasks: []protocol.Task{{SourceRefs: []protocol.SourceRef{{ID: "a", Source: "local", Metadata: map[string]string{"key": "original"}}, {ID: "b", Source: "remote"}}}}, Complete: map[string]bool{"local": true}}
	fresh := result.ForSource("local")
	fresh.Tasks[0].SourceRefs[0].Metadata["key"] = "changed"
	if len(fresh.Tasks[0].SourceRefs) != 1 || result.Tasks[0].SourceRefs[0].Metadata["key"] != "original" {
		t.Fatal("local snapshot shares mutable observations")
	}
	empty := Result{Complete: map[string]bool{"local": true}}.ForSource("local")
	if len(empty.Tasks) != 0 || !empty.Complete["local"] || len(empty.SourceNames) != 1 {
		t.Fatal("empty complete snapshot lost removal scope")
	}
}
