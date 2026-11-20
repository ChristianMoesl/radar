package workspacewatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fsnotify/fsnotify"

	"radar/internal/localrefresh"
)

type fakeWatch struct {
	mu      sync.Mutex
	eventCh chan fsnotify.Event
	errorCh chan error
	paths   map[string]bool
	addErr  error
	onAdd   func(string)
	closed  bool
}

func newFakeWatch() *fakeWatch {
	return &fakeWatch{eventCh: make(chan fsnotify.Event, 100), errorCh: make(chan error, 10), paths: map[string]bool{}}
}
func (w *fakeWatch) events() <-chan fsnotify.Event { return w.eventCh }
func (w *fakeWatch) errors() <-chan error          { return w.errorCh }
func (w *fakeWatch) Add(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.addErr != nil {
		return w.addErr
	}
	w.paths[path] = true
	if w.onAdd != nil {
		w.onAdd(path)
	}
	return nil
}
func (w *fakeWatch) Remove(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.paths, path)
	return nil
}
func (w *fakeWatch) Close() error { w.mu.Lock(); defer w.mu.Unlock(); w.closed = true; return nil }
func (w *fakeWatch) WatchList() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var paths []string
	for path := range w.paths {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

func (w *fakeWatch) SetAddError(err error) { w.mu.Lock(); defer w.mu.Unlock(); w.addErr = err }
func (w *fakeWatch) Drop(path string)      { w.mu.Lock(); defer w.mu.Unlock(); delete(w.paths, path) }
func (w *fakeWatch) Closed() bool          { w.mu.Lock(); defer w.mu.Unlock(); return w.closed }

type errorLog struct {
	mu     sync.Mutex
	values []error
}

func (l *errorLog) Add(err error) { l.mu.Lock(); defer l.mu.Unlock(); l.values = append(l.values, err) }
func (l *errorLog) Snapshot() []error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.values)
}

type timeLog struct {
	mu     sync.Mutex
	values []time.Time
}

func (l *timeLog) Add(now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.values = append(l.values, now)
	return len(l.values)
}
func (l *timeLog) Snapshot() []time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.values)
}

func TestWatchBurstUsesBoundedSchedulerAndRetainsDirtyFollowup(t *testing.T) {
	root := t.TempDir()
	anchor := filepath.Join(root, "ABC-123")
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		w := newFakeWatch()
		var starts timeLog
		release := make(chan struct{})
		request, refreshDone := localrefresh.Start(ctx, func() {
			if starts.Add(time.Now()) == 2 {
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		})
		defer func() { cancel(); <-refreshDone }()
		done := make(chan struct{})
		defer func() { cancel(); <-done }()
		go func() {
			defer close(done)
			run(ctx, root, func() []string { return []string{anchor} }, request, func(err error) {
				if err != nil {
					t.Error(err)
				}
			}, func() (backend, error) { return w, nil })
		}()
		synctest.Wait()
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		if len(starts.Snapshot()) != 1 {
			t.Fatal("post-arming collection was not coalesced")
		}
		first := time.Now()
		for range 30 {
			for range 100 {
				w.eventCh <- fsnotify.Event{Name: anchor, Op: fsnotify.Rename}
			}
			synctest.Wait()
			time.Sleep(10 * time.Millisecond)
		}
		synctest.Wait()
		if len(starts.Snapshot()) != 2 || starts.Snapshot()[1].Sub(first) != 100*time.Millisecond || starts.Snapshot()[1].Sub(first) > 500*time.Millisecond {
			t.Fatalf("continuous burst violated bounded deferral: %v", starts.Snapshot())
		}
		time.Sleep(time.Second)
		if len(starts.Snapshot()) != 2 {
			t.Fatal("per-event or overlapping collectors")
		}
		close(release)
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		if len(starts.Snapshot()) != 3 {
			t.Fatalf("dirty follow-up lost or unbounded: %d", len(starts.Snapshot()))
		}
		cancel()
		<-done
		<-refreshDone
	})
}

