package radar

import (
	"net/url"
	"path"
	"regexp"
	"strings"
	"testing"
)

func TestDocumentationCatalog(t *testing.T) {
	topics := Topics()
	if len(topics) < 20 {
		t.Fatal("incomplete documentation index", topics)
	}
	previous := ""
	for _, topic := range topics {
		if topic.Source <= previous || topic.Title == "" {
			t.Fatalf("invalid index entry: %+v", topic)
		}
		previous = topic.Source
		doc, err := Read(topic.Source)
		if err != nil || doc.Topic != topic || !strings.HasPrefix(doc.Content, "# ") {
			t.Fatalf("invalid document: %+v %v", doc, err)
		}
		if len(doc.Content) > 64*1024 {
			t.Fatalf("split oversized topic %s before shipping it to Pi", topic.Source)
		}
	}
}

func TestDocumentationCannotReadHostFiles(t *testing.T) {
	for _, topic := range []string{"", ".", "docs", "/etc/passwd", "../README.md", "docs/../README.md", "docs/configuration.md#files", "docs\\configuration.md", "go.mod", "secrets.yaml", "internal/pi/default-AGENTS.md"} {
		if _, err := Read(topic); err == nil {
			t.Fatalf("accepted %q", topic)
		}
	}
}

func TestPiFeedbackDiagnosticsIsDedicatedEmbeddedTopic(t *testing.T) {
	guide, err := Read("docs/integrations/pi.md")
	if err != nil || !strings.Contains(guide.Content, "[Pi feedback diagnostics](pi-diagnostics.md)") {
		t.Fatalf("Pi guide must link the bounded diagnostics topic: %v", err)
	}
	diagnostics, err := Read("docs/integrations/pi-diagnostics.md")
	if err != nil || diagnostics.Title != "Pi feedback diagnostics" {
		t.Fatalf("dedicated diagnostics topic unavailable: %+v %v", diagnostics.Topic, err)
	}
	for _, contract := range []string{
		"[Pi integration](pi.md)",
		"## Current-daemon managed operations",
		"native_handler_start_only", "native_handler_monotonic_elapsed",
		"elapsed_milliseconds", "unknown_not_captured", "phase_timing_evidence", "native_completed_phase_aggregates", "completed_intervals", "Nested phases overlap",
		"## Configuration policy and origins", "## Explicit native manual-cleanup previews",
		"## Explicit installation-wide collection refresh", "30-second", "collection-only adapter",
		"## Explicit manual cleanup bundle guard check", "cleanup_check", "(pi-cleanup-check.md)", "complete_cleanup_execution_eligibility",
		"request_outcome_unknown_do_not_replay", "plan ID establishes", "freshness, not user consent",
		"does not call ordinary daemon refresh", "cleanup_apply", "schema 33", "native_cleanup_returned_target_summaries", "native_cleanup_execution_boundaries", "provider_call", "attempted_targets", "not_completed_may_be_partial",
		"complete integration envelope", "Unknown sibling fields", "native evidence shape",
		"## Durable-agent registration and checkpoint evidence",
		"complete agent inventory envelope", "Unavailable supervision never bypasses",
		"complete runtime probe envelope", "wrong targets and inconsistent evidence",
	} {
		if !strings.Contains(diagnostics.Content, contract) {
			t.Errorf("diagnostic contract omitted from embedded topic: %q", contract)
		}
	}
	if strings.Contains(guide.Content, "native_handler_monotonic_elapsed") {
		t.Fatal("keep the detailed contract in one topic rather than duplicating it")
	}
}

func TestPiCleanupCheckContractIsSeparatelyBundled(t *testing.T) {
	diagnostics, err := Read("docs/integrations/pi-diagnostics.md")
	if err != nil || !strings.Contains(diagnostics.Content, "(pi-cleanup-check.md)") {
		t.Fatalf("diagnostics must link the bounded guard-check contract: %v", err)
	}
	check, err := Read("docs/integrations/pi-cleanup-check.md")
	if err != nil || check.Title != "Pi manual cleanup bundle guard checks" {
		t.Fatalf("guard-check topic unavailable: %+v %v", check.Topic, err)
	}
	for _, contract := range []string{"[Pi feedback diagnostics](pi-diagnostics.md)", "## Explicit manual cleanup bundle guard check", "cleanup_check", "entire ordered resource bundle", "fresh interactive exact-task/scope confirmation", "cleanup.Service.CheckBundle", "coordination files/directories", "existing-inode probes", "no-created-lease-files", "cooperative 30-second", "automatically retried", "guards_passed_at_check", "not_passed_or_unavailable", "not be replayed", "last-moment checks", "schema 33", "No apply token"} {
		if !strings.Contains(check.Content, contract) {
			t.Errorf("guard-check contract omitted: %q", contract)
		}
	}
	if strings.Contains(diagnostics.Content, "cleanup.Service.CheckBundle") {
		t.Fatal("keep the detailed check contract in one bounded topic")
	}
}

