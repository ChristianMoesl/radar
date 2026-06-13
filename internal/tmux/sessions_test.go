package tmux

import "testing"

func TestParseSessions(t *testing.T) {
	output := "$1\tABC-123-feature\t2\t3\t/home/me/repo\n$2\tother\t0\t1\t/tmp\n"
	sessions, err := parseSessions(output)
	if err != nil {
		t.Fatalf("parseSessions returned error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	first := sessions[0]
	if first.ID != "$1" || first.Name != "ABC-123-feature" || first.AttachedCount != 2 || first.WindowCount != 3 || first.Path != "/home/me/repo" {
		t.Fatalf("unexpected first session: %#v", first)
	}
	second := sessions[1]
	if second.ID != "$2" || second.Name != "other" || second.AttachedCount != 0 || second.WindowCount != 1 || second.Path != "/tmp" {
		t.Fatalf("unexpected second session: %#v", second)
	}
}

func TestSessionSourceRef(t *testing.T) {
	session := session{
		ID:            "$1",
		Name:          "ABC-123-feature",
		AttachedCount: 2,
		WindowCount:   3,
		Path:          "/home/me/repo",
	}

	sourceRef := session.SourceRef()
	if sourceRef.ID != "tmux:session:$1" {
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
	if sourceRef.Metadata["session_id"] != "$1" {
		t.Fatalf("unexpected session ID metadata: %#v", sourceRef.Metadata)
	}
	if sourceRef.Metadata["switch_target"] != "$1" {
		t.Fatalf("unexpected switch target metadata: %#v", sourceRef.Metadata)
	}
	if sourceRef.Metadata["ticket"] != "ABC-123" {
		t.Fatalf("unexpected ticket metadata: %#v", sourceRef.Metadata)
	}
	if sourceRef.Path != "/home/me/repo" || sourceRef.Metadata["working_directory"] != "/home/me/repo" {
		t.Fatalf("unexpected path metadata: %#v", sourceRef)
	}
	if sourceRef.Metadata["window_count"] != "3" {
		t.Fatalf("unexpected window count metadata: %#v", sourceRef.Metadata)
	}
}
