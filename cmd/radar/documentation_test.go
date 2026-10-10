package main

import (
	"strings"
	"testing"
)

func TestDocumentationCLIWithoutSetupOrTools(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
		code int
	}{
		{[]string{"documentation", "--json"}, `"source":"docs/configuration.md"`, 0},
		{[]string{"--json", "documentation", "--topic", "docs/configuration.md"}, `"version":"dev"`, 0},
		{[]string{"documentation", "--topic", "docs/integrations/pi.md", "--json"}, `"content":`, 0},
		{[]string{"documentation", "--topic", "docs/configuration.md"}, "# Configuration", 0},
		{[]string{"documentation"}, "Radar dev documentation", 0},
		{[]string{"documentation", "--topic", "/etc/passwd", "--json"}, "unknown documentation topic", 1},
		{[]string{"documentation", "--topic", "docs/../README.md", "--json"}, "unknown documentation topic", 1},
		{[]string{"documentation", "extra"}, "usage:", 2},
		{[]string{"documentation", "--unknown"}, "flag provided but not defined", 2},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			stdout, stderr, code := outputCLI(t, test.args, "")
			if code != test.code {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
			if code == 0 && (!strings.Contains(stdout, test.want) || stderr != "") {
				t.Fatalf("stdout=%s stderr=%s", stdout, stderr)
			}
			if code != 0 && (stdout != "" || !strings.Contains(stderr, test.want)) {
				t.Fatalf("stdout=%s stderr=%s", stdout, stderr)
			}
		})
	}
}
