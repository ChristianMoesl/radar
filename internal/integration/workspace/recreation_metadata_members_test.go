package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
)

type recreationMetadataCase struct {
	name, secrets, policies string
}

func ambiguousRecreationMetadataCases() []recreationMetadataCase {
	kit := `"name":"kit:sandbox-0","scope":"sandbox:sandbox-0"`
	provenance := `"provenance":{"created_via":"provisioned","source":"sandboxes"}`
	return []recreationMetadataCase{
		{"duplicate service", `{"secrets":[{"name":"PRIVATE"}],"secrets":[],"custom_secrets":[],"env_only_count":0}`, ""},
		{"escaped service", `{"secr\u0065ts":["PRIVATE"],"secrets":[],"custom_secrets":[],"env_only_count":0}`, ""},
		{"folded service", `{"Secrets":["PRIVATE"],"secrets":[],"custom_secrets":[],"env_only_count":0}`, ""},
		{"unicode folded service", `{"\u017fecrets":["PRIVATE"],"secrets":[],"custom_secrets":[],"env_only_count":0}`, ""},
		{"duplicate custom", `{"secrets":[],"custom_secrets":["PRIVATE"],"custom_secrets":[],"env_only_count":0}`, ""},
		{"duplicate count", `{"secrets":[],"custom_secrets":[],"env_only_count":1,"env_only_count":0}`, ""},
		{"folded count", `{"secrets":[],"custom_secrets":[],"Env_Only_Count":1,"env_only_count":0}`, ""},
		{"noncanonical inventory", `{"Secrets":[],"custom_secrets":[],"env_only_count":0}`, ""},
		{"duplicate rules", "", `{"rules":[{"scope":"sandbox:PRIVATE"}],"rules":[]}`},
		{"escaped rules", "", `{"rul\u0065s":[{"scope":"sandbox:PRIVATE"}],"rules":[]}`},
		{"folded rules", "", `{"Rules":[{"scope":"sandbox:PRIVATE"}],"rules":[]}`},
		{"noncanonical rules", "", `{"Rules":[]}`},
		{"duplicate scope", "", `{"rules":[{"scope":"sandbox:PRIVATE","scope":"global"}]}`},
		{"folded scope", "", `{"rules":[{"Scope":"sandbox:PRIVATE","scope":"global"}]}`},
		{"duplicate name", "", `{"rules":[{"name":"PRIVATE","name":"kit:sandbox-0","scope":"sandbox:sandbox-0","editable":false,` + provenance + `}]}`},
		{"duplicate editable", "", `{"rules":[{` + kit + `,"editable":true,"editable":false,` + provenance + `}]}`},
		{"folded editable", "", `{"rules":[{` + kit + `,"Editable":true,"editable":false,` + provenance + `}]}`},
		{"missing editable", "", `{"rules":[{` + kit + `,` + provenance + `}]}`},
		{"null editable", "", `{"rules":[{` + kit + `,"editable":null,` + provenance + `}]}`},
		{"duplicate provenance", "", `{"rules":[{` + kit + `,"editable":false,"provenance":{"created_via":"PRIVATE","source":"PRIVATE"},` + provenance + `}]}`},
		{"duplicate created via", "", `{"rules":[{` + kit + `,"editable":false,"provenance":{"created_via":"PRIVATE","created_via":"provisioned","source":"sandboxes"}}]}`},
		{"folded source", "", `{"rules":[{` + kit + `,"editable":false,"provenance":{"created_via":"provisioned","Source":"PRIVATE","source":"sandboxes"}}]}`},
		{"noncanonical provenance", "", `{"rules":[{` + kit + `,"editable":false,"provenance":{"Created_Via":"provisioned","source":"sandboxes"}}]}`},
	}
}

func TestRecreationMetadataMembersRequireUnambiguousEvidence(t *testing.T) {
	cases := ambiguousRecreationMetadataCases()
	cases = append(cases, []recreationMetadataCase{
		{"null rule", "", `{"rules":[null]}`},
		{"null provenance", "", `{"rules":[{"name":"kit:sandbox-0","scope":"sandbox:sandbox-0","editable":false,"provenance":null}]}`},
		{"trailing secret document", emptyRecreateSecrets + `{}`, ""},
		{"trailing policy document", "", `{"rules":[]}{}`},
	}...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, r := recreationFixture(t, 1)
			if tc.secrets != "" {
				r.secrets["sandbox-0"] = tc.secrets
			}
			if tc.policies != "" {
				r.policies["sandbox-0"] = tc.policies
			}
			err := validateRecreationCredentialsAndPolicies(context.Background(), r, "sandbox-0")
			if err == nil {
				t.Error("ambiguous or incomplete metadata authorized recreation")
			} else if strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("private metadata escaped", err)
			}
			if tc.secrets != "" {
				for _, c := range r.calls {
					if c.args[0] == "policy" {
						t.Error("ambiguous secrets advanced to policy reads")
					}
				}
			}
			assertNoRecreationMutation(t, r)
		})
	}
}

