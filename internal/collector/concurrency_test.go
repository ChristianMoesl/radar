package collector

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type concurrentCollectionSource struct {
	name    string
	started chan<- string
	release <-chan struct{}
}

func (s concurrentCollectionSource) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: s.name, Label: s.name}
}

func (s concurrentCollectionSource) Collect(_ context.Context, req integration.CollectRequest) integration.CollectResult {
	req.Previous[0].Metadata["collector"] = s.name
	req.Previous[0].SourceRefs[0].Metadata["collector"] = s.name
	s.started <- s.name
	<-s.release
	return integration.CollectResult{Observations: []integration.Observation{{
		Ref: protocol.SourceRef{
			ID:    s.name + ":ref",
			Kind:  "test",
			Title: req.Previous[0].Metadata["collector"] + "/" + req.Previous[0].SourceRefs[0].Metadata["collector"],
		},
	}}}
}

func TestCollectSourcesRunsConcurrentlyWithIsolatedInputsAndDeterministicOutput(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	started := make(chan string, 2)
	release := make(chan struct{})
	previous := []protocol.Task{{
		Metadata: map[string]string{"collector": "original"},
		SourceRefs: []protocol.SourceRef{{
			Metadata: map[string]string{"collector": "original"},
		}},
	}}
	sources := []integration.Source{
		concurrentCollectionSource{name: "first", started: started, release: release},
		concurrentCollectionSource{name: "second", started: started, release: release},
	}

	resultCh := make(chan Collected, 1)
	go func() {
		resultCh <- CollectSources(context.Background(), previous, logger, sources)
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("sources did not collect concurrently")
		}
	}
	close(release)

	var collected Collected
	select {
	case collected = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("source collection did not finish")
	}

	if previous[0].Metadata["collector"] != "original" || previous[0].SourceRefs[0].Metadata["collector"] != "original" {
		t.Fatalf("previous tasks were mutated: %+v", previous)
	}
	if len(collected.Observations) != 2 || collected.Observations[0].Ref.Source != "first" || collected.Observations[1].Ref.Source != "second" {
		t.Fatalf("observations are not in registration order: %+v", collected.Observations)
	}
	if collected.Observations[0].Ref.Title != "first/first" || collected.Observations[1].Ref.Title != "second/second" {
		t.Fatalf("source inputs were not isolated: %+v", collected.Observations)
	}
	if len(collected.Sources) != 2 || collected.Sources[0].Name != "first" || collected.Sources[1].Name != "second" {
		t.Fatalf("source statuses are not in registration order: %+v", collected.Sources)
	}
}
