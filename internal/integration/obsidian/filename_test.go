package obsidian

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTaskFilename(t *testing.T) {
	for _, test := range []struct {
		name, title, want string
	}{
		{"readable", "  Plan authentication  ", "Plan authentication"},
		{"unicode", "Überprüfung 日本語 🚀", "Überprüfung 日本語 🚀"},
		{"separators", `Fix CI/CD\build`, "Fix CI-CD-build"},
		{"reserved characters", `Radar: <plan> "what?" * |`, "Radar- -plan- -what-- - -"},
		{"obsidian links", "Plan [draft] #1 ^ref", "Plan -draft- -1 -ref"},
		{"controls", "Plan\r\nnext\x00step\tend\x7f", "Plan--next-step-end-"},
		{"traversal", "../outside", "-outside"},
		{"dots", "...", "Untitled"},
		{"trailing dots", " Plan... ", "Plan"},
		{"unicode edges", ".\u2003Plan\u2003.", "Plan"},
		{"truncated unicode space", strings.Repeat("a", 197) + "\u2003end", strings.Repeat("a", 197)},
		{"device", "con", "_con"},
		{"truncated device", "CON" + strings.Repeat(" ", 198) + "x", "_CON"},
		{"device with extension", "NUL.txt", "_NUL.txt"},
		{"numbered device", "COM1", "_COM1"},
		{"superscript device", "LPT²", "_LPT²"},
		{"non-device", "COM10", "COM10"},
		{"long ascii", strings.Repeat("a", 300), strings.Repeat("a", 200)},
		{"long unicode", strings.Repeat("界", 100), strings.Repeat("界", 66)},
		{"long device", "CON." + strings.Repeat("a", 300), "_CON." + strings.Repeat("a", 195)},
		{"truncated trailing dot", strings.Repeat("a", 199) + ".end", strings.Repeat("a", 199)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := taskFilename(test.title)
			if got != test.want {
				t.Fatalf("taskFilename(%q) = %q, want %q", test.title, got, test.want)
			}
			if !utf8.ValidString(got) || len(got) > 200 || taskFilename(got) != got {
				t.Fatalf("filename is not bounded, valid UTF-8 and idempotent: %q", got)
			}
		})
	}
}
