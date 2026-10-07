package config

import (
	"fmt"
	"strings"
	"unicode"
)

// WorkspaceCleanupConfig grants deletion permission, not just permission to
// ignore files during safety checks. Names match direct anchor children only.
type WorkspaceCleanupConfig struct {
	DisposableEntries []string `yaml:"disposable_entries"`
}

func (cfg WorkspaceCleanupConfig) validate() error {
	seen := map[string]bool{}
	for i, name := range cfg.DisposableEntries {
		field := fmt.Sprintf("workspace.cleanup.disposable_entries[%d]", i)
		if name == "" || strings.TrimSpace(name) != name || name == "." || name == ".." ||
			strings.ContainsAny(name, `/\:*?[]{}`) ||
			strings.ContainsFunc(name, unicode.IsControl) {
			return fmt.Errorf("%s must be an exact workspace-root entry name, without paths or glob patterns", field)
		}
		if strings.EqualFold(name, "notes.md") {
			return fmt.Errorf("%s: notes.md is reserved for the canonical note link", field)
		}
		if seen[name] {
			return fmt.Errorf("%s duplicates %q", field, name)
		}
		seen[name] = true
	}
	return nil
}
