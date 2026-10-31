package taskservice

import (
	"context"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestResourceMutationDoesNotWaitForCollectionOrPublication(t *testing.T) {
	local := &testSource{name: "local", local: true}
	f := newFixture(t, local)
	local.ref = f.linkedRef("local", "in_progress")
	f.service.Refresh(context.Background(), true)
	if f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("fresh collection remained pending")
	}
	// Model unrelated lifecycle reconciliation inside the publication lock.
	f.service.applyMu.Lock()
	finished := make(chan protocol.Response, 1)
	go func() { f.service.MarkResourceRefreshPending(); finished <- f.service.ResourceSnapshot() }()
	select {
	case response := <-finished:
		if !response.ResourceRefreshPending {
			t.Fatal("mutation snapshot falsely fresh")
		}
	case <-time.After(time.Second):
		f.service.applyMu.Unlock()
		t.Fatal("acknowledgement waited for unrelated publication")
	}
	f.service.applyMu.Unlock()
}
func TestOldCollectionCannotClearPendingOrPublishAcrossMutation(t *testing.T) {
	local := &testSource{name: "local", local: true}
	f := newFixture(t, local)
	local.ref = f.linkedRef("local", "in_progress")
	f.service.Refresh(context.Background(), true)
	local.started, local.release = make(chan struct{}, 1), make(chan struct{})
	finished := make(chan struct{})
	go func() { defer close(finished); f.service.Refresh(context.Background(), true) }()
	<-local.started
	previousRevision := f.store.Revision()
	f.service.MarkResourceRefreshPending()
	markedRevision := f.store.Revision()
	if markedRevision <= previousRevision {
		t.Fatal("pending state did not wake revision subscribers")
	}
	close(local.release)
	<-finished
	if f.store.Revision() != markedRevision {
		t.Fatal("pre-mutation collection was published")
	}
	if !f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("stale collection cleared pending state")
	}
	f.service.Refresh(context.Background(), true)
	if f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("fresh collection did not complete pending publication")
	}
}
func TestCanceledOrPartialCollectionDoesNotClaimFreshResources(t *testing.T) {
	f := newFixture(t)
	f.service.MarkResourceRefreshPending()
	f.author.collectError = context.DeadlineExceeded
	f.service.Refresh(context.Background(), true)
	if !f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("partial collection claimed freshness")
	}
	f.author.collectError = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.service.Refresh(ctx, true)
	if !f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("canceled collection claimed freshness")
	}
	f.service.Refresh(context.Background(), true)
	if f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("healthy retry remained pending")
	}
}
func TestResourceSnapshotDoesNotPairOldProjectionWithFreshMarker(t *testing.T) {
	f := newFixture(t)
	epoch := f.service.resourceRevision.Load()
	f.service.resourcePublication.Store(&resourcePublication{epoch: epoch, storeRevision: f.store.Revision() + 1})
	if !f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("old projection carried a newer freshness marker")
	}
}
