package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	previousFetched := rateState.fetched
	previousResponse := rateState.response
	previousCoreUntil := rateState.coreUntil
	previousSearchUntil := rateState.searchUntil
	previousGraphQLUntil := rateState.graphqlUntil
	rateState.fetched = time.Time{}
	rateState.response = rateLimitResponse{}
	rateState.coreUntil = time.Time{}
	rateState.searchUntil = time.Time{}
	rateState.graphqlUntil = time.Time{}
	rateState.mu.Unlock()

	t.Cleanup(func() {
		rateState.mu.Lock()
		rateState.fetched = previousFetched
		rateState.response = previousResponse
		rateState.coreUntil = previousCoreUntil
		rateState.searchUntil = previousSearchUntil
		rateState.graphqlUntil = previousGraphQLUntil
		rateState.mu.Unlock()
	})
}

func TestInstalledGitHubFailuresRemainErrors(t *testing.T) {
	for _, body := range []string{
		"exit 2", // a broken local authentication check is not missing credentials
		`if [ "$1 $2" = "auth token" ]; then printf 'fixture-token\n'; exit 0; fi
printf 'authentication rejected\n' >&2
exit 1`,
	} {
		t.Run(body, func(t *testing.T) {
			resetRateStateForTest(t)
			tmp := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", tmp)
			t.Setenv("PATH", tmp)
			if err := os.WriteFile(filepath.Join(tmp, "gh"), []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
				t.Fatal(err)
			}
			status := NewSource().Status(context.Background(), testLogger())
			if status.CanRun || status.Status.Status != "error" {
				t.Fatalf("status = %+v", status)
			}
			if strings.Contains(status.Status.Detail, "fixture-token") {
				t.Fatal("authentication token leaked into status")
			}
		})
	}
}
