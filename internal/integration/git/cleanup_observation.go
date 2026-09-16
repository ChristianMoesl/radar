package git

import (
	"context"
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
				} else if len(targets) == 0 && refs[i].ProvidesWorkspace {
					refs[i].CleanupIssues = append(refs[i].CleanupIssues, "matching workspace cleanup target was not found")
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

// Local collection runs every 15 seconds. Cache only its remote fetch outcome
// for two minutes, per repository; local status and branch reachability are
// checked every time. Cleanup previews and GC always use a fresh fetch.
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
	if name != "git" || len(args) != 3 || args[0] != "fetch" || args[1] != "--prune" || args[2] != "origin" {
		return r.Runner.Run(ctx, cwd, name, args...)
	}
	r.cache.mu.Lock()
	entry := r.cache.entries[cwd]
	if entry == nil {
		entry = &observationFetch{}
		r.cache.entries[cwd] = entry
	}
	r.cache.mu.Unlock()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if entry.checked.IsZero() || time.Since(entry.checked) >= 2*time.Minute {
		entry.output, entry.err = r.Runner.Run(ctx, cwd, name, args...)
		entry.checked = time.Now()
	}
	return entry.output, entry.err
}
