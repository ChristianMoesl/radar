package server

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/collector"
	"radar/internal/integration"
	"radar/internal/protocol"
)

// Arm only after fixture registration/prior preview, so cancellation occurs
// inside this request's target discovery rather than NewRegistry or its setup.
type refreshAdmissionSource struct {
	refreshFixtureSource
	onDescriptor func()
}

func (s *refreshAdmissionSource) Descriptor() integration.Descriptor {
	if s.onDescriptor != nil {
		s.onDescriptor()
	}
	return s.refreshFixtureSource.Descriptor()
}

func TestDiagnosticRefreshAdmissionRejectsCancellation(t *testing.T) {
	for _, phase := range []string{"preview_targets", "preview_publication", "apply_targets"} {
		for _, signal := range []string{"cancelled", "deadline"} {
			priors := []bool{false, true}
			if phase == "apply_targets" {
				priors = []bool{true}
			}
			for _, prior := range priors {
				name := phase + "/" + signal + "/empty"
				if prior {
					name = phase + "/" + signal + "/prior_plan"
				}
				t.Run(name, func(t *testing.T) {
					source := &refreshAdmissionSource{refreshFixtureSource: refreshFixtureSource{name: "one"}}
					s, store := refreshFixture(t, source)
					request := refreshRequest("preview", "")
					if prior {
						preview := mustRefreshPreview(t, s)
						if phase == "apply_targets" {
							request = refreshRequest("apply", preview.PlanID)
						}
					}
					old := s.refreshPlan
					var oldView protocol.DiagnosticMaintenance
					if old != nil {
						oldView = copyRefreshView(old.view)
					}
					revision := store.Revision()
					var adapterCalls atomic.Int32
					s.SetDiagnosticRefresh(func(context.Context) (collector.Result, error) {
						adapterCalls.Add(1)
						return collector.Result{}, nil
					})
					ctx, cancel := context.WithCancel(context.Background())
					if signal == "deadline" {
						cancel()
						ctx, cancel = context.WithTimeout(context.Background(), time.Second)
					}
					defer cancel()
					descriptorCalls, publicationCalls := 0, 0
					interrupt := func() {
						if ctx.Err() != nil {
							t.Fatal("fixture cancelled before reaching callback")
						}
						if signal == "cancelled" {
							cancel()
						} else {
							<-ctx.Done()
						}
					}
					source.onDescriptor = func() {
						descriptorCalls++
						if phase != "preview_publication" {
							interrupt()
						}
					}
					if phase == "preview_publication" {
						s.SetResourcePublication(nil, func() protocol.Response {
							publicationCalls++
							interrupt()
							return protocol.Response{} // Fresh publication does not undo cancellation.
						})
					}
					view, err := s.maintainDiagnostics(ctx, request)
					if ctx.Err() == nil || descriptorCalls != 1 || (phase == "preview_publication" && publicationCalls != 1) {
						t.Fatal("fixture missed intended admission callback", ctx.Err(), descriptorCalls, publicationCalls)
					}
					if signal == "deadline" && ctx.Err() != context.DeadlineExceeded {
						t.Fatal("fixture did not observe real deadline expiry")
					}
					if err == nil || view.PlanID != "" || view.OperationID != "" {
						t.Error("cancelled admission returned accepted preview/apply", view.Status, view.PlanID, view.OperationID, err)
					}
					if s.refreshPlan != old || (old != nil && (old.consumed || !reflect.DeepEqual(old.view, oldView))) {
						t.Error("cancelled admission superseded or consumed its prior plan")
					}
					if adapterCalls.Load() != 0 || source.calls.Load() != 0 {
						t.Error("cancelled admission invoked native adapter or provider", adapterCalls.Load(), source.calls.Load())
					}
					_, _, operations, _ := store.OperationInspectionSnapshot()
					if len(operations) != 0 || store.Revision() != revision {
						t.Error("cancelled admission created an operation or changed retained state")
					}
					if !s.maintenanceMu.TryLock() {
						t.Fatal("rejected admission leaked maintenance mutex")
					}
					s.maintenanceMu.Unlock()
				})
			}
		}
	}
}

func TestDiagnosticRefreshAdmissionLiveControls(t *testing.T) {
	for _, mode := range []string{"preview_empty", "preview_prior", "apply"} {
		t.Run(mode, func(t *testing.T) {
			source := &refreshAdmissionSource{refreshFixtureSource: refreshFixtureSource{name: "one"}}
			s, store := refreshFixture(t, source)
			request := refreshRequest("preview", "")
			oldID := ""
			if mode != "preview_empty" {
				oldID = mustRefreshPreview(t, s).PlanID
				if mode == "apply" {
					request = refreshRequest("apply", oldID)
				}
			}
			calls := 0
			source.onDescriptor = func() { calls++ }
			view, err := s.maintainDiagnostics(context.Background(), request)
			if err != nil || !hexPlanID(view.PlanID) || calls < 1 {
				t.Fatal("live admission failed", view, err, calls)
			}
			_, _, operations, _ := store.OperationInspectionSnapshot()
			if mode == "apply" {
				if source.calls.Load() != 1 || len(operations) != 1 || view.Status != "returned" || view.Counts.Complete != 1 || !s.refreshPlan.consumed || view.PlanID != oldID {
					t.Fatal("live exact apply no longer uses native one-use collection", view, source.calls.Load())
				}
				again, err := s.maintainDiagnostics(context.Background(), request)
				if err != nil || again.OperationID != view.OperationID || source.calls.Load() != 1 {
					t.Fatal("duplicate accepted work reexecuted", again, err)
				}
			} else if source.calls.Load() != 0 || len(operations) != 0 || view.Status != "preview" || view.PlanID == oldID || s.refreshPlan.consumed {
				t.Fatal("live preview no longer replaces latest plan without collection", view, source.calls.Load())
			}
		})
	}
}
