package tmux

import (
	"slices"
	"testing"

	"radar/internal/integration/contracttest"
	"radar/internal/linking"
	"radar/internal/protocol"
)

func TestParseSessions(t *testing.T) {
	output := "101\t1700000001\t$1\tABC-123-feature\t2\t3\t/home/me/repo\n101\t1700000002\t$2\tother\t0\t1\t/tmp\n"
	sessions, err := parseSessions(output)
	if err != nil {
		t.Fatalf("parseSessions returned error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	first := sessions[0]
	if first.ServerPID != "101" || first.CreatedAt != "1700000001" || first.ID != "$1" || first.Name != "ABC-123-feature" || first.AttachedCount != 2 || first.WindowCount != 3 || first.Path != "/home/me/repo" {
		t.Fatalf("unexpected first session: %#v", first)
	}
	second := sessions[1]
	if second.ServerPID != "101" || second.CreatedAt != "1700000002" || second.ID != "$2" || second.Name != "other" || second.AttachedCount != 0 || second.WindowCount != 1 || second.Path != "/tmp" {
		t.Fatalf("unexpected second session: %#v", second)
	}
}

func TestParseBusySessions(t *testing.T) {
	output := "$1\t0\t1\n$2\t0\t\n$3\t0\t0\n$4\t1\t1\n"
	busy, err := parseBusySessions(output)
	if err != nil {
		t.Fatalf("parseBusySessions returned error: %v", err)
	}
	if !busy["$1"] {
		t.Fatalf("session $1 is not busy: %+v", busy)
	}
	for _, sessionID := range []string{"$2", "$3", "$4"} {
		if busy[sessionID] {
			t.Fatalf("session %s unexpectedly busy: %+v", sessionID, busy)
		}
	}
}

func TestParseBusySessionsRejectsMalformedOutput(t *testing.T) {
	if _, err := parseBusySessions("$1\t0\t1\textra\n"); err == nil {
		t.Fatal("parseBusySessions accepted malformed output")
	}
}

func TestSessionSourceRef(t *testing.T) {
	session := session{
		ServerPID:     "101",
		CreatedAt:     "1700000001",
		ID:            "$1",
		Name:          "ABC-123-feature",
		AttachedCount: 2,
		WindowCount:   3,
		Path:          "/home/me/repo",
		Busy:          true,
	}

	sourceRef := session.SourceRef(linking.NewMarkMatcher([]string{"ABC"}))
	contracttest.AssertValidSourceRefs(t, "tmux", []protocol.SourceRef{sourceRef})
	if sourceRef.ProvidesWorkspace {
		t.Fatalf("tmux resource provides workspace: %+v", sourceRef)
	}
	if sourceRef.ID != "tmux:session:101:1700000001:$1" || sourceRef.EntityID != sourceRef.ID {
		t.Fatalf("unexpected ID: %s", sourceRef.ID)
	}
	if sourceRef.Source != "tmux" || sourceRef.Kind != "session" {
		t.Fatalf("unexpected source ref type: %#v", sourceRef)
	}
	if sourceRef.Title != "ABC-123-feature" {
		t.Fatalf("unexpected title: %s", sourceRef.Title)
	}
	if sourceRef.Presentation.Label != "tmux:session:ABC-123-feature" {
		t.Fatalf("unexpected presentation label: %s", sourceRef.Presentation.Label)
	}
	if sourceRef.Status != "attached" {
		t.Fatalf("unexpected status: %s", sourceRef.Status)
	}
	if sourceRef.Metadata["session_id"] != "$1" || sourceRef.Metadata["server_pid"] != "101" || sourceRef.Metadata["session_created"] != "1700000001" {
		t.Fatalf("unexpected session identity metadata: %#v", sourceRef.Metadata)
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
	if !sourceRef.Busy {
		t.Fatalf("tmux source ref is not busy: %#v", sourceRef)
	}
	for _, want := range []string{"mark:ABC-123", "workspace:/home/me/repo"} {
		if !slices.Contains(sourceRef.LinkingKeys, want) {
			t.Fatalf("linking keys = %+v, want %s", sourceRef.LinkingKeys, want)
		}
	}
}

func TestSessionSourceRefIDDistinguishesReusedTmuxIDs(t *testing.T) {
	first := session{ServerPID: "101", CreatedAt: "1700000001", ID: "$1"}
	sameGeneration := session{ServerPID: "101", CreatedAt: "1700000001", ID: "$1"}

	if first.sourceRefID() != sameGeneration.sourceRefID() {
		t.Fatalf("same tmux session generation produced different IDs: %q and %q", first.sourceRefID(), sameGeneration.sourceRefID())
	}
	for _, reused := range []session{
		{ServerPID: "101", CreatedAt: "1700000100", ID: "$1"},
		{ServerPID: "202", CreatedAt: "1700000001", ID: "$1"},
	} {
		if first.sourceRefID() == reused.sourceRefID() {
			t.Fatalf("reused tmux ID produced the same source ref ID: %q", first.sourceRefID())
		}
	}
}