func TestPiPolicyContractsAreSeparatelyBundled(t *testing.T) {
	diagnostics, err := Read("docs/integrations/pi-diagnostics.md")
	if err != nil || !strings.Contains(diagnostics.Content, "(pi-policy.md#configuration-policy-and-origins)") || !strings.Contains(diagnostics.Content, "(pi-policy.md#recorded-workspace-policy-diagnostics-schema-33)") {
		t.Fatalf("diagnostics must link both policy contracts: %v", err)
	}
	policy, err := Read("docs/integrations/pi-policy.md")
	if err != nil || policy.Title != "Pi configuration and workspace policy" {
		t.Fatalf("policy topic unavailable: %+v %v", policy.Topic, err)
	}
	for _, contract := range []string{"[Pi feedback diagnostics](pi-diagnostics.md)", "## Configuration policy and origins", "CLI-process global defaults", "daemon-process global defaults", "compiled_default", "configuration_file", "At most 64 KiB", "## Recorded workspace policy (diagnostics schema 33)", "registered_workspace_recipe_policy", "workspace_effective_policy", "historical per-setting", "public shape", "not private recipe bytes", "Pi validates the complete envelope", "not a registry/data migration", "separately installed Pi extension", "## Current registered member repository declarations (schema 33)", "current_member_repository_declarations", "registered source repository", "no repository override", "repository_default", "point-in-time checks"} {
		if !strings.Contains(policy.Content, contract) {
			t.Errorf("policy contract omitted: %q", contract)
		}
	}
	if strings.Contains(diagnostics.Content, "At most 64 KiB") || strings.Contains(diagnostics.Content, "not private recipe bytes") {
		t.Fatal("keep detailed policy contracts in one bounded topic")
	}
}

func TestLocalMarkdownDocumentationLinksAreBundled(t *testing.T) {
	link := regexp.MustCompile(`\]\(([^)]+)\)`)
	for _, topic := range Topics() {
		doc, _ := Read(topic.Source)
		for _, match := range link.FindAllStringSubmatch(doc.Content, -1) {
			u, err := url.Parse(match[1])
			if err != nil || u.IsAbs() || u.Host != "" || !strings.HasSuffix(u.Path, ".md") {
				continue
			}
			target := path.Clean(path.Join(path.Dir(topic.Source), u.Path))
			if _, err := Read(target); err != nil {
				t.Errorf("%s links to unbundled document %s", topic.Source, target)
			}
		}
	}
}

func TestPiRecreationPreviewContractIsSeparatelyBundled(t *testing.T) {
	diagnostics, err := Read("docs/integrations/pi-diagnostics.md")
	if err != nil || !strings.Contains(diagnostics.Content, "(pi-recreation-preview.md)") {
		t.Fatalf("diagnostics must link the separate native recreation proposal: %v", err)
	}
	doc, err := Read("docs/integrations/pi-recreation-preview.md")
	if err != nil || doc.Title != "Exact registered sandbox recreation proposal" {
		t.Fatalf("recreation proposal topic unavailable: %+v %v", doc.Topic, err)
	}
	for _, contract := range []string{
		"recreation_preview", "parent_id=<exact registered workspace ID>", "old peers fail closed",
		"fresh interactive exact-parent/scope confirmation", "PreviewRecreateSandboxes", "planner once",
		"cooperative native budget is 30 seconds", "secret/policy", "readability", "auto-start",
		"unambiguous", "explicit nonnull `editable: false`", "missing/null evidence", "Repeated decoded object members",
		"full private registration/recipe snapshots", "revision drift", "1 MiB", "point-in-time",
		"preview_id", "recreate", "blocked", "skipped", "runtime_identity_observed",
		"ownership_evidence", "agent_owner_count", "lease_evidence", "two transient",
		"without creating coordination files/directories", "same-count owner changes", "same-status inode",
		"not runtime ownership", "writer admission", "including stopped registrations",
		"one independent native runtime listing", "without replanning or replay",
		"Ports, secrets and", "policies are not re-read",
		"unknown_fresh_execution_guards_not_run", "withheld", "complete available and",
		"No automatic retry or replay", "not** a native apply plan", "Cleanup apply and runtime controls remain unsupported",
	} {
		if !strings.Contains(doc.Content, contract) {
			t.Errorf("native recreation proposal contract omitted: %q", contract)
		}
	}
	cli, err := Read("docs/cli.md")
	if err != nil || !strings.Contains(cli.Content, "(integrations/pi-recreation-preview.md)") || !strings.Contains(cli.Content, "schema 33") {
		t.Fatalf("CLI must link the matching-schema native proposal: %v", err)
	}
}
