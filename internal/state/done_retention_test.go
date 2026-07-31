package state

import (
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestProjectTasksOnlyShowsRecentlyDoneTasks(t *testing.T) {
	now := time.Now().UTC()
	state := persistedState{
		Version: stateVersion,
		Records: []TaskRecord{
			{
				ID:        "task:1",
				NumericID: 1,
				State:     "done",
				DoneAt:    now.Add(-2 * 24 * time.Hour).Format(time.RFC3339),
				Snapshot:  protocol.Task{Title: "recently done", Attention: "done"},
			},
			{
				ID:        "task:2",
				NumericID: 2,
				State:     "done",
				DoneAt:    now.Add(-4 * 24 * time.Hour).Format(time.RFC3339),
				Snapshot:  protocol.Task{Title: "old done", Attention: "done"},
			},
		},
	}

	tasks := projectTasks(state)
	if len(tasks) != 1 || tasks[0].Title != "recently done" {
		t.Fatalf("tasks = %+v, want only the task done within the last three days", tasks)
	}
}
