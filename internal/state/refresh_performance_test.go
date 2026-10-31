package state

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"radar/internal/protocol"
)

// Status-only updates persist metadata without rewriting task/ref rows.
// Fixture construction, initial projection, and SQLite startup are not timed.
func BenchmarkRefreshProbeStatusPersistence(b *testing.B) {
	for _, count := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("tasks=%d", count), func(b *testing.B) {
			b.Setenv("RADAR_STATE", filepath.Join(b.TempDir(), "state.db"))
			store, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				b.Fatal(err)
			}
			tasks := make([]protocol.Task, count)
			for i := range tasks {
				id := fmt.Sprintf("local:%d", i)
				tasks[i] = protocol.Task{Title: "Synthetic task", Attention: "in_progress", SourceRefs: []protocol.SourceRef{{ID: id, Source: "local", Kind: "item", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing, CanonicalKey: id, Title: "Synthetic task"}}}
			}
			store.SetTasks(tasks)
			statuses := []protocol.SourceStatus{{Name: "local", Status: "ok"}}
			b.ReportAllocs()
			b.ResetTimer()
			iteration := 0
			for b.Loop() {
				statuses[0].Detail = fmt.Sprintf("sample-%d", iteration)
				iteration++
				store.SetSources(statuses)
			}
			b.StopTimer()
			if len(store.Tasks()) != count {
				b.Fatal("status publication lost tasks")
			}
		})
	}
}
