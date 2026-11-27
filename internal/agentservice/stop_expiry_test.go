package agentservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNativeStopPlanExpiryAfterEffectsCannotReturnSuccess(t *testing.T) {
	for _, kind := range []string{"expired", "cancelled", "guard_error", "allowed"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := stopPlanFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var plan StopPlan
			guard := func(_ context.Context, execute func() error) error {
				if err := execute(); err != nil {
					return err
				}
				switch kind {
				case "expired":
					plan.capture.expires = time.Now().Add(-time.Second)
				case "cancelled":
					cancel()
				case "guard_error":
					return errors.New("private-guard-error")
				}
				return nil
			}
			var err error
			plan, err = m.PrepareStop(ctx, reg.ID, reg.Workspace, guard)
			if err != nil {
				t.Fatal(err)
			}
			err = plan.Execute(ctx)
			if (err == nil) != (kind == "allowed") || err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("expired/cancelled/failed stop returned success or leaked", err)
			}
			if err := plan.Execute(context.Background()); err == nil {
				t.Fatal("late failure restored one-use admission")
			}
			got, err := load(reg.ID)
			if err != nil || got.Desired || got.Workspace != reg.Workspace || got.Entrypoint != reg.Entrypoint {
				t.Fatal("accepted desired-state effect rolled back or registration changed", got, err)
			}
			if err := CheckWorkspaceRemoval(reg.Workspace.Path); err == nil {
				t.Fatal("late stop result released registered ownership")
			}
		})
	}
}

func TestNativeStopPlanExecutionContextKeepsEarliestDeadline(t *testing.T) {
	for _, kind := range []string{"original_expiry", "earlier_caller", "native_cap"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := stopPlanFixture(t)
			ctx := context.Background()
			want := time.Now().Add(time.Second)
			if kind == "earlier_caller" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, want)
				defer cancel()
			}
			var deadline time.Time
			plan, err := m.PrepareStop(ctx, reg.ID, reg.Workspace, func(c context.Context, execute func() error) error {
				var ok bool
				deadline, ok = c.Deadline()
				if !ok {
					t.Error("native execution has no deadline")
				}
				return execute()
			})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "original_expiry" {
				plan.capture.expires = want
			}
			start := time.Now()
			if err := plan.Execute(ctx); err != nil {
				t.Fatal(err)
			}
			if kind == "native_cap" {
				if deadline.Before(start.Add(14*time.Second)) || deadline.After(time.Now().Add(15*time.Second)) {
					t.Fatal("original native execution cap changed", deadline)
				}
			} else if !deadline.Equal(want) {
				t.Fatal("native execution extended the original/caller deadline", deadline, want)
			}
		})
	}
}

func TestNativeStopPlanOriginalExpiryInterruptsConnectedShutdown(t *testing.T) {
	reg, m := stopPlanFixture(t)
	release := make(chan struct{})
	defer close(release)
	methods := rawDiagnosticAgentPeer(t, reg, func(method string) string {
		switch method {
		case "hello":
			return `{"jsonrpc":"2.0","id":1,"result":{"protocol":1,"instanceId":"worker"}}`
		case "snapshot":
			return `{"jsonrpc":"2.0","id":1,"result":` + nativeStatusSnapshot(reg) + `}`
		default:
			<-release // No reply; the original native expiry must close the call.
			return `{"jsonrpc":"2.0","id":1,"result":{"stopping":true}}`
		}
	})
	plan, err := m.PrepareStop(context.Background(), reg.ID, reg.Workspace, allowStopGuard)
	if err != nil {
		t.Fatal(err)
	}
	plan.capture.expires = time.Now().Add(250 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- plan.Execute(ctx) }()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("expired connected shutdown returned success")
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("connected shutdown ignored the original native expiry")
	}
	if err := plan.Execute(context.Background()); err == nil {
		t.Fatal("expired shutdown restored admission")
	}
	got, err := load(reg.ID)
	if err != nil || got.Desired || got.Workspace != reg.Workspace {
		t.Fatal("expired shutdown rolled back desired state or removed registration", got, err)
	}
	shutdowns := 0
	for _, method := range methods() {
		if method == "shutdown" {
			shutdowns++
		}
	}
	if shutdowns != 1 {
		t.Fatal("fixture did not exercise exactly one connected shutdown", shutdowns)
	}
	if err := CheckWorkspaceRemoval(reg.Workspace.Path); err == nil {
		t.Fatal("expired shutdown released registered ownership")
	}
}
