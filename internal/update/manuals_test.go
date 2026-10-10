package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManualActivationAndRollback(t *testing.T) {
	for _, existed := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{true: "upgrade", false: "first install"}[existed], map[bool]string{true: "rollback", false: "success"}[fail]}, "/"), func(t *testing.T) {
				s, _ := stagedFixture(t, false)
				if existed {
					for _, path := range manualPaths {
						put(t, filepath.Join(s.Prefix, path), "old manual "+path, 0644)
					}
				}
				var err error
				s.Journal.Manuals, err = inspectManuals(s.Prefix, s.Root)
				if err != nil {
					t.Fatal(err)
				}
				err = s.Activate(Hooks{Health: func(string, string) error {
					for _, path := range manualPaths {
						if sum, err := manualDigest(filepath.Join(s.Prefix, path), true); err != nil || sum != s.Journal.Manuals[path].Next {
							t.Fatalf("manual not installed before health check: %v", err)
						}
					}
					if fail {
						return errors.New("unhealthy")
					}
					return nil
				}})
				if (err != nil) != fail {
					t.Fatal(err)
				}
				for _, path := range manualPaths {
					sum, err := manualDigest(filepath.Join(s.Prefix, path), !fail || existed)
					want := s.Journal.Manuals[path].Next
					if fail {
						want = s.Journal.Manuals[path].Previous
					}
					if err != nil || sum != want {
						t.Fatalf("manual digest %s, want %s: %v", sum, want, err)
					}
				}
			})
		}
	}
}

func TestManualCrashRecovery(t *testing.T) {
	for _, existed := range []bool{true, false} {
		for _, installed := range []int{0, 1, 2} {
			s, _ := stagedFixture(t, false)
			if existed {
				for _, path := range manualPaths {
					put(t, filepath.Join(s.Prefix, path), "old "+path, 0644)
				}
			}
			s.Journal.Manuals, _ = inspectManuals(s.Prefix, s.Root)
			if err := s.backupManuals(); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(filepath.Join(s.Prefix, "bin/radar"), filepath.Join(s.Prefix, transactionPath, "previous-radar")); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(filepath.Join(s.Prefix, transactionPath, "journal.json"), s.Journal); err != nil {
				t.Fatal(err)
			}
			for _, path := range manualPaths[:installed] {
				if err := copyManual(filepath.Join(s.Root, path), filepath.Join(s.Prefix, path)); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := Recover(s.Prefix); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range manualPaths {
				if sum, err := manualDigest(filepath.Join(s.Prefix, path), false); err != nil || sum != s.Journal.Manuals[path].Previous {
					t.Fatalf("manual not restored: %v", err)
				}
			}
		}
	}
}

func TestManualProtection(t *testing.T) {
	t.Run("external edit", func(t *testing.T) {
		s, _ := stagedFixture(t, false)
		put(t, filepath.Join(s.Prefix, manualPaths[0]), "external", 0644)
		if err := s.Activate(Hooks{Health: func(string, string) error { return nil }}); err == nil {
			t.Fatal("overwrote changed manual")
		}
	})
	t.Run("missing staged page", func(t *testing.T) {
		s, _ := stagedFixture(t, false)
		os.Remove(filepath.Join(s.Root, manualPaths[0]))
		if _, err := inspectManuals(s.Prefix, s.Root); err == nil {
			t.Fatal("accepted incomplete release")
		}
	})
	t.Run("symlink directory", func(t *testing.T) {
		s, _ := stagedFixture(t, false)
		if err := os.Symlink(t.TempDir(), filepath.Join(s.Prefix, "share")); err != nil {
			t.Fatal(err)
		}
		if err := s.Activate(Hooks{Health: func(string, string) error { return nil }}); err == nil {
			t.Fatal("followed manual directory symlink")
		}
	})
	t.Run("rollback refuses edits", func(t *testing.T) {
		s, _ := stagedFixture(t, false)
		err := s.Activate(Hooks{Health: func(string, string) error {
			put(t, filepath.Join(s.Prefix, manualPaths[0]), "user edit", 0644)
			return errors.New("unhealthy")
		}})
		if err == nil || !strings.Contains(err.Error(), "manual changed") {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(s.Prefix, manualPaths[0]))
		if string(data) != "user edit" {
			t.Fatal("overwrote user edit")
		}
	})
	t.Run("old journal explicitly rejected", func(t *testing.T) {
		s, _ := stagedFixture(t, false)
		s.Journal.Schema = 1
		writeJSON(filepath.Join(s.Prefix, transactionPath, "journal.json"), s.Journal)
		if _, err := ReadJournal(s.Prefix); err == nil || !strings.Contains(err.Error(), "previous Radar version") {
			t.Fatal(err)
		}
	})
	t.Run("arbitrary journal path rejected", func(t *testing.T) {
		s, _ := stagedFixture(t, false)
		delete(s.Journal.Manuals, manualPaths[0])
		s.Journal.Manuals["../outside"] = s.Journal.Manuals[manualPaths[1]]
		writeJSON(filepath.Join(s.Prefix, transactionPath, "journal.json"), s.Journal)
		if _, err := ReadJournal(s.Prefix); err == nil {
			t.Fatal("accepted unowned path")
		}
	})
}
