package tmux

import (
	"context"
	"errors"
	"testing"
)

func TestDiagnosticRuntimeUsesNativeLifetimeAndNeverTreatsFailureAsAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, output, presence, id string
		err                        error
	}{
		{name: "present", output: "123\t456\t$1\tregistered\t1\t2\t/private-path\n", presence: "present", id: "tmux:session:123:456:$1"},
		{name: "absent", presence: "absent"},
		{name: "foreign", output: "123\t456\t$1\tforeign\t0\t1\t/private-path\n", presence: "absent"},
		{name: "failure", err: errors.New("private-provider-secret"), presence: "unknown"},
		{name: "malformed", output: "private-secret", presence: "unknown"},
		{name: "missing lifetime", output: "\t456\t$1\tregistered\t1\t2\t/private-path\n", presence: "unknown"},
		{name: "missing name", output: "123\t456\t$1\t\t1\t2\t/private-path\n", presence: "unknown"},
		{name: "duplicate", output: "123\t456\t$1\tregistered\t1\t2\t/path\n123\t456\t$2\tregistered\t1\t2\t/path\n", presence: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := diagnosticRuntime(context.Background(), "registered", tc.output, tc.err)
			if view.Presence != tc.presence || view.ID != tc.id {
				t.Fatalf("view=%+v", view)
			}
			if tc.presence == "unknown" && view.Problem != "probe_failed" {
				t.Fatalf("raw failure/false absence: %+v", view)
			}
			if tc.presence == "present" && view.State != "running" {
				t.Fatal("verified session not running")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if view := diagnosticRuntime(ctx, "registered", "", nil); view.Presence != "unknown" || view.Problem != "cancelled_or_timed_out" {
		t.Fatalf("cancelled probe=%+v", view)
	}
}
