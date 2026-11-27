package diagnostics

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type cancellingRepositoryPolicyProvider struct {
	repositoryPolicyProvider
	run func(context.Context)
}

func (p cancellingRepositoryPolicyProvider) DiagnosticRepositoryPolicy(ctx context.Context, parent, member, scope string) (protocol.DiagnosticRepositoryPolicy, error) {
	p.run(ctx)
	return p.repositoryPolicyProvider.DiagnosticRepositoryPolicy(ctx, parent, member, scope)
}

func TestRepositoryPolicyCancellationWithholdsProviderDeclarations(t *testing.T) {
	for _, kind := range []string{"cancelled_at_entry", "expired_at_entry", "cancelled_in_provider", "expired_in_provider", "expired_absence_in_provider", "allowed", "allowed_absence"} {
		t.Run(kind, func(t *testing.T) {
			store, _, _ := fixture(t)
			view := repositoryPolicyBoundaryFixture()
			if kind == "expired_absence_in_provider" || kind == "allowed_absence" {
				view.Evidence = "repository_configuration_absent"
			}
			beforeView, _ := json.Marshal(view)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var requestContext context.Context = ctx
			deadline := time.Now().Add(time.Hour)
			if kind == "expired_at_entry" {
				deadline = time.Now().Add(-time.Second)
			} else if kind == "expired_in_provider" || kind == "expired_absence_in_provider" {
				deadline = time.Now().Add(20 * time.Millisecond)
			}
			if kind != "cancelled_at_entry" && kind != "cancelled_in_provider" {
				requestContext = delayedPreviewDeadline{Context: ctx, deadline: deadline}
			}
			if kind == "cancelled_at_entry" {
				cancel()
			}
			calls := 0
			provider := cancellingRepositoryPolicyProvider{repositoryPolicyProvider: repositoryPolicyProvider{calls: &calls, view: view}, run: func(context.Context) {
				if kind == "cancelled_in_provider" {
					cancel()
				}
				if kind == "expired_in_provider" || kind == "expired_absence_in_provider" {
					time.Sleep(max(time.Until(deadline), 0) + time.Millisecond)
				}
			}}
			beforeRecords, beforeRefs, revision := store.InspectionSnapshot()
			page, err := Probe(requestContext, store, protocol.DiagnosticsRequest{Section: "repository_policy", ParentID: view.WorkspaceID, ID: view.ID}, true, integration.NewRegistry(provider))
			if kind == "cancelled_at_entry" || kind == "expired_at_entry" {
				if err == nil || calls != 0 || !reflect.DeepEqual(page, protocol.DiagnosticsPage{}) {
					t.Fatal("cancelled/expired request called the declaration provider", calls, err)
				}
			} else {
				if err != nil || calls != 1 || len(page.Items) != 1 || !page.ResourceRefreshPending {
					t.Fatal("provider boundary failed or retried", page, calls, err)
				}
				got := page.Items[0].RepositoryPolicy
				if kind == "allowed" || kind == "allowed_absence" {
					if !reflect.DeepEqual(got, &view) {
						t.Fatal("own deferred cancellation discarded healthy declarations", got)
					}
				} else if got.ID != view.ID || got.WorkspaceID != view.WorkspaceID || got.Origin != view.Origin || got.Available || got.Evidence != "unavailable" || got.Reason != "cancelled_or_timed_out" || got.Settings == nil || len(got.Settings) != 0 {
					t.Fatal("late cancellation/expiry retained usable declarations", got)
				}
			}
			if (kind == "expired_at_entry" || kind == "expired_in_provider" || kind == "expired_absence_in_provider") && requestContext.Err() != nil {
				t.Fatal("fixture did not isolate absolute expiry from timer notification")
			}
			afterView, _ := json.Marshal(view)
			afterRecords, afterRefs, afterRevision := store.InspectionSnapshot()
			if string(beforeView) != string(afterView) || !reflect.DeepEqual(beforeRecords, afterRecords) || !reflect.DeepEqual(beforeRefs, afterRefs) || revision != afterRevision {
				t.Fatal("cancellation normalization mutated provider or retained state")
			}
		})
	}
}
