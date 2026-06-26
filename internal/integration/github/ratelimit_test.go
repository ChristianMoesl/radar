package github

import (
	"testing"
	"time"
)

func TestHasBudgetPausesWhenRemainingAtMinimum(t *testing.T) {
	resetRateStateForTest(t)
	reset := time.Now().Add(time.Hour).Unix()

	if hasBudget("graphql", rateLimitResource{Limit: 5000, Remaining: minGraphQLRemaining, Reset: reset}, minGraphQLRemaining, testLogger()) {
		t.Fatalf("hasBudget() = true, want false when remaining equals minimum")
	}

	if !pausedUntil("graphql", testLogger()) {
		t.Fatalf("graphql budget was not paused")
	}
}

func TestReserveDoesNotGoBelowZero(t *testing.T) {
	resetRateStateForTest(t)
	rateState.response.Resources.Core.Remaining = 1
	rateState.response.Resources.Search.Remaining = 1
	rateState.response.Resources.GraphQL.Remaining = 5

	reserve("core", 10)
	reserve("search", 10)
	reserve("graphql", 10)

	if rateState.response.Resources.Core.Remaining != 0 {
		t.Fatalf("core remaining = %d, want 0", rateState.response.Resources.Core.Remaining)
	}
	if rateState.response.Resources.Search.Remaining != 0 {
		t.Fatalf("search remaining = %d, want 0", rateState.response.Resources.Search.Remaining)
	}
	if rateState.response.Resources.GraphQL.Remaining != 0 {
		t.Fatalf("graphql remaining = %d, want 0", rateState.response.Resources.GraphQL.Remaining)
	}
}

func resetRateStateForTest(t *testing.T) {
	t.Helper()
	rateState.mu.Lock()
	previous := rateState
	rateState.fetched = time.Time{}
	rateState.response = rateLimitResponse{}
	rateState.coreUntil = time.Time{}
	rateState.searchUntil = time.Time{}
	rateState.graphqlUntil = time.Time{}
	rateState.mu.Unlock()

	t.Cleanup(func() {
		rateState.mu.Lock()
		rateState.fetched = previous.fetched
		rateState.response = previous.response
		rateState.coreUntil = previous.coreUntil
		rateState.searchUntil = previous.searchUntil
		rateState.graphqlUntil = previous.graphqlUntil
		rateState.mu.Unlock()
	})
}
