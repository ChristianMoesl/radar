package phasetiming

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestTimingRecordsOnlyPhaseAndDurationOnce(t *testing.T) {
	var output bytes.Buffer
	ctx := WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)))
	done := Start(ctx, "sandbox_readiness")
	done()
	done()
	var entry map[string]any
	decoder := json.NewDecoder(&output)
	if err := decoder.Decode(&entry); err != nil {
		t.Fatal(err)
	}
	if entry["phase"] != "sandbox_readiness" || entry["duration"] == nil {
		t.Fatalf("missing timing: %v", entry)
	}
	for key := range entry {
		switch key {
		case "time", "level", "msg", "phase", "duration":
		default:
			t.Fatalf("unexpected diagnostic field: %s", key)
		}
	}
	if decoder.More() {
		t.Fatal("duplicate phase timing")
	}
	Start(context.Background(), "without_logger")()
}
