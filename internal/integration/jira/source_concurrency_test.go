package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestCollectRunsAssignedAndTitleSearchesConcurrently(t *testing.T) {
	var started atomic.Int32
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Add(1)
		<-release
		_ = json.NewEncoder(w).Encode(searchResponse{})
	}))
	t.Cleanup(server.Close)
	configureJiraSource(t, server.URL, `{}`)

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan struct{})
	go func() {
		defer close(resultCh)
		NewSource().Collect(ctx, jiraCollectRequest([]protocol.Task{{ID: 1, Title: "XYZ-7 rollout"}}))
	}()
	// Join before test-scoped configuration is restored, including a failed
	// concurrency assertion. A late CLI must not reach the next test's server.
	t.Cleanup(func() {
		cancel()
		unblock()
		<-resultCh
	})

	deadline := time.Now().Add(time.Second)
	for started.Load() < 2 {
		if time.Now().After(deadline) {
			unblock()
			t.Fatalf("started %d Jira searches, want 2 concurrently", started.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	unblock()

	select {
	case <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("Jira collection did not finish")
	}
}
