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

func TestRecreationSecretMetadataRequiresExplicitEmptyEvidence(t *testing.T) {
	cases := []struct {
		name, metadata string
		allowed        bool
	}{
		{"empty", emptyRecreateSecrets, true},
		{"whitespace empty", `{"secrets": [ ], "custom_secrets": [ ], "env_only_count": 0}`, true},
		{"extra metadata", `{"secrets":[],"custom_secrets":[],"env_only_count":0,"other":"PRIVATE"}`, true},
		{"null service", `{"secrets":null,"custom_secrets":[],"env_only_count":0}`, false},
		{"null custom", `{"secrets":[],"custom_secrets":null,"env_only_count":0}`, false},
		{"both null", `{"secrets":null,"custom_secrets":null,"env_only_count":0}`, false},
		{"negative cancels service", `{"secrets":[{"name":"PRIVATE"}],"custom_secrets":[],"env_only_count":-1}`, false},
		{"negative cancels custom", `{"secrets":[],"custom_secrets":[{"name":"PRIVATE"}],"env_only_count":-1}`, false},
		{"negative cancels mixed", `{"secrets":[{}],"custom_secrets":[{}],"env_only_count":-2}`, false},
		{"negative empty", `{"secrets":[],"custom_secrets":[],"env_only_count":-1}`, false},
		{"minimum integer", `{"secrets":[],"custom_secrets":[],"env_only_count":-9223372036854775808}`, false},
		{"maximum integer", `{"secrets":[{}],"custom_secrets":[],"env_only_count":9223372036854775807}`, false},
		{"service entry", `{"secrets":[null],"custom_secrets":[],"env_only_count":0}`, false},
		{"custom entry", `{"secrets":[],"custom_secrets":[{}],"env_only_count":0}`, false},
		{"env-only entry", `{"secrets":[],"custom_secrets":[],"env_only_count":1}`, false},
		{"missing service", `{"custom_secrets":[],"env_only_count":0}`, false},
		{"missing custom", `{"secrets":[],"env_only_count":0}`, false},
		{"null count", `{"secrets":[],"custom_secrets":[],"env_only_count":null}`, false},
		{"fractional count", `{"secrets":[],"custom_secrets":[],"env_only_count":0.5}`, false},
		{"object service", `{"secrets":{},"custom_secrets":[],"env_only_count":0}`, false},
		{"string custom", `{"secrets":[],"custom_secrets":"PRIVATE","env_only_count":0}`, false},
		{"malformed", `PRIVATE`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, r := recreationFixture(t, 1)
			r.secrets["sandbox-0"] = tc.metadata
			err := validateRecreationCredentialsAndPolicies(context.Background(), r, "sandbox-0")
			if (err == nil) != tc.allowed {
				t.Errorf("metadata allowed=%t want=%t: %v", err == nil, tc.allowed, err)
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("private metadata escaped")
			}
			policies := 0
			for _, c := range r.calls {
				if c.args[0] == "policy" {
					policies++
				}
			}
			if policies != 0 && !tc.allowed {
				t.Error("unsafe secret metadata advanced to policy inspection")
			}
			assertNoRecreationMutation(t, r)
		})
	}
}