func TestRecreationMetadataMembersCannotAuthorizeConfirmedRemoval(t *testing.T) {
	for _, state := range []string{"running", "stopped"} {
		for _, route := range []string{"preview", "confirmed replan", "direct guard", "batch guard", "diagnostic"} {
			for _, tc := range ambiguousRecreationMetadataCases() {
				t.Run(state+"/"+route+"/"+tc.name, func(t *testing.T) {
					withWorkspaceGOOS(t, "darwin")
					req, groups, fixture := recreationOutcomeFixture(t, state, 1)
					r := fixture.base
					plan, err := PreviewRecreateSandboxes(context.Background(), r, req)
					if err != nil || len(plan.Targets) != 1 || plan.Targets[0].Status != "recreate" {
						t.Fatal("invalid clean fixture", plan, err)
					}
					before, err := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
					if err != nil {
						t.Fatal(err)
					}
					marker := filepath.Join(groups[0].Path, "retained")
					if err := os.WriteFile(marker, []byte("retained\n"), 0600); err != nil {
						t.Fatal(err)
					}
					original := r.live["sandbox-0"]
					inject := func() {
						if tc.secrets != "" {
							r.secrets["sandbox-0"] = tc.secrets
						}
						if tc.policies != "" {
							r.policies["sandbox-0"] = tc.policies
						}
					}
					checks := 0
					if route == "direct guard" || route == "batch guard" {
						r.beforeRun = func(args []string) {
							if args[0] != "secret" {
								return
							}
							checks++
							if route == "direct guard" || checks == 2 {
								if release, leaseErr := acquireWorkspaceLeases(context.Background(), groups[0]); leaseErr == nil {
									release()
									t.Fatal("metadata guard ran without retained writer admission")
								}
								inject()
							}
						}
					} else {
						inject()
					}
					r.calls = nil
					switch route {
					case "preview":
						plan, err = PreviewRecreateSandboxes(context.Background(), r, req)
						if err != nil || len(plan.Targets) != 1 || plan.Targets[0].Status != "blocked" {
							t.Error("unsafe native preview", plan, err)
						}
					case "confirmed replan":
						_, err = recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
						if err == nil || !strings.Contains(err.Error(), "plan changed") {
							t.Error("unsafe confirmed replan", err)
						}
					case "direct guard":
						err = recreateOneSandbox(context.Background(), r, nil, req.WorkspaceRoot, groups[0], plan.Targets[0], sandboxReconcilePolicy{forceRecreate: true})
						if err == nil {
							t.Error("unsafe direct removal guard")
						}
					case "batch guard":
						var result integration.SandboxRecreateResult
						result, err = recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
						if err != nil || result.OK || result.Failed != 1 || result.Recreated != 0 || len(result.Targets) != 1 || result.Targets[0].Status != "failed" {
							t.Error("unsafe batch removal guard", result, err)
						}
					case "diagnostic":
						view, viewErr := diagnosticRecreationPreview(context.Background(), r, req.WorkspaceRoot, groups[0].ID, groups[0].Path)
						if viewErr != nil || view.Status != "blocked" || view.ExecutionEligibility != "unknown_fresh_execution_guards_not_run" {
							t.Error("diagnostic departed from blocked native proposal", view, viewErr)
						}
						data, _ := json.Marshal(view)
						if strings.Contains(string(data), "PRIVATE") {
							t.Fatal("private metadata escaped projection")
						}
					}
					if !reflect.DeepEqual(original, r.live["sandbox-0"]) {
						t.Error("UNSAFE_METADATA_REMOVED_ORIGINAL=1")
					}
					assertNoRecreationMutation(t, r)
					after, readErr := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
					if readErr != nil || !reflect.DeepEqual(before, after) {
						t.Fatal("registration changed", readErr)
					}
					if data, readErr := os.ReadFile(marker); readErr != nil || string(data) != "retained\n" {
						t.Fatal("workspace data changed", readErr)
					}
					if err != nil && strings.Contains(err.Error(), "PRIVATE") {
						t.Fatal("private metadata escaped", err)
					}
					if route == "direct guard" && checks != 1 || route == "batch guard" && checks != 2 {
						t.Error("missing or replayed metadata guard", checks)
					}
					release, leaseErr := acquireWorkspaceLeases(context.Background(), groups[0])
					if leaseErr != nil {
						t.Fatal("admission not released", leaseErr)
					}
					release()
				})
			}
		}
	}
}

