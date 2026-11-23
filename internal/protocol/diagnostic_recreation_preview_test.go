package protocol

import (
	"strings"
	"testing"
)

func TestDiagnosticRecreationPreviewFiniteContractNotExecutionAuthority(t *testing.T) {
	for _, status := range []string{"recreate", "blocked", "skipped", "recreated", "eligible", "PRIVATE"} {
		for _, state := range []string{"running", "stopped", "unknown", "healthy"} {
			for _, observed := range []bool{false, true} {
				v := DiagnosticRecreationPreview{ID: "registered", PreviewID: "recreation-preview:" + strings.Repeat("a", 64), Status: status, PreviousState: state, RuntimeIdentityObserved: observed, ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "unavailable", LeaseEvidence: "unavailable"}
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
	v := DiagnosticRecreationPreview{ID: "registered", PreviewID: strings.Repeat("a", 64), Status: "recreate", PreviousState: "running", RuntimeIdentityObserved: true, ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "unavailable", LeaseEvidence: "unavailable"}
	if ValidDiagnosticRecreationPreview(v, "registered") {
		t.Fatal("native apply-shaped identity accepted")
	}
	v.PreviewID = "recreation-preview:" + strings.Repeat("a", 64)
	if ValidDiagnosticRecreationPreview(v, "other") {
		t.Fatal("wrong exact parent accepted")
	}
}

func TestDiagnosticRecreationOwnershipContractRemainsObservationOnly(t *testing.T) {
	for _, lease := range []string{"unknown_no_lock_file", "exclusive_available_at_probe", "contended_at_probe", "unavailable"} {
		for _, count := range []int{-1, 0, 1, 1024} {
			v := DiagnosticRecreationPreview{ID: "registered", PreviewID: "recreation-preview:" + strings.Repeat("a", 64), Status: "recreate", PreviousState: "running", RuntimeIdentityObserved: true, ExecutionEligibility: "unknown_fresh_execution_guards_not_run", OwnershipEvidence: "fresh_bounded_registration", AgentOwnerCount: &count, LeaseEvidence: lease}
			if count == -1 {
				v.OwnershipEvidence, v.AgentOwnerCount = "unavailable", nil
			}
			if !ValidDiagnosticRecreationPreview(v, "registered") {
				t.Fatal("bounded unknown-or-observed evidence rejected", v)
			}
			for name, change := range map[string]func(*DiagnosticRecreationPreview){
				"missing ownership": func(v *DiagnosticRecreationPreview) { v.OwnershipEvidence = "" },
				"private ownership": func(v *DiagnosticRecreationPreview) { v.OwnershipEvidence = "PRIVATE" },
				"missing count": func(v *DiagnosticRecreationPreview) {
					v.OwnershipEvidence, v.AgentOwnerCount = "fresh_bounded_registration", nil
				},
				"unavailable count": func(v *DiagnosticRecreationPreview) {
					n := 0
					v.OwnershipEvidence, v.AgentOwnerCount = "unavailable", &n
				},
				"negative count": func(v *DiagnosticRecreationPreview) {
					n := -1
					v.OwnershipEvidence, v.AgentOwnerCount = "fresh_bounded_registration", &n
				},
				"oversize count": func(v *DiagnosticRecreationPreview) {
					n := 1025
					v.OwnershipEvidence, v.AgentOwnerCount = "fresh_bounded_registration", &n
				},
				"missing lease":          func(v *DiagnosticRecreationPreview) { v.LeaseEvidence = "" },
				"holder identity":        func(v *DiagnosticRecreationPreview) { v.LeaseEvidence = "PRIVATE-holder" },
				"eligibility claim":      func(v *DiagnosticRecreationPreview) { v.ExecutionEligibility = "eligible" },
				"invented owner blocker": func(v *DiagnosticRecreationPreview) { v.ExecutionEligibility = "blocked_registered_agent" },
				"invented lease blocker": func(v *DiagnosticRecreationPreview) { v.ExecutionEligibility = "blocked_lease_contention" },
			} {
				changed := v
				change(&changed)
				if ValidDiagnosticRecreationPreview(changed, "registered") {
					t.Fatal("inconsistent evidence accepted", name, changed)
				}
			}
		}
	}
}
