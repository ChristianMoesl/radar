package tmux

import "testing"

func TestParsePanes(t *testing.T) {
	output := "work\t1\tABC-123-feature\t%4\t/home/me/project/ABC-123\tnvim\tnvim title\n"
	panes, err := parsePanes(output)
	if err != nil {
		t.Fatalf("parsePanes returned error: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("expected 1 pane, got %d", len(panes))
	}
	pane := panes[0]
	if pane.SessionName != "work" || pane.WindowIndex != "1" || pane.WindowName != "ABC-123-feature" || pane.PaneID != "%4" || pane.Path != "/home/me/project/ABC-123" || pane.Command != "nvim" || pane.Title != "nvim title" {
		t.Fatalf("unexpected pane: %#v", pane)
	}
}

func TestPaneSourceRef(t *testing.T) {
	pane := pane{
		SessionName: "work",
		WindowIndex: "1",
		WindowName:  "ABC-123-feature",
		PaneID:      "%4",
		Path:        "/home/me/project/ABC-123",
		Command:     "nvim",
		Title:       "nvim title",
	}

	sourceRef := pane.SourceRef()
	if sourceRef.ID != "tmux:pane:%4" {
		t.Fatalf("unexpected ID: %s", sourceRef.ID)
	}
	if sourceRef.Source != "tmux" || sourceRef.Kind != "pane" {
		t.Fatalf("unexpected source ref type: %#v", sourceRef)
	}
	if sourceRef.Path != "/home/me/project/ABC-123" {
		t.Fatalf("unexpected path: %s", sourceRef.Path)
	}
	if sourceRef.Metadata["ticket"] != "ABC-123" {
		t.Fatalf("unexpected ticket metadata: %#v", sourceRef.Metadata)
	}
}