func TestRecreationMetadataMembersPreserveSupportedPolicies(t *testing.T) {
	for _, state := range []string{"running", "stopped"} {
		for _, policy := range []string{
			`{"rules":[]}`,
			`{"rules":[{"scope":"global"}],"extra":"PRIVATE"}`,
			`{"rules":[{"name":"kit:sandbox-0","scope":"sandbox:sandbox-0","editable":false,"provenance":{"created_via":"provisioned","source":"sandboxes"}}]}`,
			`{"rules":[{"scope":"global"},{"name":"kit:sandbox-0","scope":"sandbox:sandbox-0","editable":false,"provenance":{"created_via":"provisioned","source":"sandboxes","extra":"PRIVATE"},"extra":"PRIVATE"}]}`,
		} {
			t.Run(fmt.Sprintf("%s/%d", state, len(policy)), func(t *testing.T) {
				req, _, fixture := recreationOutcomeFixture(t, state, 1)
				r := fixture.base
				r.policies["sandbox-0"] = policy
				r.secrets["sandbox-0"] = `{"secrets":[],"custom_secrets":[],"env_only_count":0,"extra":"PRIVATE"}`
				plan, err := PreviewRecreateSandboxes(context.Background(), r, req)
				if err != nil || len(plan.Targets) != 1 || plan.Targets[0].Status != "recreate" {
					t.Fatal("supported metadata blocked", plan, err)
				}
				req.ExpectedPlanID = plan.PlanID
				result, err := recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
				if err != nil || !result.OK || result.Recreated != 1 || r.live["sandbox-0"].Status != state {
					t.Fatal("supported recreation changed", result, err)
				}
			})
		}
	}
}

func TestRecreationMetadataMembersPreserveBatchContinuation(t *testing.T) {
	for _, state := range []string{"running", "stopped"} {
		cases := ambiguousRecreationMetadataCases()
		for _, index := range []int{0, 5, 12, 17} {
			tc := cases[index]
			t.Run(state+"/"+tc.name, func(t *testing.T) {
				req, groups, fixture := recreationOutcomeFixture(t, state, 2)
				r := fixture.base
				checks := 0
				r.beforeRun = func(args []string) {
					if args[0] == "secret" && args[3] == "sandbox-0" {
						checks++
						if checks == 2 {
							if tc.secrets != "" {
								r.secrets["sandbox-0"] = tc.secrets
							}
							if tc.policies != "" {
								r.policies["sandbox-0"] = tc.policies
							}
						}
					}
				}
				result, err := recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
				if err != nil || result.OK || result.Failed != 1 || result.Recreated != 1 || len(result.Targets) != 2 || result.Targets[0].Status != "failed" || result.Targets[1].Status != "recreated" {
					t.Fatal("batch continuation changed", result, err)
				}
				if checks != 2 || r.live["sandbox-0"].ID != "original-sandbox-0" || r.live["sandbox-1"].ID != "new-sandbox-1" {
					t.Fatal("wrong runtime retained/recreated or metadata replayed", checks, r.live)
				}
				for _, c := range r.calls {
					if c.args[0] == "rm" && c.args[2] == "original-sandbox-0" {
						t.Fatal("unsafe target removed")
					}
				}
				data, _ := json.Marshal(result)
				if strings.Contains(string(data), "PRIVATE") {
					t.Fatal("private metadata escaped batch outcome")
				}
				for _, group := range groups {
					release, err := acquireWorkspaceLeases(context.Background(), group)
					if err != nil {
						t.Fatal("admission not released", err)
					}
					release()
				}
			})
		}
	}
}

func TestRecreationMetadataObjectBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"empty object", `{}`, true},
		{"canonical with unknown metadata", `{"rules":[],"other":{"rules":"PRIVATE"}}`, true},
		{"escaped canonical", `{"rul\u0065s":[]}`, true},
		{"whitespace", " \n { \"rules\": [] } \t ", true},
		{"array", `[]`, false}, {"null", `null`, false},
		{"number", `123`, false}, {"string", `"PRIVATE"`, false},
		{"missing value", `{"rules":}`, false},
		{"unterminated object", `{"rules":[]`, false},
		{"unterminated value", `{"rules":[`, false},
		{"trailing document", `{} {}`, false},
		{"trailing malformed", `{} PRIVATE`, false},
		{"repeated unknown", `{"other":1,"other":2}`, false},
		{"folded canonical", `{"Rules":[]}`, false},
		{"duplicate escaped", `{"rul\u0065s":[],"rules":[]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if actual := unambiguousRecreationMetadataObject([]byte(tc.raw), "rules"); actual != tc.valid {
				t.Fatalf("object admission=%t want=%t", actual, tc.valid)
			}
		})
	}
}
