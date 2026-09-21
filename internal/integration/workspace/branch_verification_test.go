package workspace

import (
	"context"
	"errors"
	"testing"
	"time"
)

type deadlineVerificationRunner struct {
	*mergeProofRunner
	deadlines []time.Time
	block     bool
}

func (r *deadlineVerificationRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return "", errors.New("verification command has no deadline")
	}
	r.deadlines = append(r.deadlines, deadline)
	if r.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return r.mergeProofRunner.Run(ctx, cwd, name, args...)
}

func TestBranchVerificationSharesDeadlineAcrossFetchAndMergeProofRetries(t *testing.T) {
	for _, shorterCallerDeadline := range []bool{false, true} {
		name := "background context"
		if shorterCallerDeadline {
			name = "shorter caller deadline"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if shorterCallerDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
			}
			r := &deadlineVerificationRunner{mergeProofRunner: &mergeProofRunner{
				proof: mergedProof(testHead, "acme/app", "2026-01-01"), mergeReachable: true,
				fetchFailures: 1, apiFailures: 1,
			}}
			start := time.Now()
			safe, err := BranchPublishedOrMerged(ctx, r, "/repo", "feature")
			if err != nil || !safe {
				t.Fatalf("safe=%v error=%v", safe, err)
			}
			if r.fetchCalls != 2 || r.apiCalls != 2 {
				t.Fatalf("expected fetch and merge-proof retries: fetch=%d api=%d", r.fetchCalls, r.apiCalls)
			}
			deadline := r.deadlines[0]
			if !deadline.After(start) || deadline.After(start.Add(branchVerificationTimeout+100*time.Millisecond)) {
				t.Fatalf("verification deadline %v is not bounded from start %v", deadline, start)
			}
			if parentDeadline, ok := ctx.Deadline(); ok && !deadline.Equal(parentDeadline) {
				t.Fatalf("shorter caller deadline was changed: got %v want %v", deadline, parentDeadline)
			}
			for i, got := range r.deadlines {
				if !got.Equal(deadline) {
					t.Fatalf("command %d restarted the verification budget: got %v want %v", i, got, deadline)
				}
			}
		})
	}
}

func TestBranchVerificationDeadlineFailsClosedWithoutRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r := &deadlineVerificationRunner{block: true}
	safe, err := BranchPublishedOrMerged(ctx, r, "/repo", "feature")
	if safe || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline must not establish publication: safe=%v error=%v", safe, err)
	}
	if len(r.deadlines) != 1 {
		t.Fatalf("retried after deadline: %d commands", len(r.deadlines))
	}
}
