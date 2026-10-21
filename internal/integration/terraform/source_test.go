package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/integration/github"
	"radar/internal/integration/terraform/settings"
	"radar/internal/linking"
	"radar/internal/protocol"
	"radar/internal/setup"
)

func encode(value any) string       { data, _ := json.Marshal(value); return string(data) }
func raw(value any) json.RawMessage { return json.RawMessage(encode(value)) }
func rel(id, kind string) json.RawMessage {
	return raw(map[string]any{"data": linkage{ID: id, Type: kind}})
}
func runFixture(id, status, owner string) resource {
	return resource{ID: id, Type: "runs", Attributes: raw(map[string]any{
		"status": status, "message": "ABC-123 update ingress", "plan-only": false, "auto-apply": false,
		"created-at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	}), Relationships: map[string]json.RawMessage{"created-by": rel(owner, "users"), "workspace": rel("ws-one", "workspaces")}}
}

type fixtureAPI struct {
	runs       []resource
	pages      func(*url.URL) (string, error)
	direct     map[string]resource
	calls      []string
	auth       string
	authErr    error
	profile    string
	service    bool
	workspaces func(*url.URL) (string, error)
}

func (f *fixtureAPI) output(_ context.Context, command setup.Command) (string, error) {
	if len(command.Args) == 1 && command.Args[0] == "--version" {
		return "v0.4.0", nil
	}
	profile := f.profile
	if profile == "" {
		profile = "default"
	}
	if command.Name != "tfctl" || len(command.Args) < 3 || command.Args[len(command.Args)-3] != "--profile="+profile {
		return "", fmt.Errorf("wrong CLI/profile")
	}
	args := command.Args[:len(command.Args)-3]
	if len(args) == 1 && args[0] == "--version" {
		return "v0.4.0", nil
	}
	if strings.Join(args, " ") == "auth status" {
		if f.auth != "" {
			return f.auth, f.authErr
		}
		return `{"hostname":"app.terraform.io","active":true,"username":"fixture"}`, nil
	}
	if len(args) != 3 || args[0] != "api" || args[2] != "--method=GET" {
		return "", fmt.Errorf("non-read command")
	}
	u, _ := url.Parse(args[1])
	f.calls = append(f.calls, u.String())
	switch u.Path {
	case "/account/details":
		return encode(map[string]any{"data": resource{ID: "user-me", Type: "users", Attributes: raw(map[string]any{"is-service-account": f.service})}}), nil
	case "/organizations/acme/workspaces":
		if f.workspaces != nil {
			return f.workspaces(u)
		}
		return encode(map[string]any{"data": []resource{{ID: "ws-one", Type: "workspaces", Attributes: raw(map[string]any{"name": "production-network"})}}, "links": map[string]any{"next": nil}}), nil
	case "/organizations/acme/workspaces/production-network":
		return encode(map[string]any{"data": resource{ID: "ws-one", Type: "workspaces", Attributes: raw(map[string]any{"name": "production-network"})}}), nil
	case "/workspaces/ws-one/runs":
		if f.pages != nil {
			return f.pages(u)
		}
		var records = []resource{}
		for _, run := range f.runs {
			var a runAttributes
			_ = json.Unmarshal(run.Attributes, &a)
			_, _, failed, _ := classify(a)
			terminal := a.Status == "applied" || a.Status == "planned_and_finished" || a.Status == "discarded" || a.Status == "canceled" || failed
			if (u.Query().Get("filter[status_group]") == "final") == terminal {
				records = append(records, run)
			}
		}
		return encode(map[string]any{"data": records, "links": map[string]any{"next": nil}}), nil
	default:
		id := strings.TrimPrefix(u.Path, "/runs/")
		if r, ok := f.direct[id]; ok {
			return encode(map[string]any{"data": r}), nil
		}
		return "private diagnostic must not escape", fmt.Errorf("fixture read failure")
	}
}

func fixtureSource(t *testing.T, runs ...resource) (Source, *fixtureAPI, settings.Config) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	cfg := config.Default()
	cfg.Terraform.Organization = "acme"
	cfg.Terraform.Workspaces = []string{"production-network"}
	if err := config.Create(cfg); err != nil {
		t.Fatal(err)
	}
	f := &fixtureAPI{runs: runs, direct: map[string]resource{}}
	s := NewSource(github.NewSource())
	s.output = f.output
	return s, f, cfg.Terraform
}

func tasksFrom(result integration.CollectResult) []protocol.Task {
	var tasks []protocol.Task
	for _, observation := range result.Observations {
		tasks = append(tasks, protocol.Task{Title: observation.Ref.Title, Attention: string(observation.Signal), Reason: observation.Reason, SourceRefs: []protocol.SourceRef{observation.Ref}})
	}
	return tasks
}

func TestOwnRunCollectionAndSourceContract(t *testing.T) {
	mine := runFixture("run-one", "planning", "user-me")
	other := runFixture("run-other", "planning", "user-other")
	missing := runFixture("run-missing", "planning", "")
	s, api, _ := fixtureSource(t, mine, other, missing)
	result := s.Collect(context.Background(), integration.CollectRequest{LinkingMarks: linking.NewMarkMatcher([]string{"ABC"})})
	if !result.Complete || len(result.Observations) != 1 {
		t.Fatalf("collection: %+v", result)
	}
	ref := result.Observations[0].Ref
	contracttest.AssertValidSourceRefs(t, "terraform", []protocol.SourceRef{ref})
	if ref.ID != "terraform:run:app.terraform.io:run-one" || ref.Authority != protocol.SourceRefAuthorityContributing || ref.ProvidesWorkspace || ref.Path != "" || ref.WorkspaceID != "" || ref.Presentation.PreferTitle {
		t.Fatalf("ref contract: %+v", ref)
	}
	if !strings.Contains(strings.Join(ref.LinkingKeys, ","), "mark:ABC-123") {
		t.Fatal("ticket association missing")
	}
	if len(s.Actions(context.Background(), integration.ActionRequest{Ref: ref})) != 2 {
		t.Fatal("both open actions required")
	}
	for _, call := range api.calls {
		if strings.Contains(call, "/runs?") && !strings.Contains(call, "plan_only") {
			t.Fatal("speculative runs excluded")
		}
	}
}

func TestExplicitPaginationBeyondCLIAllLimit(t *testing.T) {
	s, api, _ := fixtureSource(t)
	pages := 0
	api.pages = func(u *url.URL) (string, error) {
		if u.Query().Get("filter[status_group]") == "final" {
			return `{"data":[],"links":{"next":null}}`, nil
		}
		pages++
		n, _ := strconv.Atoi(u.Query().Get("page[number]"))
		records := []resource{}
		for i := (n - 1) * 100; i < min(n*100, 2101); i++ {
			owner := "user-other"
			if i == 2050 {
				owner = "user-me"
			}
			records = append(records, runFixture(fmt.Sprintf("run-%d", i), "planning", owner))
		}
		var next any
		if n < 22 {
			next = fmt.Sprintf("https://app.terraform.io/api/v2/workspaces/ws-one/runs?page%%5Bnumber%%5D=%d", n+1)
		}
		return encode(map[string]any{"data": records, "links": map[string]any{"next": next}}), nil
	}
	result := s.Collect(context.Background(), integration.CollectRequest{})
	if !result.Complete || pages != 22 || len(result.Observations) != 1 || !strings.HasSuffix(result.Observations[0].Ref.ID, "run-2050") {
		t.Fatalf("pages=%d result=%+v", pages, result)
	}
}

func TestPartialUnknownAndMissingReadsRetainKnownRun(t *testing.T) {
	s, api, _ := fixtureSource(t, runFixture("run-one", "planning", "user-me"))
	first := s.Collect(context.Background(), integration.CollectRequest{})
	api.runs = nil
	api.pages = func(*url.URL) (string, error) { return "", fmt.Errorf("sensitive payload") }
	result := s.Collect(context.Background(), integration.CollectRequest{Previous: tasksFrom(first)})
	if result.Complete || len(result.Observations) != 1 || result.Observations[0].Signal != integration.SignalInProgress || strings.Contains(result.SourceStatus.Detail, "sensitive") {
		t.Fatalf("unsafe failed refresh: %+v", result)
	}
	api.pages = nil
	api.direct["run-one"] = runFixture("run-one", "future_state", "user-me")
	result = s.Collect(context.Background(), integration.CollectRequest{Previous: tasksFrom(first)})
	if result.Complete || result.Observations[0].Signal == integration.SignalDone {
		t.Fatal("unknown state completed work")
	}
}

func TestRunOutcomesAndFailureReview(t *testing.T) {
	for _, status := range []string{"applied", "planned_and_finished", "canceled", "discarded", "planning", "policy_override", "planned_and_saved", "errored", "policy_soft_failed"} {
		t.Run(status, func(t *testing.T) {
			r := runFixture("run-one", status, "user-me")
			s, _, _ := fixtureSource(t, r)
			result := s.Collect(context.Background(), integration.CollectRequest{})
			if !result.Complete || len(result.Observations) != 1 {
				t.Fatalf("%+v", result)
			}
			observation := result.Observations[0]
			if status == "errored" || status == "policy_soft_failed" {
				if observation.Signal != integration.SignalAttention || observation.Ref.Acknowledgement == nil {
					t.Fatal("failure not actionable")
				}
				previous := tasksFrom(result)
				previous[0].AcknowledgementCursor = time.Now().UTC().Format(time.RFC3339)
				reviewed := s.Collect(context.Background(), integration.CollectRequest{Previous: previous})
				if !reviewed.Complete || reviewed.Observations[0].Signal != integration.SignalDone || reviewed.Observations[0].Ref.Metadata["run_status"] != status {
					t.Fatal("failure review did not preserve outcome and complete")
				}
				replayed := s.Collect(context.Background(), integration.CollectRequest{Previous: tasksFrom(reviewed)})
				if replayed.Observations[0].Signal != integration.SignalDone {
					t.Fatal("reviewed failure replayed")
				}
			}
		})
	}
	s, api, _ := fixtureSource(t, runFixture("run-save", "planned_and_saved", "user-me"))
	first := s.Collect(context.Background(), integration.CollectRequest{})
	previous := tasksFrom(first)
	previous[0].AcknowledgementCursor = time.Now().UTC().Format(time.RFC3339)
	if result := s.Collect(context.Background(), integration.CollectRequest{Previous: previous}); result.Observations[0].Signal != integration.SignalAttention {
		t.Fatal("acknowledged active approval completed")
	}
	api.runs[0] = runFixture("run-spec", "planned_and_finished", "user-me")
	var attrs map[string]any
	_ = json.Unmarshal(api.runs[0].Attributes, &attrs)
	attrs["plan-only"] = true
	api.runs[0].Attributes = raw(attrs)
	if ref := s.Collect(context.Background(), integration.CollectRequest{}).Observations[0].Ref; !strings.Contains(ref.Presentation.Label, "speculative") || ref.Status == "Applied" {
		t.Fatal("speculative success mislabeled")
	}
}

func TestColdBindingsAndDowntime(t *testing.T) {
	s, api, _ := fixtureSource(t)
	r := runFixture("run-old", "applied", "user-me")
	var attrs map[string]any
	_ = json.Unmarshal(r.Attributes, &attrs)
	attrs["created-at"] = time.Now().Add(-200 * 24 * time.Hour).Format(time.RFC3339)
	r.Attributes = raw(attrs)
	api.direct[r.ID] = r
	b := protocol.SourceBinding{Source: "terraform", Kind: "run", ID: runIdentity("app.terraform.io", r.ID), WorkItem: true}
	result := s.ResolveBindings(context.Background(), integration.BindingRequest{Bindings: []protocol.SourceBinding{b}})
	if !result.Complete || len(result.Observations) != 1 || result.Observations[0].Signal != integration.SignalDone {
		t.Fatalf("cold binding: %+v", result)
	}
	r.Relationships["created-by"] = rel("user-other", "users")
	api.direct[r.ID] = r
	if result := s.ResolveBindings(context.Background(), integration.BindingRequest{Bindings: []protocol.SourceBinding{b}}); result.Complete || len(result.Observations) != 0 {
		t.Fatal("bound lookup bypassed ownership")
	}
	b.ID = runIdentity("other.terraform.io", r.ID)
	if result := s.ResolveBindings(context.Background(), integration.BindingRequest{Bindings: []protocol.SourceBinding{b}}); result.Complete {
		t.Fatal("cross-host binding accepted")
	}
}

func TestDisabledAndServiceAccounts(t *testing.T) {
	s, api, _ := fixtureSource(t)
	api.service = true
	if result := s.Collect(context.Background(), integration.CollectRequest{}); result.Complete || len(result.Observations) != 0 {
		t.Fatal("service account accepted")
	}
	cfg, _ := config.Load()
	cfg.Terraform.Enabled = new(false)
	draft, err := config.LoadSetupDraft()
	if err != nil {
		t.Fatal(err)
	}
	if err := draft.Save(cfg); err != nil {
		t.Fatal(err)
	}
	api.calls = nil
	if status := s.Status(context.Background(), nil); status.CanRun || status.Status.Status != "disabled" {
		t.Fatal(status)
	}
	_ = s.Collect(context.Background(), integration.CollectRequest{})
	_ = s.ResolveBindings(context.Background(), integration.BindingRequest{Bindings: []protocol.SourceBinding{{Source: "terraform", ID: "bad"}}})
	if len(api.calls) != 0 {
		t.Fatal("disabled provider made API calls")
	}
}

func TestPaginationRejectsIncompleteAndForeignLinks(t *testing.T) {
	for _, link := range []string{"https://evil.example/api/v2/workspaces/ws-one/runs?page%5Bnumber%5D=2", "https://app.terraform.io/api/v2/workspaces/ws-other/runs?page%5Bnumber%5D=2", "https://app.terraform.io/api/v2/workspaces/ws-one/runs?page%5Bnumber%5D=1"} {
		var d document
		_ = json.Unmarshal([]byte(encode(map[string]any{"data": []any{}, "links": map[string]any{"next": link}})), &d)
		if _, err := nextPage(d, "app.terraform.io", "/workspaces/ws-one/runs", 1); err == nil {
			t.Fatal("unsafe pagination", link)
		}
	}
	if _, err := nextPage(document{}, "app.terraform.io", "/workspaces/ws-one/runs", 1); err == nil {
		t.Fatal("missing pagination accepted")
	}
}
