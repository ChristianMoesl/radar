package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/integration/terraform/settings"
	"radar/internal/protocol"
)

// Opt-in read-only qualification. The disposable CLI profile contains selectors
// only; tfctl owns authentication through the CI environment's personal token.
func TestLiveTerraformCollection(t *testing.T) {
	testLiveTerraformQualification(t, false)
}

func TestLiveTerraformFailureAcknowledgement(t *testing.T) {
	testLiveTerraformQualification(t, true)
}

func testLiveTerraformQualification(t *testing.T, expectFailure bool) {
	t.Helper()
	if os.Getenv("RADAR_TEST_TERRAFORM_LIVE") != "1" {
		t.Skip("set RADAR_TEST_TERRAFORM_LIVE=1 with explicit approved scope and tfctl authentication")
	}
	if os.Getenv("TFCTL_TOKEN") == "" {
		t.Fatal("the disposable live fixture requires TFCTL_TOKEN; configure it as a GitHub Environment secret")
	}
	host, err := normalizeHost(os.Getenv("RADAR_TEST_TERRAFORM_HOSTNAME"))
	if err != nil {
		t.Fatal("select the approved Terraform hostname explicitly")
	}
	cfg := settings.Default()
	cfg.Enabled = new(true)
	cfg.Organization = os.Getenv("RADAR_TEST_TERRAFORM_ORGANIZATION")
	cfg.Workspaces = []string{os.Getenv("RADAR_TEST_TERRAFORM_WORKSPACE")}
	runID := os.Getenv("RADAR_TEST_TERRAFORM_RUN_ID")
	if cfg.Organization == "" || cfg.Workspaces[0] == "" || settings.Validate(cfg) != nil || (runID != "" && !validID(runID, "run-")) {
		t.Fatal("live qualification requires an explicit organization/workspace and a valid optional run ID")
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	cliDir := filepath.Join(root, "tfctl")
	t.Setenv("TFCTL_CONFIG_DIR", cliDir)
	if err := os.MkdirAll(filepath.Join(cliDir, "profiles"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{
		"active_profile.hcl":   "name = \"default\"\n",
		"profiles/default.hcl": fmt.Sprintf("name = \"default\"\nhostname = %q\ndefault_organization = %q\ntelemetry = \"disabled\"\n", host, cfg.Organization),
	} {
		if err := os.WriteFile(filepath.Join(cliDir, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	radarConfig := config.Default()
	radarConfig.Terraform = cfg
	if err := config.Create(radarConfig); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	report, observations, err := qualifyTerraformLive(ctx, NewSource(), cfg, host, runID, expectFailure)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range observations {
		if contracttest.ValidateSourceRefs("terraform", []protocol.SourceRef{observation.Ref}) != nil {
			t.Fatal("live Terraform source-ref contract is invalid")
		}
	}
	// Counts and enum states only: never print account IDs, run messages, plan
	// payloads, CLI profiles, provider responses, or credentials into CI logs.
	data, _ := json.Marshal(report)
	t.Logf("live Terraform qualification: %s", data)
}

type terraformLiveReport struct {
	DiscoveredRuns   int            `json:"discovered_runs"`
	States           map[string]int `json:"states"`
	PlanSummaries    int            `json:"plan_summaries"`
	ColdBindingState string         `json:"cold_binding_state"`
	FailureReviewed  bool           `json:"failure_reviewed"`
}

func qualifyTerraformLive(ctx context.Context, source Source, cfg settings.Config, host, runID string, expectFailure bool) (terraformLiveReport, []integration.Observation, error) {
	report := terraformLiveReport{States: map[string]int{}}
	fail := func(message string) (terraformLiveReport, []integration.Observation, error) {
		return report, nil, fmt.Errorf("%s", message)
	}
	if expectFailure && runID == "" {
		return fail("failure qualification requires the exact deliberate fixture run ID")
	}
	if status := source.Status(ctx, nil); !status.CanRun {
		return fail("Terraform source unavailable; verify tfctl version and live scope")
	}
	session, err := (client{profile: cfg.Profile, output: source.output}).connect(ctx, cfg)
	if err != nil {
		return fail(err.Error())
	}
	if session.host != host {
		return fail("tfctl authenticated to a different service than the approved live hostname")
	}
	result := source.Collect(ctx, integration.CollectRequest{})
	if !result.Complete || result.SourceStatus == nil || result.SourceStatus.Status != "ok" {
		if result.SourceStatus != nil {
			return fail("live collection incomplete: " + result.SourceStatus.Detail)
		}
		return fail("live collection incomplete")
	}
	validate := func(observation integration.Observation) bool {
		ref := observation.Ref
		return ref.Source == "terraform" && ref.Kind == "run" && ref.ID == runIdentity(host, ref.Metadata["run_id"]) &&
			ref.Metadata["creator_id"] == session.userID && ref.Metadata["organization"] == cfg.Organization &&
			session.spaces[ref.Metadata["workspace_id"]] == ref.Metadata["workspace_name"] && ref.Metadata["workspace_name"] != ""
	}
	for _, observation := range result.Observations {
		if !validate(observation) {
			return fail("live observation escaped the approved personal account or workspace scope")
		}
		metadata := observation.Ref.Metadata
		report.States[metadata["run_status"]]++
		if metadata["additions"] != "" && metadata["changes"] != "" && metadata["destructions"] != "" {
			report.PlanSummaries++
		}
	}
	report.DiscoveredRuns = len(result.Observations)
	if runID == "" {
		if len(result.Observations) == 0 {
			return fail("no personally created runs were discovered; create a run as the token's user or supply a personal run ID")
		}
		runID = result.Observations[0].Ref.Metadata["run_id"]
	}
	// A fresh source with no previous tasks/result must recover the selected run
	// directly. This also qualifies older terminal fixtures outside discovery.
	cold := NewSource()
	cold.output = source.output
	binding := protocol.SourceBinding{Source: "terraform", Kind: "run", ID: runIdentity(host, runID), WorkItem: true}
	bound := cold.ResolveBindings(ctx, integration.BindingRequest{Bindings: []protocol.SourceBinding{binding}})
	if !bound.Complete || len(bound.Observations) != 1 || bound.Observations[0].Ref.ID != binding.ID || !validate(bound.Observations[0]) {
		return fail("cold binding lookup could not verify the selected personal run in the approved workspace")
	}
	observation := bound.Observations[0]
	result.Observations = append(result.Observations, observation)
	report.ColdBindingState = observation.Ref.Metadata["run_status"]
	if expectFailure && (report.ColdBindingState != "errored" || observation.Ref.Metadata["plan_only"] != "true" || observation.Ref.Acknowledgement == nil) {
		return fail("deliberate speculative fixture run did not expose an errored outcome requiring acknowledgement")
	}
	if len(cold.Actions(ctx, integration.ActionRequest{Ref: observation.Ref})) != 2 {
		return fail("selected live run did not expose both validated open actions")
	}
	if acknowledgement := observation.Ref.Acknowledgement; acknowledgement != nil {
		if observation.Signal != integration.SignalAttention {
			return fail("unacknowledged live failure did not require Attention")
		}
		// Acknowledge only in isolated Radar memory; Terraform receives GETs.
		previous := protocol.Task{AcknowledgementCursor: acknowledgement.Cursor, SourceRefs: []protocol.SourceRef{observation.Ref}}
		reviewed := cold.ResolveBindings(ctx, integration.BindingRequest{Bindings: []protocol.SourceBinding{binding}, Previous: []protocol.Task{previous}})
		if !reviewed.Complete || len(reviewed.Observations) != 1 || !validate(reviewed.Observations[0]) ||
			reviewed.Observations[0].Ref.ID != binding.ID || reviewed.Observations[0].Ref.Acknowledgement != nil ||
			reviewed.Observations[0].Signal != integration.SignalDone || reviewed.Observations[0].Ref.Metadata["run_status"] != report.ColdBindingState ||
			reviewed.Observations[0].Ref.Metadata["failure_reviewed_at"] != acknowledgement.Cursor {
			return fail("live failure acknowledgement did not confirm Done while retaining the failed outcome")
		}
		report.FailureReviewed = true
		result.Observations = append(result.Observations, reviewed.Observations[0])
	}
	return report, result.Observations, nil
}

func TestTerraformLiveQualificationGuards(t *testing.T) {
	for _, scenario := range []string{"owned", "empty", "different host", "different creator", "older terminal", "failure review", "unexpected success", "unselected failure", "non-speculative failure"} {
		t.Run(scenario, func(t *testing.T) {
			run := runFixture("run-one", "planning", "user-me")
			if scenario == "failure review" || scenario == "unselected failure" || scenario == "non-speculative failure" {
				run = runFixture("run-one", "errored", "user-me")
			}
			if scenario == "unexpected success" {
				run = runFixture("run-one", "planned_and_finished", "user-me")
			}
			if scenario == "failure review" || scenario == "unexpected success" {
				var attributes map[string]any
				_ = json.Unmarshal(run.Attributes, &attributes)
				attributes["plan-only"] = true
				run.Attributes = raw(attributes)
			}
			source, api, cfg := fixtureSource(t, run)
			api.direct[run.ID] = run
			host, selected := "app.terraform.io", ""
			switch scenario {
			case "empty":
				api.runs = nil
			case "different host":
				host = "different.example.com"
			case "different creator":
				selected = run.ID
				api.direct[run.ID] = runFixture(run.ID, "planning", "user-other")
			case "older terminal":
				api.runs = nil
				selected = run.ID
				api.direct[run.ID] = runFixture(run.ID, "applied", "user-me")
			case "failure review", "unexpected success", "non-speculative failure":
				selected = run.ID
			}
			expectFailure := scenario == "failure review" || scenario == "unexpected success" || scenario == "unselected failure" || scenario == "non-speculative failure"
			report, _, err := qualifyTerraformLive(context.Background(), source, cfg, host, selected, expectFailure)
			wantError := scenario == "empty" || scenario == "different host" || scenario == "different creator" || (expectFailure && scenario != "failure review")
			if (err != nil) != wantError {
				t.Fatalf("qualification error = %v, want error %t", err, wantError)
			}
			if !wantError && (report.ColdBindingState == "" || report.FailureReviewed != (scenario == "failure review")) {
				t.Fatalf("incomplete qualification report: %+v", report)
			}
		})
	}
}
