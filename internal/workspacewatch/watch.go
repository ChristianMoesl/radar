// Package workspacewatch invalidates registered anchor-presence observations.
// It never reads task/note contents or interprets an event as a resource fact.
package workspacewatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Coverage maintenance does not collect sources or replace the daemon's
// unchanged 15-second local / two-minute full reconciliation timers.
const (
	maintenanceInterval = 15 * time.Second
	rearmWindow         = 100 * time.Millisecond
)

type backend interface {
	Add(string) error
	Close() error
	WatchList() []string
	events() <-chan fsnotify.Event
	errors() <-chan error
}

type native struct{ *fsnotify.Watcher }

func (w native) events() <-chan fsnotify.Event { return w.Events }
func (w native) errors() <-chan error          { return w.Errors }

func openNative() (backend, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return native{w}, nil
}

// Run watches only the root and its nearest existing ancestor. known returns
// the currently registered anchor paths from the daemon's published snapshot;
// it must not perform provider I/O. request must enqueue rather than collect
// inline. Initial arming, recovery and coverage changes request a fresh read,
// closing the installation/rearm gap without asserting presence or ownership.
// Log transitions describe watch coverage, independently of source health.
func Run(ctx context.Context, root string, known func() []string, request func(), logger *slog.Logger) {
	last := ""
	health := func(err error) {
		if err != nil {
			if err.Error() != last {
				logger.Warn("workspace presence watcher degraded; periodic reconciliation retained", "error", err)
			}
			last = err.Error()
		} else if last != "" {
			logger.Info("workspace presence watch coverage restored")
			last = ""
		}
	}
	run(ctx, root, known, request, health, openNative)
}

func run(ctx context.Context, root string, known func() []string, request func(), health func(error), open func() (backend, error)) {
	root, err := filepath.Abs(root)
	if err != nil {
		health(err)
		request()
		return
	}
	var w backend
	var events <-chan fsnotify.Event
	var failures <-chan error
	watched := map[string]os.FileInfo{}
	var recheck *time.Timer
	var recheckC <-chan time.Time
	defer func() {
		if recheck != nil {
			recheck.Stop()
		}
	}()
	closeWatch := func() {
		if w != nil {
			_ = w.Close()
		}
		w, events, failures = nil, nil, nil
		watched = map[string]os.FileInfo{}
	}
	defer closeWatch()

	rearm := func() {
		if ctx.Err() != nil {
			return
		}
		if w == nil {
			w, err = open()
			if err != nil {
				health(err)
				request()
				return
			}
			events, failures = w.events(), w.errors()
		}
		desired, coverageErr := coverage(root)
		listed := map[string]bool{}
		for _, path := range w.WatchList() {
			listed[path] = true
		}
		changed := len(watched) != len(desired)
		for path, info := range watched {
			want, keep := desired[path]
			if !keep || !listed[path] || !os.SameFile(info, want) {
				changed = true
			}
		}
		if changed && len(watched) != 0 {
			// kqueue keeps internal child descriptors/seen-path bookkeeping in
			// addition to user watches. Removing and re-adding the same root
			// name can reuse that old incarnation. Replace the bounded backend
			// as a unit instead of trusting pathname-only registration reuse.
			closeWatch()
			w, err = open()
			if err != nil {
				health(err)
				request()
				return
			}
			events, failures = w.events(), w.errors()
		}
		for path, info := range desired {
			if _, exists := watched[path]; exists {
				continue
			}
			if err := w.Add(path); err != nil {
				coverageErr = errors.Join(coverageErr, fmt.Errorf("watch %s: %w", path, err))
				continue
			}
			watched[path] = info
			changed = true
		}
		health(coverageErr)
		if changed || coverageErr != nil {
			request()
		}
		if changed && coverageErr == nil && recheckC == nil {
			// MkdirAll/replacement can create the next directory between our
			// coverage stat and Add. Recheck after arming to close that gap;
			// events cannot postpone this fixed window. Healthy coverage does
			// not schedule further checks or source reads.
			recheck = time.NewTimer(rearmWindow)
			recheckC = recheck.C
		}
	}
	recoverWatch := func(err error) {
		health(err)
		request() // Includes overflow/closed channels, even if rearming fails.
		closeWatch()
		rearm()
	}

	rearm()
	request() // Even a preexisting empty root needs a post-arming observation.
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rearm()
		case <-recheckC:
			recheckC = nil
			rearm()
		case err, ok := <-failures:
			if !ok {
				err = errors.New("filesystem watcher error channel closed")
			}
			recoverWatch(err)
		case event, ok := <-events:
			if !ok {
				recoverWatch(errors.New("filesystem watcher event channel closed"))
				continue
			}
			if event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) == 0 {
				continue
			}
			path := filepath.Clean(event.Name)
			// Root/ancestor replacement is relevant even with no known anchors.
			if path == root || strings.HasPrefix(root, path+string(filepath.Separator)) {
				request()
				closeWatch()
				rearm()
				continue
			}
			if filepath.Dir(path) == root {
				for _, anchor := range known() {
					if filepath.Clean(anchor) == path {
						request()
						break
					}
				}
			}
		}
	}
}

// Missing roots/parents are normal: the nearest surviving ancestor detects
// recreation. No directories, registry locks or state files are created here.
// Identity checks also detect silent replacement or a dropped OS registration
// during maintenance, rather than mistaking a same-named path for the old watch.
func coverage(root string) (map[string]os.FileInfo, error) {
	paths := map[string]os.FileInfo{}
	var problem error
	if info, err := os.Stat(root); err == nil && info.IsDir() {
		paths[root] = info
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		problem = err
	}
	for path := filepath.Dir(root); ; path = filepath.Dir(path) {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			paths[path] = info
			return paths, problem
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			problem = errors.Join(problem, err)
		}
		if filepath.Dir(path) == path {
			return paths, errors.Join(problem, errors.New("no existing workspace ancestor to watch"))
		}
	}
}
