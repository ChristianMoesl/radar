package git

import (
	"context"
	"strings"
	"sync"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
)

// Reuse the real preview checks for each resource, including errors. Inspecting
// separately keeps one unreadable worktree from hiding issues on other members.
func (s Source) collectCleanupIssues(ctx context.Context, refs []protocol.SourceRef) {
	cache := s.observations
	if cache == nil {
		cache = newObservationFetchCache()
	}
	runner := observationRunner{Runner: workspace.ExecRunner{}, cache: cache}
	var wg sync.WaitGroup
	jobs := make(chan int)
	for worker := 0; worker < min(4, len(refs)); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				targets, err := previewCleanup(checkCtx, integration.CleanupPreviewRequest{Task: protocol.Task{SourceRefs: []protocol.SourceRef{refs[i]}}}, runner)
				cancel()
				refs[i].CleanupIssues = cleanup.BlockingMessages(targets)
				if err != nil {
					refs[i].CleanupIssues = append(refs[i].CleanupIssues, err.Error())
				}
			}
		}()
	}
	for i := range refs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

// Local collection runs every 15 seconds. Cache successful remote verification outcomes
// for two minutes, per repository/commit; local status and branch reachability are
// checked every time. Cleanup previews and GC always perform fresh verification.
type observationFetchCache struct {
	mu      sync.Mutex
	entries map[string]*observationFetch
}

type observationFetch struct {
	mu      sync.Mutex
	checked time.Time
	output  string
	err     error
}

func newObservationFetchCache() *observationFetchCache {
	return &observationFetchCache{entries: map[string]*observationFetch{}}
}

type observationRunner struct {
	workspace.Runner
	cache *observationFetchCache
}

func (r observationRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	fetch := name == "git" && len(args) == 3 && args[0] == "fetch" && args[1] == "--prune" && args[2] == "origin"
	mergeProof := name == "gh" && len(args) > 0 && args[0] == "api"
	if !fetch && !mergeProof {
		return r.Runner.Run(ctx, cwd, name, args...)
	}
	key := cwd
	if mergeProof {
		key += "\x00" + strings.Join(args, "\x00")
	}
	r.cache.mu.Lock()
	entry := r.cache.entries[key]
	if entry == nil {
		entry = &observationFetch{}
		r.cache.entries[key] = entry
	}
	r.cache.mu.Unlock()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if entry.checked.IsZero() || time.Since(entry.checked) >= 2*time.Minute {
		entry.output, entry.err = r.Runner.Run(ctx, cwd, name, args...)
		if entry.err == nil {
			entry.checked = time.Now()
		} else {
			// Verification retries must run a real command after a failure.
			entry.checked = time.Time{}
		}
	}
	return entry.output, entry.err
}