func TestRecreationUnsafeSecretMetadataCannotAuthorizeRemoval(t *testing.T) {
	for _, state := range []string{"running", "stopped"} {
		for _, route := range []string{"preview", "confirmed replan", "direct removal guard", "batch removal guard"} {
			for _, shape := range []string{"negative service", "negative custom", "null service", "null custom"} {
				t.Run(state+"/"+route+"/"+shape, func(t *testing.T) {
					req, groups, fixture := recreationOutcomeFixture(t, state, 1)
					r := fixture.base
					plan, err := PreviewRecreateSandboxes(context.Background(), r, req)
					if err != nil {
						t.Fatal(err)
					}
					unsafe := map[string]string{
						"negative service": `{"secrets":[{"name":"PRIVATE"}],"custom_secrets":[],"env_only_count":-1}`,
						"negative custom":  `{"secrets":[],"custom_secrets":[{"name":"PRIVATE"}],"env_only_count":-1}`,
						"null service":     `{"secrets":null,"custom_secrets":[],"env_only_count":0}`,
						"null custom":      `{"secrets":[],"custom_secrets":null,"env_only_count":0}`,
					}[shape]
					marker := filepath.Join(groups[0].Path, "retained")
					if err := os.WriteFile(marker, []byte("retained\n"), 0600); err != nil {
						t.Fatal(err)
					}
					before, err := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
					if err != nil {
						t.Fatal(err)
					}
					original := r.live["sandbox-0"]
					reads := 0
					r.beforeRun = func(args []string) {
						if args[0] != "secret" {
							return
						}
						reads++
						if route == "direct removal guard" || (route == "batch removal guard" && reads == 2) {
							if release, err := acquireWorkspaceLeases(context.Background(), groups[0]); err == nil {
								release()
								t.Fatal("secret recheck ran outside retained admission")
							}
							r.secrets["sandbox-0"] = unsafe
						}
					}
					if route == "preview" || route == "confirmed replan" {
						r.secrets["sandbox-0"] = unsafe
					}
					r.calls = nil
					switch route {
					case "preview":
						plan, err = PreviewRecreateSandboxes(context.Background(), r, req)
						if err != nil || len(plan.Targets) != 1 || plan.Targets[0].Status != "blocked" {
							t.Errorf("unsafe preview not blocked: %+v, %v", plan, err)
						}
					case "confirmed replan":
						_, err = recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
						if err == nil || !strings.Contains(err.Error(), "plan changed") {
							t.Errorf("unsafe confirmed replan accepted: %v", err)
						}
					case "direct removal guard":
						err = recreateOneSandbox(context.Background(), r, nil, req.WorkspaceRoot, groups[0], plan.Targets[0], sandboxReconcilePolicy{forceRecreate: true})
						if err == nil {
							t.Error("unsafe pre-removal metadata accepted")
						}
					case "batch removal guard":
						var result integration.SandboxRecreateResult
						result, err = recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
						if err != nil || result.OK || result.Recreated != 0 || result.Failed != 1 || len(result.Targets) != 1 || result.Targets[0].Status != "failed" {
							t.Errorf("unsafe pre-removal metadata not failed: %+v, %v", result, err)
						}
					}
					if runtime := r.live["sandbox-0"]; runtime.ID != original.ID {
						t.Errorf("ORIGINAL_SECRET_GUARD_BYPASS=1 runtime=%s", runtime.ID)
					}
					if err != nil && strings.Contains(err.Error(), "PRIVATE") {
						t.Fatal("private metadata escaped")
					}
					if !reflect.DeepEqual(r.live["sandbox-0"], original) {
						t.Error("original runtime changed")
					}
					if route == "batch removal guard" && reads != 2 || route != "batch removal guard" && reads != 1 {
						t.Errorf("secret check replay or missing guard: %d", reads)
					}
					after, readErr := os.ReadFile(workspacegroup.Path(req.WorkspaceRoot))
					if readErr != nil || !reflect.DeepEqual(before, after) {
						t.Fatal("registration changed", readErr)
					}
					data, readErr := os.ReadFile(marker)
					if readErr != nil || string(data) != "retained\n" {
						t.Fatal("workspace files changed", readErr)
					}
					release, leaseErr := acquireWorkspaceLeases(context.Background(), groups[0])
					if leaseErr != nil {
						t.Fatal("admission not released", leaseErr)
					}
					release()
					assertNoRecreationMutation(t, r)
				})
			}
		}
	}
}

func TestRecreationUnsafeSecretMetadataPreservesBatchContinuation(t *testing.T) {
	for _, state := range []string{"running", "stopped"} {
		for _, metadata := range []string{
			`{"secrets":[{}],"custom_secrets":[],"env_only_count":-1}`,
			`{"secrets":[],"custom_secrets":null,"env_only_count":0}`,
		} {
			t.Run(fmt.Sprintf("%s/%d", state, len(metadata)), func(t *testing.T) {
				req, groups, fixture := recreationOutcomeFixture(t, state, 2)
				r := fixture.base
				reads := 0
				r.beforeRun = func(args []string) {
					if args[0] == "secret" && args[3] == "sandbox-0" {
						reads++
						if reads == 2 {
							r.secrets["sandbox-0"] = metadata
						}
					}
				}
				result, err := recreateSandboxes(context.Background(), r, nil, req, sandboxReconcilePolicy{})
				if err != nil || result.OK || result.Failed != 1 || result.Recreated != 1 || len(result.Targets) != 2 || result.Targets[0].Status != "failed" || result.Targets[1].Status != "recreated" {
					t.Fatalf("batch continuation changed: %+v, %v", result, err)
				}
				if r.live["sandbox-0"].ID != "original-sandbox-0" || r.live["sandbox-1"].ID != "new-sandbox-1" {
					t.Fatal("wrong runtime retained/recreated", r.live)
				}
				for _, c := range r.calls {
					if c.args[0] == "rm" && c.args[2] == "original-sandbox-0" {
						t.Fatal("unsafe target removed")
					}
				}
				if b, err := json.Marshal(result); err != nil || strings.Contains(string(b), "PRIVATE") {
					t.Fatal("unsafe public result", err)
				}
				for _, group := range groups {
					release, err := acquireWorkspaceLeases(context.Background(), group)
					if err != nil {
						t.Fatal("admission retained after result", err)
					}
					release()
				}
			})
		}
	}
}
