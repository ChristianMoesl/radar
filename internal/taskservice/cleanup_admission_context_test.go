package taskservice

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/protocol"
)

// Simulate cancellation immediately after the admission helper reads Err but
// before its successful TryLock. The guard must check again while owning it.
type cancelOnGuardAdmission struct {
	context.Context
	cancel context.CancelFunc
}

func (c cancelOnGuardAdmission) Err() error {
	err := c.Context.Err()
	c.cancel()
	return err
}

func TestCleanupGuardCancellationBoundsAuthoredAdmission(t *testing.T) {
	for _, mode := range []string{"pre-cancelled", "cancel-at-acquisition", "cancelled-wait", "deadline-wait"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
			records, refs, revision := f.store.InspectionSnapshot()
			collections := f.author.calls.Load()
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "deadline-wait" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
			}
			defer cancel()
			if mode == "cancel-at-acquisition" {
				ctx = cancelOnGuardAdmission{Context: ctx, cancel: cancel}
			}
			locked := mode == "cancelled-wait" || mode == "deadline-wait"
			if locked {
				f.service.applyMu.Lock()
			} else if mode == "pre-cancelled" {
				cancel()
			}
			// Always release and collect the call, including on unchanged-production
			// failure. An admission regression must not leave a blocked goroutine.
			var executions atomic.Int32
			started, returned := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				returned <- f.service.GuardCleanup(ctx, done, func() error {
					executions.Add(1)
					return nil
				})
			}()
			<-started
			if mode == "cancelled-wait" {
				cancel()
			}
			<-ctx.Done()
			var err error
			waitedForOwner := false
			select {
			case err = <-returned:
			case <-time.After(time.Second):
				waitedForOwner = true
			}
			if locked {
				if f.service.applyMu.TryLock() {
					f.service.applyMu.Unlock()
					t.Error("cancelled waiter released another operation's admission")
				}
				f.service.applyMu.Unlock()
			}
			if waitedForOwner {
				select {
				case err = <-returned:
				case <-time.After(3 * time.Second):
					t.Fatal("released admission call did not exit")
				}
				t.Error("cancelled cleanup waited for unrelated lifecycle admission")
			}
			if !errors.Is(err, ctx.Err()) || executions.Load() != 0 || f.author.calls.Load() != collections {
				t.Errorf("cancelled admission reached author or execution: err=%v collections=%d want=%d executions=%d", err, f.author.calls.Load(), collections, executions.Load())
			}
			afterRecords, afterRefs, afterRevision := f.store.InspectionSnapshot()
			if revision != afterRevision || !reflect.DeepEqual(records, afterRecords) || !reflect.DeepEqual(refs, afterRefs) {
				t.Error("cancelled admission published state")
			}
			// No delayed work survives cancellation. A new independently requested
			// guard must still refresh the author, execute once and release admission.
			freshCollections := f.author.calls.Load()
			if err := f.service.GuardCleanup(context.Background(), done, func() error { executions.Add(1); return nil }); err != nil || executions.Load() != 1 || f.author.calls.Load() != freshCollections+1 {
				t.Fatalf("fresh native guard changed: err=%v collections=%d executions=%d", err, f.author.calls.Load(), executions.Load())
			}
			if !f.service.applyMu.TryLock() {
				t.Fatal("guard leaked lifecycle admission")
			}
			f.service.applyMu.Unlock()
		})
	}
}

func TestCleanupGuardContextAdmissionPreservesFreshLifecycleChecks(t *testing.T) {
	for _, change := range []string{"unchanged", "reopened", "unavailable", "callback-error"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
			ref, _ := authoredRef(done, "obsidian")
			collections := f.author.calls.Load()
			f.service.applyMu.Lock()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var executions atomic.Int32
			callbackErr := errors.New("native callback failed after admission")
			started, returned := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				returned <- f.service.GuardCleanup(ctx, done, func() error {
					executions.Add(1)
					if f.service.applyMu.TryLock() {
						f.service.applyMu.Unlock()
						t.Error("callback lost lifecycle serialization")
					}
					if change == "callback-error" {
						return callbackErr
					}
					return nil
				})
			}()
			<-started
			select {
			case err := <-returned:
				f.service.applyMu.Unlock()
				t.Fatalf("guard bypassed held lifecycle admission: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			if executions.Load() != 0 || f.author.calls.Load() != collections {
				t.Error("waiting guard performed checks before admission")
			}
			// The held mutex orders these changes before the admitted reader.
			// No collection snapshot is published to rescue a stale completion.
			switch change {
			case "reopened":
				if _, err := f.author.Source.SetLifecycle(context.Background(), ref, "open", nil); err != nil {
					f.service.applyMu.Unlock()
					<-returned
					t.Fatal(err)
				}
			case "unavailable":
				f.author.collectError = errors.New("selected author unavailable")
			}
			f.service.applyMu.Unlock()
			var err error
			select {
			case err = <-returned:
			case <-time.After(6 * time.Second):
				t.Fatal("native lifecycle guard did not return")
			}
			allowed := change == "unchanged" || change == "callback-error"
			if (executions.Load() == 1) != allowed || f.author.calls.Load() != collections+1 || (err == nil) != (change == "unchanged") || (change == "callback-error" && !errors.Is(err, callbackErr)) {
				t.Fatalf("fresh native checks changed: change=%s err=%v collections=%d executions=%d", change, err, f.author.calls.Load(), executions.Load())
			}
			if !f.service.applyMu.TryLock() {
				t.Fatal("success or refusal leaked lifecycle admission")
			}
			f.service.applyMu.Unlock()
		})
	}
}
