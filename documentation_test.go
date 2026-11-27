package radar

import (
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"radar/internal/protocol"
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

func TestDocumentationRuntimeProbeCancellationBoundary(t *testing.T) {
	doc, err := Read("docs/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{"whole selected runtime page", "including earlier observations", "final registration revalidation", "`not_observed`", "`cancelled_or_timed_out`", "Absolute expiry applies", "No extra probe, cached fallback or automatic retry", "ownership/leases remain uninspected"} {
		if !strings.Contains(doc.Content, contract) {
			t.Errorf("runtime probe cancellation contract omitted %q", contract)
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
		"does not call ordinary daemon refresh", "cleanup_apply", "schema 42", "native_cleanup_returned_target_summaries", "native_cleanup_execution_boundaries", "provider_call", "attempted_targets", "not_completed_may_be_partial",
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
	for _, contract := range []string{"[Pi feedback diagnostics](pi-diagnostics.md)", "## Explicit manual cleanup bundle guard check", "cleanup_check", "entire ordered resource bundle", "fresh interactive exact-task/scope confirmation", "cleanup.Service.CheckBundle", "coordination files/directories", "existing-inode probes", "no-created-lease-files", "cooperative 30-second", "automatically retried", "guards_passed_at_check", "not_passed_or_unavailable", "not be replayed", "last-moment checks", "schema 42", "No apply token"} {
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
	if err != nil || !strings.Contains(diagnostics.Content, "(pi-policy.md#configuration-policy-and-origins)") || !strings.Contains(diagnostics.Content, "(pi-policy.md#recorded-workspace-policy-diagnostics-schema-42)") {
		t.Fatalf("diagnostics must link both policy contracts: %v", err)
	}
	policy, err := Read("docs/integrations/pi-policy.md")
	if err != nil || policy.Title != "Pi configuration and workspace policy" {
		t.Fatalf("policy topic unavailable: %+v %v", policy.Topic, err)
	}
	for _, contract := range []string{"[Pi feedback diagnostics](pi-diagnostics.md)", "## Configuration policy and origins", "CLI-process global defaults", "daemon-process global defaults", "compiled_default", "configuration_file", "At most 64 KiB", "## Recorded workspace policy (diagnostics schema 42)", "registered_workspace_recipe_policy", "workspace_effective_policy", "historical per-setting", "public shape", "not private recipe bytes", "Pi validates the complete envelope", "not a registry/data migration", "separately installed Pi extension", "## Current registered member repository declarations (schema 42)", "current_member_repository_declarations", "registered source repository", "no repository override", "repository_default", "point-in-time checks"} {
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
	if err != nil || doc.Title != "Exact registered sandbox recreation" {
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
		"No automatic retry or replay", "not** a native apply plan", "Other runtime controls remain unsupported",
		"SEPARATE fresh destructive confirmation", "monotonic 60-second expiry", "RecreateSandboxes", "native_restoration_checks_returned", "unverified_may_be_partial", "session", "duplicate (including escaped)",
	} {
		if !strings.Contains(doc.Content, contract) {
			t.Errorf("native recreation proposal contract omitted: %q", contract)
		}
	}
	cli, err := Read("docs/cli.md")
	if err != nil || !strings.Contains(cli.Content, "(integrations/pi-recreation-preview.md)") || !strings.Contains(cli.Content, "schema 42") {
		t.Fatalf("CLI must link the matching-schema native proposal: %v", err)
	}
}

func TestPiManualCleanupExactPlanContractIsBundled(t *testing.T) {
	doc, err := Read("docs/integrations/pi-diagnostics.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{
		"## Explicit native manual-cleanup previews", "complete native plan", "Historical retained refs alone",
		"### Preview is freshness, not consent", "Git fetch --prune", "no lease file/directory is created",
		"32 KiB for the complete public", "never truncated or paged", "not another executor/cache",
		"missing", "workspace_id", "unknown workspace association", "stopped", "not a", "reservation",
		"### Separate exact-plan destructive apply", "separate fresh interactive destructive", "every ordered target/effect/safety",
		"no subset apply", "silent replan", "consumed before live", "coordination", "cleanup.Service.Execute",
		"cleanup_execution", "attempted Cleanup-call count", "not independently verified resource absence",
		"not forced cancellation or rollback", "cleanup_apply: true", "schema 42", "persisted formats",
		"expiry_cleanup_apply: true", "(pi-expiry-cleanup.md)",
	} {
		if !strings.Contains(doc.Content, contract) {
			t.Errorf("manual exact-plan contract omitted %q", contract)
		}
	}
}

func TestPiDiagnosticsMaintenanceCapabilityBoundaries(t *testing.T) {
	doc, err := Read("docs/integrations/pi-diagnostics.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		heading string
		want    []string
		stale   string
	}{
		{
			name:  "overview",
			want:  []string{"separately confirmed exact-plan manual and expired cleanup apply", "Full provider cleanup eligibility, durable operation history", "runtime controls remain unsupported"},
			stale: "cleanup apply\nand runtime controls are not yet exposed",
		},
		{
			name:    "collection_refresh",
			heading: "## Explicit installation-wide collection refresh",
			want:    []string{"Collection refresh, exact-plan manual cleanup, exact runtime retirement, sandbox", "exact registered-workspace expiry", "only maintenance apply flows currently exposed", "installation-wide **inspection never authorizes mutation**"},
			stale:   "This is the only maintenance apply currently exposed",
		},
		{
			name:    "exact_native_retirement",
			heading: "## Exact native runtime retirement",
			want:    []string{"SEPARATE fresh", "complete immutable ordered bundle", "existing bounded per-task cache", "IDs are", "freshness, never consent", "Stopped registered owners block", "unknown ownership refuses", "No destructive expiry escalation or silent replan", "native_bundle_returned_unverified", "not independent verification", "Other typed reconciliation and runtime controls", "remain unsupported"},
			stale:   "Runtime-retirement apply and verified operation outcomes remain unsupported",
		},
		{
			name:    "cached_gc_plan",
			heading: "## Cleanup-retention and cached GC-plan explanations",
			want:    []string{"gc_plan", "does not issue an apply token", "Separate exact-plan destructive apply", "separate whole-plan destructive consent flow", "separate native whole-plan consent flow"},
			stale:   "Explicit provider previews and apply adapters remain separate future operations",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			section := doc.Content
			if tc.heading != "" {
				_, section, _ = strings.Cut(section, tc.heading+"\n")
				if section == "" {
					t.Fatalf("missing section %q", tc.heading)
				}
			}
			section, _, _ = strings.Cut(section, "\n## ")
			for _, contract := range tc.want {
				if !strings.Contains(section, contract) {
					t.Errorf("capability boundary omitted: %q", contract)
				}
			}
			if strings.Contains(section, tc.stale) {
				t.Errorf("obsolete capability claim: %q", tc.stale)
			}
		})
	}
	t.Run("current_schema", func(t *testing.T) {
		matches := regexp.MustCompile(`(?i)\bschema ([0-9]+)\b`).FindAllStringSubmatch(doc.Content, -1)
		if len(matches) == 0 {
			t.Fatal("diagnostics topic must disclose the matching schema")
		}
		for _, match := range matches {
			if match[1] != strconv.Itoa(protocol.DiagnosticsVersion) {
				t.Errorf("documentation advertises %q; current diagnostics schema is %d", match[0], protocol.DiagnosticsVersion)
			}
		}
	})
}

func TestPiExpiryConsentContractIsSeparatelyBundled(t *testing.T) {
	diagnostics, err := Read("docs/integrations/pi-diagnostics.md")
	if err != nil || !strings.Contains(diagnostics.Content, "(pi-expiry-cleanup.md)") {
		t.Fatalf("diagnostics must link bounded expiry consent topic: %v", err)
	}
	doc, err := Read("docs/integrations/pi-expiry-cleanup.md")
	if err != nil || doc.Title != "Exact registered-workspace expiry" {
		t.Fatalf("expiry topic unavailable: %+v %v", doc.Topic, err)
	}
	for _, contract := range []string{
		"[Pi feedback diagnostics](pi-diagnostics.md)", "cleanup_mode: \"expired\"", "workspace_id", "expired mode rejects `parent_id`", "No row, cursor, limit, state", "provider Cleanup", "seven-day expiry gate", "including other", "fresh", "metadata-read approval", "complete ordered selected bundle", "32 KiB", "Paths, names, branches, messages", "Stopped registered owners block", "unknown", "SEPARATE", "interactive whole-plan destructive data-loss approval", "immutable", "identified Pi session", "never deletion consent", "consume the UI", "60-second monotonic one-use", "trusted", "including unpublished commits", "Missing-member cleanup retains its branch", "remote repositories", "authored-lifecycle", "not", "independent resource absence", "partial changes", "automatically retry or silently replan", "no paged/native-old fallback", "schema42", "No persisted data",
	} {
		if !strings.Contains(doc.Content, contract) {
			t.Errorf("expiry consent contract omitted %q", contract)
		}
	}
	if strings.Contains(diagnostics.Content, "Missing-member cleanup retains its branch") {
		t.Fatal("keep detailed expiry disclosure in one bounded topic")
	}
}

func TestDocumentationAgentStopInterruptionConsentBoundary(t *testing.T) {
	doc, err := Read("docs/integrations/pi-agent-stop.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{"schema 42", "section `agent_stop`", "registered `agent_id`", "identified Pi session", "Headless, denied", "desired-state persistence", "not a consent-token issuer", "every attempt consumes", "Context/session recapture", "--section agent_stop", "agent_stop_daemon_client_transport: true", "agent_stop_previews: true", "agent_stop_apply: true", "runtime_controls", "separate fresh interactive exact-target interruption consent", "immutable latest preview", "Duplicate (including escaped)", "30-second expiry", "15-second cap", "including connected shutdown I/O", "expiry observed after effects", "16-entry per-task", "32 KiB", "agent-stop", "not_process_outcome", "no rollback or safe-retry", "Persisted formats", "not a registry/data migration"} {
		if !strings.Contains(doc.Content, contract) {
			t.Errorf("stop transport contract omitted %q", contract)
		}
	}
	overview, err := Read("docs/integrations/pi-diagnostics.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{"## Exact registered-agent stop (schema 42)", "agent_stop_previews: true", "agent_stop_apply: true", "separate exact-target interruption", "identified Pi session"} {
		if !strings.Contains(overview.Content, contract) {
			t.Errorf("stop overview omitted %q", contract)
		}
	}
	for _, stale := range []string{"agent_stop_previews: false", "agent_stop_apply: false", "CLI/Pi stop controls remain unsupported"} {
		if strings.Contains(overview.Content, stale) || strings.Contains(doc.Content, stale) {
			t.Errorf("stale stop capability %q", stale)
		}
	}
}

func TestDiagnosticAgentRemovalExactConsentContract(t *testing.T) {
	doc, err := Read("docs/integrations/pi-agent-removal.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"schema 42", "agent_remove_daemon_client_transport: true", "agent_remove_previews: true", "agent_remove_apply: true", "runtime_controls: false", "SEPARATE fresh", "immutable latest", "schedule-disable", "schedules_before", "enabled_schedules_before", "capture counts", "Duplicate (including escaped)", "16-entry per-task", "30-second expiry", "32 KiB", "36 KiB", "agent-remove", "native_remove_callback_entered_not_resource_outcome", "identified Pi session", "Headless, denied", "every\nattempt consumes", "Context/session recapture", "not a consent-token issuer", "--section agent_remove", "Stop\nconsent is never removal consent", "checks cooperative cancellation immediately", "retained registration still holds", "Cancellation during unlink may follow both effects", "never restores schedules, registration or one-use admission", "trusted", "other owners", "no reset", "Persisted formats", "not a registry/data migration"} {
		if !strings.Contains(doc.Content, want) {
			t.Fatalf("missing removal contract %q", want)
		}
	}
	agents, err := Read("docs/durable-agents.md")
	if err != nil || !strings.Contains(agents.Content, "(integrations/pi-agent-removal.md)") {
		t.Fatal("missing native removal contract link", err)
	}
}

func TestDocumentationNativeAgentRPCCancellationBoundary(t *testing.T) {
	doc, err := Read("docs/durable-agents.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"five-second cap", "cancellation closes the connected socket", "before writing the encoded request", "before decoding a result into its target", "void shutdown reply", "target decoding already entered is not rolled back", "No transport retry is added"} {
		if !strings.Contains(doc.Content, want) {
			t.Errorf("missing native RPC cancellation contract %q", want)
		}
	}
}

func TestDocumentationNativeRemovalObservationProjectionBoundary(t *testing.T) {
	doc, err := Read("docs/durable-agents.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"shared immutable `Observation()`", "separate\nfrom the handler error", "ORIGINAL captured root/instance identities", "complete decoded native state", "pending payloads and admission metadata", "missing/empty schedules must remain unchanged", "No private payload or history is retained", "Cancelled/expired requests acquire no", "inspection never performs I/O", "Schema 42 projects only this finite native checkpoint evidence separately", "This adds no control permission; persisted formats are unchanged"} {
		if !strings.Contains(doc.Content, want) {
			t.Errorf("missing private native removal observation boundary %q", want)
		}
	}
}
