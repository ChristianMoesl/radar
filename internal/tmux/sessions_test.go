package tmux

import "testing"

func TestParseSessions(t *testing.T) {
	output := "ABC-123-feature\t2\t3\nother\t0\t1\n"
	sessions, err := parseSessions(output)
	if err != nil {
		t.Fatalf("parseSessions returned error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	first := sessions[0]
	if first.Name != "ABC-123-feature" || first.AttachedCount != 2 || first.WindowCount != 3 {
		t.Fatalf("unexpected first session: %#v", first)
	}
	second := sessions[1]
	if second.Name != "other" || second.AttachedCount != 0 || second.WindowCount != 1 {
		t.Fatalf("unexpected second session: %#v", second)
	}
}

func TestSessionSourceRef(t *testing.T) {
	session := session{
		Name:          "ABC-123-feature",
		AttachedCount: 2,
		WindowCount:   3,
	}

	sourceRef := session.SourceRef()
	if sourceRef.ID != "tmux:session:ABC-123-feature" {
		t.Fatalf("unexpected ID: %s", sourceRef.ID)
	}
	if sourceRef.Source != "tmux" || sourceRef.Kind != "session" {
		t.Fatalf("unexpected source ref type: %#v", sourceRef)
	}
	if sourceRef.Title != "ABC-123-feature" {
		t.Fatalf("unexpected title: %s", sourceRef.Title)
	}
	if sourceRef.Status != "attached" {
		t.Fatalf("unexpected status: %s", sourceRef.Status)
	}
	if sourceRef.Metadata["ticket"] != "ABC-123" {
		t.Fatalf("unexpected ticket metadata: %#v", sourceRef.Metadata)
	}
	if sourceRef.Metadata["window_count"] != "3" {
		t.Fatalf("unexpected window count metadata: %#v", sourceRef.Metadata)
	}
}
