package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestCollectRunsAssignedAndTitleSearchesConcurrently(t *testing.T) {
	var started atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Add(1)
		<-release
		_ = json.NewEncoder(w).Encode(searchResponse{})
	}))
	defer server.Close()
	configureJiraSource(t, server.URL, `{}`)

	resultCh := make(chan struct{}, 1)
	go func() {
		NewSource().Collect(context.Background(), jiraCollectRequest([]protocol.Task{{ID: 1, Title: "XYZ-7 rollout"}}))
		resultCh <- struct{}{}
	}()

	deadline := time.Now().Add(time.Second)
	for started.Load() < 2 {
		if time.Now().After(deadline) {
			close(release)
			t.Fatalf("started %d Jira searches, want 2 concurrently", started.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)

	select {
	case <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("Jira collection did not finish")
	}
}