func TestWatchFiltersOnlyRegisteredDirectAnchorStructure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		w := newFakeWatch()
		var known atomic.Value
		known.Store([]string{filepath.Join(root, "ABC-123")})
		var requests atomic.Int32
		done := make(chan struct{})
		defer func() { cancel(); <-done }()
		go func() {
			defer close(done)
			run(ctx, root, func() []string { return known.Load().([]string) }, func() { requests.Add(1) }, func(err error) {
				if err != nil {
					t.Error(err)
				}
			}, func() (backend, error) { return w, nil })
		}()
		synctest.Wait()
		if paths := w.WatchList(); !reflect.DeepEqual(paths, []string{filepath.Dir(root), root}) {
			t.Fatalf("nonrecursive coverage = %v", paths)
		}
		requests.Store(0)
		for _, path := range []string{filepath.Join(root, "unregistered"), filepath.Join(root, "ABC-123", "member"), filepath.Join(root, "state.db-wal"), filepath.Join(filepath.Dir(root), "other")} {
			w.eventCh <- fsnotify.Event{Name: path, Op: fsnotify.Create | fsnotify.Remove}
		}
		w.eventCh <- fsnotify.Event{Name: known.Load().([]string)[0], Op: fsnotify.Write}
		synctest.Wait()
		if requests.Load() != 0 {
			t.Fatalf("uncovered content/paths requested reads: %d", requests.Load())
		}
		for _, op := range []fsnotify.Op{fsnotify.Create, fsnotify.Remove, fsnotify.Rename, fsnotify.Chmod} {
			w.eventCh <- fsnotify.Event{Name: known.Load().([]string)[0], Op: op}
		}
		synctest.Wait()
		if requests.Load() != 4 {
			t.Fatalf("structural invalidations=%d", requests.Load())
		}
		known.Store([]string{filepath.Join(root, "ABC-456")})
		w.eventCh <- fsnotify.Event{Name: filepath.Join(root, "ABC-123"), Op: fsnotify.Create}
		w.eventCh <- fsnotify.Event{Name: known.Load().([]string)[0], Op: fsnotify.Create}
		synctest.Wait()
		if requests.Load() != 5 {
			t.Fatalf("registration snapshot changes not followed: %d", requests.Load())
		}
		cancel()
		<-done
		if !w.Closed() {
			t.Fatal("backend not closed at shutdown")
		}
	})
}

func TestWatchRecoversOverflowAndClosedChannels(t *testing.T) {
	for _, failure := range []string{"overflow", "events-closed", "errors-closed"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				first, second := newFakeWatch(), newFakeWatch()
				var opens, requests atomic.Int32
				var health errorLog
				done := make(chan struct{})
				defer func() { cancel(); <-done }()
				go func() {
					defer close(done)
					run(ctx, root, func() []string { return nil }, func() { requests.Add(1) }, func(err error) { health.Add(err) }, func() (backend, error) {
						opens.Add(1)
						if opens.Load() == 1 {
							return first, nil
						}
						return second, nil
					})
				}()
				synctest.Wait()
				before := requests.Load()
				switch failure {
				case "overflow":
					first.errorCh <- fsnotify.ErrEventOverflow
				case "events-closed":
					close(first.eventCh)
				case "errors-closed":
					close(first.errorCh)
				}
				synctest.Wait()
				if opens.Load() != 2 || !first.Closed() || requests.Load() <= before || len(second.WatchList()) != 2 {
					t.Fatalf("recovery: opens=%d closed=%v requests=%d paths=%v", opens.Load(), first.Closed(), requests.Load(), second.WatchList())
				}
				if len(health.Snapshot()) != 3 || health.Snapshot()[1] == nil || health.Snapshot()[2] != nil {
					t.Fatalf("untruthful health transitions: %v", health.Snapshot())
				}
				cancel()
				<-done
			})
		})
	}
}

