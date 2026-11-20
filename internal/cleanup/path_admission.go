package cleanup

import (
	"context"
	"sort"
	"sync"

	"radar/internal/workspacelease"
)

type admittedPathsKey struct{}

type admittedPaths struct {
	mu       sync.Mutex
	paths    map[string]bool
	releases []func()
	live     bool
}

// Only Service installs this capability, after acquiring its complete initial
// bundle. Providers can retain freshly discovered actual owners through later
// providers too, without inferring ownership from a busy lock or caller target.
func withAdmittedPaths(ctx context.Context, paths map[string]bool) (context.Context, func()) {
	owned := &admittedPaths{paths: make(map[string]bool, len(paths)), live: true}
	for path, held := range paths {
		if held {
			owned.paths[path] = true
		}
	}
	return context.WithValue(ctx, admittedPathsKey{}, owned), func() {
		owned.mu.Lock()
		defer owned.mu.Unlock()
		owned.live = false
		for i := len(owned.releases) - 1; i >= 0; i-- {
			owned.releases[i]()
		}
		owned.releases = nil
	}
}

// AcquirePaths retains canonical deletion admission for actual provider-owned
// paths, including owners absent from caller targets. A live Service execution
// owns both existing and newly acquired leases until its final provider returns;
// the returned release never drops that bundle. Direct or expired contexts own
// their new leases until release. Aliases deduplicate; partial failures unwind
// only this call's acquisitions. Admission grants no deletion authority.
func AcquirePaths(ctx context.Context, paths ...string) (func(), error) {
	identities := map[string]bool{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		canonical, err := workspacelease.CanonicalPath(path)
		if err != nil {
			return nil, err
		}
		identities[canonical] = true
	}
	ordered := make([]string, 0, len(identities))
	for path := range identities {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	owned, _ := ctx.Value(admittedPathsKey{}).(*admittedPaths)
	if owned != nil {
		owned.mu.Lock()
		defer owned.mu.Unlock()
		if !owned.live {
			owned = nil
		}
	}
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, path := range ordered {
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		if owned != nil && owned.paths[path] {
			continue
		}
		unlock, err := workspacelease.Acquire(path, true)
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, unlock)
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	if owned != nil {
		for _, path := range ordered {
			owned.paths[path] = true
		}
		owned.releases = append(owned.releases, releases...)
		return func() {}, nil
	}
	return release, nil
}
