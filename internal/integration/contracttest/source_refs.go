package contracttest

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"radar/internal/linking"
	"radar/internal/protocol"
)

func AssertValidSourceRefs(t *testing.T, source string, refs []protocol.SourceRef) {
	t.Helper()
	if err := validateSourceRefs(source, refs); err != nil {
		t.Fatal(err)
	}
}

func validateSourceRefs(source string, refs []protocol.SourceRef) error {
	seen := map[string]bool{}
	for _, ref := range refs {
		if strings.TrimSpace(ref.ID) == "" {
			return fmt.Errorf("source ref has empty ID: %+v", ref)
		}
		if ref.Source != source {
			return fmt.Errorf("source ref source = %q, want %q: %+v", ref.Source, source, ref)
		}
		if strings.TrimSpace(ref.Kind) == "" {
			return fmt.Errorf("source ref has empty kind: %+v", ref)
		}
		if ref.Role != protocol.SourceRefRoleAuthoritative && ref.Role != protocol.SourceRefRoleInformational {
			return fmt.Errorf("source ref has invalid role %q: %+v", ref.Role, ref)
		}
		if seen[ref.ID] {
			return fmt.Errorf("duplicate source ref ID %q", ref.ID)
		}
		seen[ref.ID] = true
		if ref.Role == protocol.SourceRefRoleInformational {
			if ref.CanonicalKey != "" || len(ref.LinkingKeys) != 0 || ref.Signal != "" || ref.Busy || ref.Lifecycle != "" || ref.Authority != "" || ref.RetainInactive || ref.ProvidesWorkspace {
				return fmt.Errorf("informational source ref exposes authority: %+v", ref)
			}
			if ref.EntityID == "" {
				return fmt.Errorf("informational source ref has no external entity identity: %+v", ref)
			}
		} else {
			if ref.CanonicalKey == "" && len(ref.LinkingKeys) == 0 {
				return fmt.Errorf("authoritative source ref has neither canonical key nor linking keys: %+v", ref)
			}
			if ref.EntityID == "" {
				return fmt.Errorf("authoritative source ref has no external entity identity: %+v", ref)
			}
			if ref.Lifecycle != protocol.SourceRefLifecycleWorkItem && ref.Lifecycle != protocol.SourceRefLifecycleWorkspace && ref.Lifecycle != protocol.SourceRefLifecycleResource {
				return fmt.Errorf("authoritative source ref has invalid lifecycle %q: %+v", ref.Lifecycle, ref)
			}
			if ref.Authority != protocol.SourceRefAuthorityPrimary && ref.Authority != protocol.SourceRefAuthorityContributing && ref.Authority != protocol.SourceRefAuthorityNone {
				return fmt.Errorf("authoritative source ref has invalid lifecycle authority %q: %+v", ref.Authority, ref)
			}
		}
		for _, key := range ref.LinkingKeys {
			if !strings.Contains(key, ":") {
				return fmt.Errorf("linking key %q has no prefix", key)
			}
		}
		if !ref.ProvidesWorkspace {
			continue
		}
		if ref.Role != protocol.SourceRefRoleAuthoritative {
			return fmt.Errorf("workspace-providing source ref is not authoritative: %+v", ref)
		}
		path := strings.TrimSpace(ref.Path)
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("workspace-providing source ref has non-absolute path %q: %+v", ref.Path, ref)
		}
		workspaceKey := linking.WorkspaceKey(path)
		if !slices.Contains(ref.LinkingKeys, workspaceKey) {
			return fmt.Errorf("workspace-providing source ref is missing linking key %q: %+v", workspaceKey, ref)
		}
		if ref.Lifecycle == protocol.SourceRefLifecycleResource {
			return fmt.Errorf("resource source ref provides a workspace: %+v", ref)
		}
	}
	return nil
}

func AssertStableIDs(t *testing.T, collect func() []protocol.SourceRef) {
	t.Helper()
	first := collect()
	second := collect()
	if len(first) != len(second) {
		t.Fatalf("source ref count changed between collections: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("source ref ID at index %d changed: %q vs %q", i, first[i].ID, second[i].ID)
		}
	}
}
