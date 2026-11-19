package workspacelease

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AcquireReferences excludes deletion of paths or their ancestors while a
// writer uses or publishes references to them. All leases are shared: unrelated
// writers and long-lived agents must not conflict at common ancestors. Actual
// resource mutation still needs its separate exclusive mutation admission.
//
// Keep both lexical ancestors and physical ancestors. A symlink below a deleted
// directory can lose its view even when its physical target is elsewhere.
func AcquireReferences(ctx context.Context, paths ...string) (func(), error) {
	identities := map[string]bool{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if path == "" {
			continue
		}
		physical, err := CanonicalPath(path)
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(path) {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			// Do not clean symlink/.. before physical resolution.
			path = cwd + string(filepath.Separator) + path
		}
		for _, view := range []string{path, physical} {
			view = strings.TrimRight(view, string(filepath.Separator))
			if view == "" {
				view = string(filepath.Separator)
			}
			for {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				identity, err := CanonicalPath(view)
				if err != nil {
					return nil, err
				}
				identities[identity] = true
				if view == string(filepath.Separator) {
					break
				}
				// Dir/Clean would erase unresolved symlink traversal in the
				// lexical view. Remove only its final path component instead.
				view = strings.TrimRight(view[:strings.LastIndexByte(view, byte(filepath.Separator))], string(filepath.Separator))
				if view == "" {
					view = string(filepath.Separator)
				}
			}
		}
	}
	ordered := make([]string, 0, len(identities))
	for identity := range identities {
		ordered = append(ordered, identity)
	}
	sort.Strings(ordered)
	var releases []func()
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, identity := range ordered {
		if err := ctx.Err(); err != nil {
			releaseAll()
			return nil, err
		}
		release, err := Acquire(identity, false)
		if err != nil {
			releaseAll()
			return nil, err
		}
		releases = append(releases, release)
	}
	if err := ctx.Err(); err != nil {
		releaseAll()
		return nil, err
	}
	return releaseAll, nil
}
