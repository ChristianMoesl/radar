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
		"request_outcome_unknown_do_not_replay", "plan ID establishes", "freshness, not user consent",
		"does not call ordinary daemon refresh", "cleanup_apply", "schema 26",
		"## Durable-agent registration and checkpoint evidence",
	} {
		if !strings.Contains(diagnostics.Content, contract) {
			t.Errorf("diagnostic contract omitted from embedded topic: %q", contract)
		}
	}
	if strings.Contains(guide.Content, "native_handler_monotonic_elapsed") {
		t.Fatal("keep the detailed contract in one topic rather than duplicating it")
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
