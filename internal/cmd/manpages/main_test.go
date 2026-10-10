package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedManuals(t *testing.T) {
	dir := t.TempDir()
	if err := generate(dir, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	for _, page := range []struct{ path, header, content string }{
		{"man1/radar.1", `.TH radar 1 "" "Radar v1.2.3" "Radar Manual"`, "radar reconcile"},
		{"man5/radar-config.5", `.TH radar-config 5 "" "Radar v1.2.3" "Radar Manual"`, "workspace.root_dir"},
	} {
		file := filepath.Join(dir, page.path)
		before, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(before), page.header) || !strings.Contains(string(before), page.content) || !strings.Contains(strings.ReplaceAll(string(before), `\:`, ""), "/blob/v1.2.3/docs/") {
			t.Fatalf("invalid manual %s:\n%s", file, before)
		}
		if err := generate(dir, "v1.2.3"); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(file)
		if !bytes.Equal(before, after) {
			t.Fatal("manual generation is not deterministic")
		}
		if groff, err := exec.LookPath("groff"); err == nil {
			cmd := exec.Command(groff, "-Kutf8", "-t", "-man", "-Tutf8", file)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if output, err := cmd.Output(); err != nil || len(output) == 0 {
				t.Fatalf("roff rendering: %v %s", err, &stderr)
			}
			// Narrow tables/URLs can produce harmless justification warnings.
			for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
				if line != "" && !strings.Contains(line, "cannot adjust line") {
					t.Fatalf("roff warning: %s", line)
				}
			}
		}
	}
}
