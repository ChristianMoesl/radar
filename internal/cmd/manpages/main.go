// Build-time only: end users need neither Go nor a Markdown converter.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cpuguy83/go-md2man/v2/md2man"
	documentation "radar"
)

func main() {
	output := flag.String("output", "build/man", "output directory")
	version := flag.String("version", "dev", "Radar release version")
	flag.Parse()
	if err := generate(*output, *version); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Local Markdown links become source URLs, not paths into a missing checkout.
var markdownLink = regexp.MustCompile(`\]\(([^)]+)\)`)
var nestedBoldCode = regexp.MustCompile("\\*\\*`([^`]+)`\\*\\*")
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
var roffURL = regexp.MustCompile(`https?://[^ \n]+`)

func generate(output, version string) error {
	for _, page := range []struct{ name, section, source, summary, synopsis string }{
		{"radar", "1", "docs/cli.md", "task dashboard and development workspaces", "**radar** [**--json**] [*command* [*arguments*]]"},
		{"radar-config", "5", "docs/configuration.md", "Radar configuration files", "**radar setup**\n\n**radar config-path**"},
	} {
		doc, err := documentation.Read(page.source)
		if err != nil {
			return err
		}
		_, body, _ := strings.Cut(doc.Content, "\n")
		// Avoid nested strong/code font switches leaking bold into following text.
		body = nestedBoldCode.ReplaceAllString(body, "**$1**")
		ref := version
		// Development builds have no published documentation revision.
		if !releaseTag.MatchString(ref) || strings.HasSuffix(ref, "-dirty") {
			ref = "main"
		}
		body = markdownLink.ReplaceAllStringFunc(body, func(link string) string {
			target := markdownLink.FindStringSubmatch(link)[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "#") {
				return link
			}
			path := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(page.source), target)))
			return "](https://github.com/ChristianMoesl/radar/blob/" + ref + "/" + path + ")"
		})
		// An empty date makes builds reproducible. Escape metadata before roff output.
		metadataVersion := strings.NewReplacer("\"", "", "\n", " ", "\\", "").Replace(version)
		markdown := fmt.Sprintf("%% %s %s \"\" \"Radar %s\" \"Radar Manual\"\n\n# NAME\n\n%s - %s\n\n# SYNOPSIS\n\n%s\n\n# DESCRIPTION\n%s\n\n# SEE ALSO\n\nradar(1), radar-config(5)\n", page.name, page.section, metadataVersion, page.name, page.summary, page.synopsis, body)
		dir := filepath.Join(output, "man"+page.section)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		// Permit line breaks in long URLs on narrow terminals (groff and mandoc).
		rendered := roffURL.ReplaceAllStringFunc(string(md2man.Render([]byte(markdown))), func(url string) string {
			return strings.ReplaceAll(url, "/", "/\\:")
		})
		if err := os.WriteFile(filepath.Join(dir, page.name+"."+page.section), []byte(".\\\" -*- coding: utf-8 -*-\n"+rendered), 0644); err != nil {
			return err
		}
	}
	return nil
}