func TestWatchUnavailableAndDroppedRegistrationRecoverOnMaintenance(t *testing.T) {
	for _, failure := range []string{"open", "add", "dropped"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				w := newFakeWatch()
				var requests, opens atomic.Int32
				var health errorLog
				if failure == "add" {
					w.SetAddError(errors.New("watch registration unavailable"))
				}
				done := make(chan struct{})
				defer func() { cancel(); <-done }()
				go func() {
					defer close(done)
					run(ctx, root, func() []string { return nil }, func() { requests.Add(1) }, func(err error) { health.Add(err) }, func() (backend, error) {
						opens.Add(1)
						if failure == "open" && opens.Load() == 1 {
							return nil, errors.New("unsupported/unavailable watcher")
						}
						return w, nil
					})
				}()
				synctest.Wait()
				if requests.Load() == 0 {
					t.Fatal("unavailable watch did not request authoritative recovery")
				}
				if failure != "dropped" && health.Snapshot()[len(health.Snapshot())-1] == nil {
					t.Fatal("unavailable watcher reported healthy")
				}
				before := requests.Load()
				w.SetAddError(nil)
				if failure == "dropped" {
					w.Drop(root)
				}
				time.Sleep(maintenanceInterval)
				synctest.Wait()
				if len(w.WatchList()) != 2 || requests.Load() <= before || health.Snapshot()[len(health.Snapshot())-1] != nil {
					t.Fatalf("maintenance failed: paths=%v requests=%d health=%v", w.WatchList(), requests.Load(), health.Snapshot())
				}
				before = requests.Load()
				time.Sleep(maintenanceInterval)
				synctest.Wait()
				if requests.Load() != before {
					t.Fatal("healthy coverage maintenance became source polling")
				}
				cancel()
				<-done
			})
		})
	}
}

func TestWatchClosesDirectoryCreationGapBetweenStatAndAdd(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	root := filepath.Join(parent, "workspaces")
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		w := newFakeWatch()
		// No kernel event is available: all descendant creation happens just
		// before the ancestor watch becomes usable, after coverage's Stat.
		w.onAdd = func(path string) {
			if path == base {
				if err := os.MkdirAll(root, 0o700); err != nil {
					t.Error(err)
				}
			}
		}
		var requests atomic.Int32
		done := make(chan struct{})
		defer func() { cancel(); <-done }()
		go func() {
			defer close(done)
			run(ctx, root, func() []string { return nil }, func() { requests.Add(1) }, func(err error) {
				if err != nil {
					t.Error(err)
				}
			}, func() (backend, error) { return w, nil })
		}()
		synctest.Wait()
		if !reflect.DeepEqual(w.WatchList(), []string{base}) {
			t.Fatal("fixture did not retain the actual installation gap")
		}
		before := requests.Load()
		time.Sleep(rearmWindow)
		synctest.Wait()
		if !reflect.DeepEqual(w.WatchList(), []string{parent, root}) || requests.Load() <= before {
			t.Fatalf("rearm gap survived until periodic maintenance: paths=%v requests=%d", w.WatchList(), requests.Load())
		}
		before = requests.Load()
		time.Sleep(5 * rearmWindow)
		synctest.Wait()
		if requests.Load() != before {
			t.Fatal("post-arming coverage check became source polling")
		}
		cancel()
		<-done
	})
}

func TestWatchRearmsReplacedRootAndMissingParentsWithoutCreatingThem(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	root := filepath.Join(parent, "workspaces")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		w := newFakeWatch()
		var requests atomic.Int32
		done := make(chan struct{})
		defer func() { cancel(); <-done }()
		go func() {
			defer close(done)
			run(ctx, root, func() []string { return nil }, func() { requests.Add(1) }, func(err error) {
				if err != nil {
					t.Error(err)
				}
			}, func() (backend, error) { return w, nil })
		}()
		synctest.Wait()
		if err := os.Rename(root, root+"-old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		before := requests.Load()
		// No event is delivered: maintenance must compare physical identity.
		time.Sleep(maintenanceInterval)
		synctest.Wait()
		if requests.Load() <= before {
			t.Fatal("same-named replacement did not invalidate")
		}
		if err := os.Rename(parent, parent+"-old"); err != nil {
			t.Fatal(err)
		}
		w.eventCh <- fsnotify.Event{Name: parent, Op: fsnotify.Rename}
		synctest.Wait()
		if !reflect.DeepEqual(w.WatchList(), []string{base}) {
			t.Fatalf("missing ancestor coverage = %v", w.WatchList())
		}
		if _, err := os.Stat(parent); !os.IsNotExist(err) {
			t.Fatal("watcher recreated a missing parent")
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		w.eventCh <- fsnotify.Event{Name: parent, Op: fsnotify.Create}
		synctest.Wait()
		if !reflect.DeepEqual(w.WatchList(), []string{parent, root}) {
			t.Fatalf("recreated coverage = %v", w.WatchList())
		}
		cancel()
		<-done
	})
}
