package protocol

import (
	"strings"
	"testing"
)

func TestDiagnosticRecreationPreviewFiniteContractNotExecutionAuthority(t *testing.T) {
	for _, status := range []string{"recreate", "blocked", "skipped", "recreated", "eligible", "PRIVATE"} {
		for _, state := range []string{"running", "stopped", "unknown", "healthy"} {
			for _, observed := range []bool{false, true} {
				v := DiagnosticRecreationPreview{ID: "registered", PreviewID: "recreation-preview:" + strings.Repeat("a", 64), Status: status, PreviousState: state, RuntimeIdentityObserved: observed, ExecutionEligibility: "unknown_fresh_execution_guards_not_run"}
				expected := state != "healthy" && (status == "recreate" && observed && state != "unknown" || status == "blocked" && (observed || state == "unknown") || status == "skipped" && !observed && state == "unknown")
				if ValidDiagnosticRecreationPreview(v, "registered") != expected {
					t.Fatalf("inconsistent finite contract: %+v", v)
				}
				v.ExecutionEligibility = "eligible"
				if ValidDiagnosticRecreationPreview(v, "registered") {
					t.Fatal("eligibility claim accepted")
				}
			}
		}
	}
	v := DiagnosticRecreationPreview{ID: "registered", PreviewID: strings.Repeat("a", 64), Status: "recreate", PreviousState: "running", RuntimeIdentityObserved: true, ExecutionEligibility: "unknown_fresh_execution_guards_not_run"}
	if ValidDiagnosticRecreationPreview(v, "registered") {
		t.Fatal("native apply-shaped identity accepted")
	}
	v.PreviewID = "recreation-preview:" + strings.Repeat("a", 64)
	if ValidDiagnosticRecreationPreview(v, "other") {
		t.Fatal("wrong exact parent accepted")
	}
}
