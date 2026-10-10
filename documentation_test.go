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
