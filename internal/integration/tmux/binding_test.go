package tmux

import (
	"testing"

	"radar/internal/linking"
	"radar/internal/protocol"
)

func TestSessionBindingTracksExistingLifetimeIdentity(t *testing.T) {
	original := session{ServerPID: "101", CreatedAt: "1700000001", ID: "$1", Name: "feature", Path: "/work/feature"}
	first := original.SourceRef(linking.MarkMatcher{})
	binding := first.Binding()
	if binding.ID != "tmux:session:101:1700000001:$1" || binding.Key != "" || binding.WorkItem || first.Lifecycle != protocol.SourceRefLifecycleResource || first.Authority != protocol.SourceRefAuthorityNone {
		t.Fatalf("session binding changed existing identity/authority: %+v", binding)
	}

	same := original
	same.Name = "renamed"
	same.Path = "/work/other"
	same.AttachedCount = 2
	same.WindowCount = 3
	same.Activity = protocol.ActivityBusy
	if sameBinding := same.SourceRef(linking.MarkMatcher{}).Binding(); sameBinding != binding || sameBinding.LinkingKey() != binding.LinkingKey() {
		t.Fatalf("session observations changed lifetime binding: first=%+v same=%+v", binding, sameBinding)
	}

	for _, change := range []struct {
		name string
		edit func(*session)
	}{
		{"server-pid", func(s *session) { s.ServerPID = "202" }},
		{"created-at", func(s *session) { s.CreatedAt = "1700000100" }},
		{"session-id", func(s *session) { s.ID = "$2" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			reusedName := original
			change.edit(&reusedName)
			if replacement := reusedName.SourceRef(linking.MarkMatcher{}).Binding(); replacement.LinkingKey() == binding.LinkingKey() {
				t.Fatalf("reused session name inherited old binding: first=%+v replacement=%+v", binding, replacement)
			}
		})
	}
}

func TestParseSessionsRejectsIncompleteLifetimeIdentity(t *testing.T) {
	for _, output := range []string{
		"\t1700000001\t$1\tfeature\t0\t1\t/work/feature\n",
		"101\t\t$1\tfeature\t0\t1\t/work/feature\n",
		"101\t1700000001\t\tfeature\t0\t1\t/work/feature\n",
	} {
		if _, err := parseSessions(output); err == nil {
			t.Fatalf("accepted incomplete lifetime identity: %q", output)
		}
	}
}
