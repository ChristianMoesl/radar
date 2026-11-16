package state

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"testing"

	"radar/internal/protocol"
)

func TestSourceInspectionSnapshotIsAtomicAndDoesNotShareProviderPayloads(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	store, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	publish := func(marker string) {
		item := makeTask("in_progress", "work", testJiraIssueRef("jira:issue:ABC-123", "Example"))
		if item.SourceRefs[0].Metadata == nil {
			item.SourceRefs[0].Metadata = map[string]string{}
		}
		item.SourceRefs[0].Metadata["snapshot_marker"] = marker
		store.SetCollection([]protocol.Task{item}, nil, []protocol.SourceStatus{{Name: "jira", Status: "ok", Detail: marker, SourceRefCount: 1}})
	}
	publish("initial")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			publish(fmt.Sprint(i))
		}
	}()
	inspections := 0
	for {
		refs, statuses, revision := store.SourceInspectionSnapshot()
		if len(refs) != 1 || len(statuses) != 1 || revision <= 0 || refs[0].Snapshot.Metadata["snapshot_marker"] != statuses[0].Detail {
			t.Fatal("cached status and retained reference came from different publications")
		}
		// Mutating either returned snapshot must not alter production state or the
		// next reader; source metadata may contain nested private provider values.
		refs[0].Snapshot.Metadata["snapshot_marker"] = "caller changed clone"
		statuses[0].Detail = "caller changed clone"
		inspections++
		select {
		case <-done:
			if inspections == 0 {
				t.Fatal("no atomic reads")
			}
			return
		default:
			runtime.Gosched()
		}
	}
}
