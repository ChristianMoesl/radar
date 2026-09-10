package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func legacyNote(id string) []byte {
	return []byte(fmt.Sprintf("---\nradar-id: %s\nradar-state: open\nradar-priority: normal\nradar-created-at: 2026-08-01T10:00:00Z\nradar-completed-at:\ncustom-owner: Example\n---\nBody stays byte-for-byte.\n", id))
}

func TestTitleGuessAndBytePreservation(t *testing.T) {
	for _, tt := range []struct{ name, want string }{
		{"Setup- screenshot pasting", "Setup: screenshot pasting"},
		{"Tool- fix CI-CD build", "Tool: fix CI-CD build"},
		{"ABC-123 Fix copy-pasting", "ABC-123 Fix copy-pasting"},
		{"Plan - next step", "Plan - next step"},
		{"Plan-- next step", "Plan-- next step"},
		{"XC->CA dependency", "XC->CA dependency"},
		{"Überprüfung- 日本語", "Überprüfung: 日本語"},
		{`Plan: "why?"`, `Plan: "why?"`},
	} {
		for _, newline := range []string{"\n", "\r\n"} {
			original := bytes.ReplaceAll(legacyNote("12345678-1234-4234-8234-123456789abc"), []byte("\n"), []byte(newline))
			after, title, err := migrateTitle(tt.name+".md", original)
			if err != nil || title != tt.want {
				t.Fatalf("%s: %q, %v", tt.name, title, err)
			}
			lines := bytes.SplitAfter(after, []byte(newline))
			withoutTitle := bytes.Join(append(lines[:1:1], lines[2:]...), nil)
			if !bytes.Equal(withoutTitle, original) {
				t.Fatal("changed existing frontmatter/body")
			}
			again, title, err := migrateTitle(tt.name+".md", after)
			if err != nil || title != "" || !bytes.Equal(again, after) {
				t.Fatal("migration not idempotent")
			}
		}
	}
}

func migrationVault(t *testing.T) (string, string) {
	t.Helper()
	vault := t.TempDir()
	if err := os.Mkdir(filepath.Join(vault, ".obsidian"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(vault, "Tasks", "Setup- screenshots--12345678", "Setup- screenshots.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacyNote("12345678-1234-4234-8234-123456789abc"), 0o640); err != nil {
		t.Fatal(err)
	}
	return vault, path
}

func TestPreflightAndApplyBackUpAllNotesPreservingPathsAndModes(t *testing.T) {
	vault, path := migrationVault(t)
	original, _ := os.ReadFile(path)
	changes, err := preflight(vault)
	if err != nil || len(changes) != 1 {
		t.Fatalf("preflight: %v, %v", changes, err)
	}
	untouched, _ := os.ReadFile(path)
	if !bytes.Equal(untouched, original) {
		t.Fatal("preflight modified live note")
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if err := apply(changes, backup); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(filepath.Join(backup, changes[0].relative))
	if !bytes.Equal(saved, original) {
		t.Fatal("backup differs")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, changes[0].after) {
		t.Fatal("unexpected applied contents")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed: %v", err)
	}
	again, err := preflight(vault)
	if err != nil || len(again) != 1 || !bytes.Equal(again[0].before, again[0].after) {
		t.Fatalf("retry: %v", err)
	}
}

func TestPreflightRejectsMalformedAndCollidingGuessesWithoutWriting(t *testing.T) {
	for _, failure := range []string{"collision", "missing field", "symlink", "invalid existing title"} {
		t.Run(failure, func(t *testing.T) {
			vault, path := migrationVault(t)
			original, _ := os.ReadFile(path)
			switch failure {
			case "collision":
				other := filepath.Join(vault, "Tasks", "Archived", "Setup: screenshots.md")
				if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(other, legacyNote("87654321-1234-4234-8234-123456789abc"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing field":
				original = bytes.ReplaceAll(original, []byte("radar-state: open\n"), nil)
				if err := os.WriteFile(path, original, 0o640); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(path, filepath.Join(vault, "Tasks", "link")); err != nil {
					t.Fatal(err)
				}
			case "invalid existing title":
				original = bytes.Replace(original, []byte("---\n"), []byte("---\nradar-title: []\n"), 1)
				if err := os.WriteFile(path, original, 0o640); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := preflight(vault); err == nil {
				t.Fatal("invalid migration accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, original) {
				t.Fatal("preflight wrote a note")
			}
		})
	}
}

func TestApplyRejectsConcurrentEdit(t *testing.T) {
	vault, path := migrationVault(t)
	changes, err := preflight(vault)
	if err != nil {
		t.Fatal(err)
	}
	modified := append(append([]byte{}, changes[0].before...), []byte("New user content\n")...)
	if err := os.WriteFile(path, modified, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := apply(changes, filepath.Join(t.TempDir(), "backup")); err == nil || !strings.Contains(err.Error(), "changed since preflight") {
		t.Fatalf("concurrent edit accepted: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, modified) {
		t.Fatal("concurrent edit overwritten")
	}
}
