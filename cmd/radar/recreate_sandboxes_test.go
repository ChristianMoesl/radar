package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"radar/internal/integration"
)

type fakeSandboxRecreator struct {
	plan    integration.SandboxRecreatePlan
	applied []integration.SandboxRecreateRequest
	failure error
}

func (f *fakeSandboxRecreator) PreviewRecreateSandboxes(context.Context, integration.SandboxRecreateRequest) (integration.SandboxRecreatePlan, error) {
	return f.plan, f.failure
}
func (f *fakeSandboxRecreator) RecreateSandboxes(_ context.Context, _ *slog.Logger, req integration.SandboxRecreateRequest) (integration.SandboxRecreateResult, error) {
	f.applied = append(f.applied, req)
	return integration.SandboxRecreateResult{OK: true, Recreated: 1, Targets: f.plan.Targets}, nil
}

func TestRecreateSandboxesCLIConfirmation(t *testing.T) {
	for _, tt := range []struct {
		name, input                    string
		preview, yes, apply, cancelled bool
	}{
		{name: "preview", preview: true},
		{name: "decline", input: "n\n", cancelled: true},
		{name: "EOF", cancelled: true},
		{name: "confirm", input: "y\n", apply: true},
		{name: "explicit yes", yes: true, apply: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeSandboxRecreator{plan: integration.SandboxRecreatePlan{PlanID: "confirmed-plan", Targets: []integration.SandboxRecreateTarget{{WorkspacePath: "/work", SandboxName: "sandbox", Status: "recreate"}}}}
			var diagnostics bytes.Buffer
			value, err := recreateSandboxesCommand(context.Background(), fake, nil, integration.SandboxRecreateRequest{WorkspaceRoot: "/workspaces", Workspace: "/work"}, tt.preview, tt.yes, strings.NewReader(tt.input), &diagnostics)
			if err != nil {
				t.Fatal(err)
			}
			if (len(fake.applied) == 1) != tt.apply {
				t.Fatalf("applied=%v", fake.applied)
			}
			if tt.apply && (fake.applied[0].ExpectedPlanID != "confirmed-plan" || fake.applied[0].Workspace != "/work") {
				t.Fatalf("request=%+v", fake.applied[0])
			}
			if tt.cancelled && !value.(integration.SandboxRecreateResult).Cancelled {
				t.Fatalf("value=%+v", value)
			}
			if !tt.preview && !tt.yes && !strings.Contains(diagnostics.String(), "files outside host mounts will be lost") {
				t.Fatalf("missing loss warning: %s", &diagnostics)
			}
			var stdout, stderr bytes.Buffer
			if err := writeResult(&stdout, &stderr, value, true); err != nil {
				t.Fatal(err)
			}
			if !json.Valid(stdout.Bytes()) || stderr.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q", &stdout, &stderr)
			}
		})
	}
}

func TestRecreateSandboxesCLIErrorsAndEmptyPlan(t *testing.T) {
	fake := &fakeSandboxRecreator{failure: errors.New("preflight failure")}
	if _, err := recreateSandboxesCommand(context.Background(), fake, nil, integration.SandboxRecreateRequest{}, false, true, strings.NewReader(""), &bytes.Buffer{}); err == nil || len(fake.applied) > 0 {
		t.Fatalf("err=%v", err)
	}
	fake = &fakeSandboxRecreator{plan: integration.SandboxRecreatePlan{PlanID: "empty", Targets: []integration.SandboxRecreateTarget{}}}
	var diagnostics bytes.Buffer
	if _, err := recreateSandboxesCommand(context.Background(), fake, nil, integration.SandboxRecreateRequest{}, false, false, strings.NewReader(""), &diagnostics); err != nil || diagnostics.Len() > 0 {
		t.Fatalf("err=%v diagnostics=%q", err, &diagnostics)
	}
}

func TestRecreateSandboxesCLIFlagsAndOutput(t *testing.T) {
	for _, args := range [][]string{
		{"recreate-sandboxes", "--preview"},
		{"recreate-sandboxes", "--preview", "--json"},
		{"--json", "recreate-sandboxes", "--yes"},
		{"recreate-sandboxes", "--preview", "--yes"},
		{"recreate-sandboxes", "extra"},
		{"recreate-sandboxes", "--typo"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, code := outputCLI(t, args, "")
			invalid := strings.Contains(strings.Join(args, " "), "--preview --yes") || args[len(args)-1] == "extra" || args[len(args)-1] == "--typo"
			if invalid {
				if code != 2 || stdout != "" || stderr == "" {
					t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
				}
				return
			}
			if code != 0 || stderr != "" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			asJSON := strings.Contains(strings.Join(args, " "), "--json")
			if json.Valid([]byte(stdout)) != asJSON {
				t.Fatalf("stdout=%q", stdout)
			}
			if !asJSON && !strings.Contains(stdout, "No Radar-managed sandboxes") {
				t.Fatalf("stdout=%q", stdout)
			}
		})
	}
}

func TestRecreateSandboxesHumanResults(t *testing.T) {
	result := integration.SandboxRecreateResult{Recreated: 1, Failed: 1, Skipped: 1, Targets: []integration.SandboxRecreateTarget{
		{SandboxName: "one", WorkspacePath: "/one", Status: "recreated"},
		{SandboxName: "two", WorkspacePath: "/two", Status: "blocked", Reason: "scoped secrets"},
		{SandboxName: "three", WorkspacePath: "/three", Status: "skipped", Reason: "absent"},
	}}
	var out, errout bytes.Buffer
	if err := writeResult(&out, &errout, result, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 recreated, 1 failed/blocked, 1 skipped", "scoped secrets", "/three"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, &out)
		}
	}
}
