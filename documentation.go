// Package radar contains the same Markdown sources used by the repository guides,
// man-page build and installed CLI. Reading it needs no checkout or user state.
package radar

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Include root guides too: links in the documentation index must remain useful
// without a source checkout. Embed only public documentation, never configuration.
//
//go:embed README.md ARCHITECTURE.md CONTRIBUTING.md docs/*.md docs/integrations/*.md sandbox/README.md
var sources embed.FS

type Topic struct {
	Source string `json:"source"`
	Title  string `json:"title"`
}

type Document struct {
	Topic
	Content string `json:"content"`
}

// Topics returns canonical source paths in deterministic order.
func Topics() []Topic {
	var topics []Topic
	_ = fs.WalkDir(sources, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			doc, err := Read(path)
			if err != nil {
				return err
			}
			topics = append(topics, doc.Topic)
		}
		return nil
	})
	sort.Slice(topics, func(i, j int) bool { return topics[i].Source < topics[j].Source })
	return topics
}

// Read accepts only canonical paths from Topics, not arbitrary filesystem paths.
func Read(topic string) (Document, error) {
	data, err := sources.ReadFile(topic)
	if err != nil {
		return Document{}, fmt.Errorf("unknown documentation topic %q; list topics with radar documentation --json", topic)
	}
	title := strings.TrimPrefix(strings.SplitN(string(data), "\n", 2)[0], "# ")
	return Document{Topic: Topic{Source: topic, Title: title}, Content: string(data)}, nil
}
