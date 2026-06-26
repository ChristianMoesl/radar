package integration

import (
	"log/slog"

	"radar/internal/filters"
	"radar/internal/protocol"
)

type WorkSignal string

const (
	SignalInProgress WorkSignal = "in_progress"
	SignalAttention  WorkSignal = "attention"
	SignalImmediate  WorkSignal = "immediate"
	SignalDone       WorkSignal = "done"
)

type CollectRequest struct {
	Previous []protocol.Task
	Filters  filters.Config
	Logger   *slog.Logger
}

type Observation struct {
	Ref    protocol.SourceRef
	Signal WorkSignal
	Reason string
}

type CollectResult struct {
	Observations []Observation
	Complete     bool
}

func ObserveRefs(refs []protocol.SourceRef, signal WorkSignal) []Observation {
	observations := make([]Observation, 0, len(refs))
	for _, ref := range refs {
		observations = append(observations, Observation{Ref: ref, Signal: signal})
	}
	return observations
}
