package phasetiming

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativePhaseObserverWorksWithoutLoggerAndCompletesOnce(t *testing.T) {
	var calls atomic.Int32
	ctx := WithObserver(context.Background(), func(phase string, elapsed time.Duration) {
		if phase != "cleanup_execution" || elapsed < 0 {
			t.Error("invalid sample")
		}
		calls.Add(1)
	})
	done := Start(ctx, "cleanup_execution")
	if calls.Load() != 0 {
		t.Fatal("unfinished phase published")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(done)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate completion sample")
	}
	Start(WithObserver(context.Background(), nil), "no-observer")()
}

func TestNativePhaseLoggerAndObserverReceiveTheSameCompletedInterval(t *testing.T) {
	var output bytes.Buffer
	var observed time.Duration
	ctx := WithLogger(WithObserver(context.Background(), func(_ string, elapsed time.Duration) { observed = elapsed }), slog.New(slog.NewJSONHandler(&output, nil)))
	Start(ctx, "cleanup_execution")()
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["duration"] != float64(observed) || entry["phase"] != "cleanup_execution" {
		t.Fatalf("different native interval: %+v %v", entry, observed)
	}
	if len(entry) != 5 {
		t.Fatal("phase observer added raw payload/log fields")
	}
}
