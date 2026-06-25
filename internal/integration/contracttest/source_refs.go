package contracttest

import (
	"strings"
	"testing"

	"radar/internal/protocol"
)

func AssertValidSourceRefs(t *testing.T, source string, refs []protocol.SourceRef) {
	t.Helper()
	seen := map[string]bool{}
	for _, ref := range refs {
		if strings.TrimSpace(ref.ID) == "" {
			t.Fatalf("source ref has empty ID: %+v", ref)
		}
		if ref.Source != source {
			t.Fatalf("source ref source = %q, want %q: %+v", ref.Source, source, ref)
		}
		if strings.TrimSpace(ref.Kind) == "" {
			t.Fatalf("source ref has empty kind: %+v", ref)
		}
		if seen[ref.ID] {
			t.Fatalf("duplicate source ref ID %q", ref.ID)
		}
		seen[ref.ID] = true
		if ref.CanonicalKey == "" && len(ref.LinkingKeys) == 0 {
			t.Fatalf("source ref has neither canonical key nor linking keys: %+v", ref)
		}
		for _, key := range ref.LinkingKeys {
			if !strings.Contains(key, ":") {
				t.Fatalf("linking key %q has no prefix", key)
			}
		}
	}
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
