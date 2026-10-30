package workspace

import (
	"context"
	"errors"
	"testing"
)

func TestVerificationPhaseFetchesOnceButChecksEveryBranch(t *testing.T) {
	runner := &mergeProofRunner{published: true}
	phase := NewBranchVerificationPhase(runner)
	for _, branch := range []string{"feature-one", "feature-two"} {
		if safe, err := phase.PublishedOrMerged(context.Background(), "/repo", branch); err != nil || !safe {
			t.Fatalf("%s: %v %v", branch, safe, err)
		}
	}
	if runner.fetchCalls != 1 {
		t.Fatalf("fetches = %d", runner.fetchCalls)
	}
	// Successful fetch reuse is not cached publication proof. A changed local tip
	// must run reachability/merge checks again, even during this phase.
	runner.published = false
	runner.proof = "[]"
	if safe, err := phase.PublishedOrMerged(context.Background(), "/repo", "feature-one"); safe || err != nil {
		t.Fatalf("stale proof reused: %v %v", safe, err)
	}
	fresh := NewBranchVerificationPhase(runner)
	if _, err := fresh.PublishedOrMerged(context.Background(), "/repo", "feature-one"); err != nil {
		t.Fatal(err)
	}
	if runner.fetchCalls != 2 {
		t.Fatalf("phase reused old fetch: %d", runner.fetchCalls)
	}
	if _, err := phase.PublishedOrMerged(context.Background(), "/another-repo", "feature"); err != nil {
		t.Fatal(err)
	}
	if runner.fetchCalls != 3 {
		t.Fatal("different repository skipped its fetch")
	}
}
func TestVerificationPhaseNeverCachesFailuresOrIgnoresCancellation(t *testing.T) {
	runner := &mergeProofRunner{published: true, fetchFailures: 2}
	phase := NewBranchVerificationPhase(runner)
	if safe, err := phase.PublishedOrMerged(context.Background(), "/repo", "feature"); safe || err == nil {
		t.Fatalf("fetch failure accepted: %v %v", safe, err)
	}
	if safe, err := phase.PublishedOrMerged(context.Background(), "/repo", "feature"); !safe || err != nil {
		t.Fatalf("fresh retry skipped: %v %v", safe, err)
	}
	if runner.fetchCalls != 3 {
		t.Fatalf("fetches = %d", runner.fetchCalls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if safe, err := phase.PublishedOrMerged(ctx, "/repo", "feature"); safe || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v %v", safe, err)
	}
}
