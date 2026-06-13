package github

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"radar.nvim/internal/protocol"
)

const minSearchRemaining = 2
const minCoreRemaining = 5
const minGraphQLRemaining = 100

type rateLimitResponse struct {
	Resources struct {
		Core    rateLimitResource `json:"core"`
		Search  rateLimitResource `json:"search"`
		GraphQL rateLimitResource `json:"graphql"`
	} `json:"resources"`
}

type rateLimitResource struct {
	Limit     int   `json:"limit"`
	Remaining int   `json:"remaining"`
	Reset     int64 `json:"reset"`
}

var rateState = struct {
	mu           sync.Mutex
	fetched      time.Time
	response     rateLimitResponse
	coreUntil    time.Time
	searchUntil  time.Time
	graphqlUntil time.Time
}{response: rateLimitResponse{}}

func EnsureSearchBudget(ctx context.Context, logger *slog.Logger) bool {
	if pausedUntil("search", logger) {
		return false
	}
	limits, err := rateLimits(ctx, logger)
	if err != nil {
		logger.Warn("could not fetch github rate limits; skipping github collection to be safe", "error", err)
		return false
	}
	if !hasBudget("search", limits.Resources.Search, minSearchRemaining, logger) {
		return false
	}
	reserve("search", 1)
	return true
}

func EnsureCoreBudget(ctx context.Context, logger *slog.Logger) bool {
	if pausedUntil("core", logger) {
		return false
	}
	limits, err := rateLimits(ctx, logger)
	if err != nil {
		logger.Warn("could not fetch github rate limits; skipping github collection to be safe", "error", err)
		return false
	}
	if !hasBudget("core", limits.Resources.Core, minCoreRemaining, logger) {
		return false
	}
	reserve("core", 1)
	return true
}

func EnsureGraphQLBudget(ctx context.Context, logger *slog.Logger) bool {
	_, allowed := GraphQLSourceStatus(ctx, logger)
	return allowed
}

func GraphQLSourceStatus(ctx context.Context, logger *slog.Logger) (protocol.SourceStatus, bool) {
	status := protocol.SourceStatus{Name: "github", Status: "ok"}

	if until, ok := pausedUntilTime("graphql"); ok {
		logger.Debug("github collection paused until rate limit reset", "resource", "graphql", "reset", until.Format(time.RFC3339))
		status.Status = "paused"
		status.Detail = fmt.Sprintf("rate limited until %s", until.Format(time.RFC3339))
		return status, false
	}

	limits, err := rateLimits(ctx, logger)
	if err != nil {
		logger.Warn("could not fetch github rate limits; skipping github collection to be safe", "error", err)
		status.Status = "error"
		status.Detail = err.Error()
		return status, false
	}
	if !hasBudget("graphql", limits.Resources.GraphQL, minGraphQLRemaining, logger) {
		until, _ := pausedUntilTime("graphql")
		status.Status = "paused"
		status.Detail = fmt.Sprintf("rate limited until %s", until.Format(time.RFC3339))
		return status, false
	}

	reserve("graphql", 10)
	remaining := max(0, limits.Resources.GraphQL.Remaining-10)
	status.Detail = fmt.Sprintf("graphql %d/%d remaining", remaining, limits.Resources.GraphQL.Limit)
	return status, true
}

func pausedUntil(name string, logger *slog.Logger) bool {
	until, ok := pausedUntilTime(name)
	if !ok {
		return false
	}

	logger.Debug("github collection paused until rate limit reset", "resource", name, "reset", until.Format(time.RFC3339))
	return true
}

func pausedUntilTime(name string) (time.Time, bool) {
	rateState.mu.Lock()
	defer rateState.mu.Unlock()

	var until time.Time
	switch name {
	case "core":
		until = rateState.coreUntil
	case "search":
		until = rateState.searchUntil
	case "graphql":
		until = rateState.graphqlUntil
	}
	if until.IsZero() || time.Now().After(until) {
		return time.Time{}, false
	}
	return until, true
}

func rateLimits(ctx context.Context, logger *slog.Logger) (rateLimitResponse, error) {
	rateState.mu.Lock()
	defer rateState.mu.Unlock()

	if time.Since(rateState.fetched) < 5*time.Minute {
		return rateState.response, nil
	}

	var response rateLimitResponse
	if err := ghJSON(ctx, []string{"api", "rate_limit"}, &response); err != nil {
		return response, err
	}
	rateState.response = response
	rateState.fetched = time.Now()

	logger.Debug(
		"github rate limits",
		"core_remaining", response.Resources.Core.Remaining,
		"core_limit", response.Resources.Core.Limit,
		"core_reset", resetTime(response.Resources.Core.Reset),
		"search_remaining", response.Resources.Search.Remaining,
		"search_limit", response.Resources.Search.Limit,
		"search_reset", resetTime(response.Resources.Search.Reset),
		"graphql_remaining", response.Resources.GraphQL.Remaining,
		"graphql_limit", response.Resources.GraphQL.Limit,
		"graphql_reset", resetTime(response.Resources.GraphQL.Reset),
	)
	return response, nil
}

func hasBudget(name string, resource rateLimitResource, minimum int, logger *slog.Logger) bool {
	if resource.Remaining > minimum {
		return true
	}

	until := time.Unix(resource.Reset, 0).Add(5 * time.Second)
	if resource.Reset == 0 {
		until = time.Now().Add(15 * time.Minute)
	}
	pause(name, until)

	logger.Warn(
		"github rate limit low; pausing collection until reset",
		"resource", name,
		"remaining", resource.Remaining,
		"limit", resource.Limit,
		"reset", until.Format(time.RFC3339),
	)
	return false
}

func pause(name string, until time.Time) {
	rateState.mu.Lock()
	defer rateState.mu.Unlock()

	switch name {
	case "core":
		rateState.coreUntil = until
	case "search":
		rateState.searchUntil = until
	case "graphql":
		rateState.graphqlUntil = until
	}
}

func PauseSearch(until time.Time) {
	pause("search", until)
}

func PauseCore(until time.Time) {
	pause("core", until)
}

func reserve(name string, cost int) {
	rateState.mu.Lock()
	defer rateState.mu.Unlock()

	switch name {
	case "core":
		rateState.response.Resources.Core.Remaining = max(0, rateState.response.Resources.Core.Remaining-cost)
	case "search":
		rateState.response.Resources.Search.Remaining = max(0, rateState.response.Resources.Search.Remaining-cost)
	case "graphql":
		rateState.response.Resources.GraphQL.Remaining = max(0, rateState.response.Resources.GraphQL.Remaining-cost)
	}
}

func resetTime(reset int64) string {
	if reset == 0 {
		return "unknown"
	}
	return time.Unix(reset, 0).Format(time.RFC3339)
}

func RateLimitSummary(ctx context.Context, logger *slog.Logger) (string, error) {
	limits, err := rateLimits(ctx, logger)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"github core %d/%d reset %s, search %d/%d reset %s, graphql %d/%d reset %s",
		limits.Resources.Core.Remaining,
		limits.Resources.Core.Limit,
		resetTime(limits.Resources.Core.Reset),
		limits.Resources.Search.Remaining,
		limits.Resources.Search.Limit,
		resetTime(limits.Resources.Search.Reset),
		limits.Resources.GraphQL.Remaining,
		limits.Resources.GraphQL.Limit,
		resetTime(limits.Resources.GraphQL.Reset),
	), nil
}
