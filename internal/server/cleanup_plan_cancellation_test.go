package server

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"radar/internal/protocol"
)

// Capture Err before signalling: cancellation after the signal cannot change
// the first successful check, so the fixture deterministically reaches the
// subsequent plan-admission boundary with a now-cancelled context.
type observedPlanContext struct {
	context.Context
	once    sync.Once
	checked chan struct{}
}

func (c *observedPlanContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestCleanupPlanIssuanceRejectsCancellationDuringAdmission(t *testing.T) {
	for _, phase := range []string{"mutex", "publication_callback", "deadline"} {
		for _, prior := range []bool{false, true} {
			name := phase + "/empty"
			if prior {
				name = phase + "/prior_plan"
			}
			t.Run(name, func(t *testing.T) {
				s, provider, resource := cleanupPlanFixture(t)
				preview := protocol.CleanupPreview{TaskID: 1, TaskTitle: "private-task", Targets: provider.targets}
				var old protocol.CleanupPreview
				if prior {
					old = requireCleanupPlan(t, s)
				}
				revision := s.store.Revision()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if phase == "deadline" {
					deadlineCtx, stop := context.WithTimeout(context.Background(), time.Second)
					defer stop()
					ctx = deadlineCtx
				}
				observed := &observedPlanContext{Context: ctx, checked: make(chan struct{})}
				if phase == "publication_callback" {
					s.SetResourcePublication(nil, func() protocol.Response {
						cancel()
						return protocol.Response{} // Fresh publication does not undo cancellation.
					})
				}
				type outcome struct {
					preview protocol.CleanupPreview
					err     error
				}
				returned := make(chan outcome, 1)
				func() {
					// Neither cancellation mode relies on sleeps or goroutine stacks:
					// keep admission closed until the first successful Err check.
					s.cleanupPlanMu.Lock()
					defer s.cleanupPlanMu.Unlock()
					go func() {
						view, err := s.issueCleanupPlan(observed, preview, revision)
						returned <- outcome{view, err}
					}()
					select {
					case <-observed.checked:
					case <-time.After(5 * time.Second):
						t.Fatal("plan did not reach initial context check")
					}
					if phase == "mutex" {
						cancel()
					} else if phase == "deadline" {
						select {
						case <-ctx.Done():
						case <-time.After(5 * time.Second):
							t.Fatal("fixture deadline did not expire")
						}
					}
				}()
				var result outcome
				select {
				case result = <-returned:
				case <-time.After(5 * time.Second):
					t.Fatal("plan admission did not return")
				}
				if result.err == nil || !errors.Is(result.err, ctx.Err()) || result.preview.PlanID != "" || len(result.preview.Targets) != 0 {
					t.Fatalf("cancelled admission issued a native plan: view=%+v err=%v context=%v", result.preview, result.err, ctx.Err())
				}
				wantPreviews := int32(0)
				if prior {
					wantPreviews = 1
				}
				if provider.previews.Load() != wantPreviews || provider.admissions.Load() != 0 || provider.checks.Load() != 0 || provider.calls.Load() != 0 || s.store.Revision() != revision {
					t.Fatal("issuance cancellation invoked execution or mutated task state")
				}
				if data, err := os.ReadFile(resource); err != nil || string(data) != "intact" {
					t.Fatal("cancelled issuance changed the fixture resource", err)
				}
				wantPlans := 0
				if prior {
					wantPlans = 1
					if s.cleanupPlans[1].id != old.PlanID {
						t.Fatal("cancelled issuance superseded the prior successful plan")
					}
				}
				if len(s.cleanupPlans) != wantPlans {
					t.Fatal("cancelled issuance retained an executable plan")
				}
				// Rejection of this cancelled preview is not cancellation of an
				// independently issued earlier plan or permission to execute it.
				// Existing successful-issuance and one-use execution tests remain
				// responsible for that separate native contract.
			})
		}
	}
}

func TestCleanupPlanIssuanceKeepsLiveAdmissionAvailable(t *testing.T) {
	for _, callback := range []bool{false, true} {
		name := "mutex"
		if callback {
			name = "publication_callback"
		}
		t.Run(name, func(t *testing.T) {
			s, provider, _ := cleanupPlanFixture(t)
			old := requireCleanupPlan(t, s)
			if callback {
				s.SetResourcePublication(nil, func() protocol.Response { return protocol.Response{} })
			}
			ctx := &observedPlanContext{Context: context.Background(), checked: make(chan struct{})}
			view, err := s.issueCleanupPlan(ctx, old, s.store.Revision())
			if err != nil || !hexPlanID(view.PlanID) || view.PlanID == old.PlanID || s.cleanupPlans[1].id != view.PlanID || len(s.cleanupPlans) != 1 || provider.calls.Load() != 0 {
				t.Fatal("live issuance was disabled or failed to replace its prior plan", view, err)
			}
		})
	}
}
