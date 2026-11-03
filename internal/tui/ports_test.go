package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"radar/internal/protocol"
)

func portTask(ports ...protocol.PortForward) protocol.Task {
	return protocol.Task{Title: "Local development", Attention: "in_progress", SourceRefs: []protocol.SourceRef{{
		ID: "workspace:dev", Source: "workspace", Kind: "workspace", Role: protocol.SourceRefRoleAuthoritative,
		Path: "/workspaces/dev", ProvidesWorkspace: true, WorkspaceEntry: true,
		Presentation: protocol.SourceRefPresentation{PortForwards: ports},
	}}}
}

func generatedPortLinks(task protocol.Task) []linkChoice {
	var ports []linkChoice
	for _, link := range taskLinks(task) {
		if link.PortForward {
			ports = append(ports, link)
		}
	}
	return ports
}

func TestConfiguredPortBadgesInspectAndLinks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ports []protocol.PortForward
		badge string
		urls  []string
	}{
		{name: "empty"},
		{name: "different sandbox port", ports: []protocol.PortForward{{HostPort: 8080, SandboxPort: 3000}}, badge: "🔌 8080", urls: []string{"http://localhost:8080"}},
		{name: "multiple", ports: []protocol.PortForward{{HostPort: 8080, SandboxPort: 3000}, {HostPort: 5173, SandboxPort: 5173}}, badge: "🔌 5173, 8080", urls: []string{"http://localhost:5173", "http://localhost:8080"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := portTask(tc.ports...)
			if got := taskPortBadge(task); got != tc.badge {
				t.Fatalf("badge = %q, want %q", got, tc.badge)
			}
			view := ansi.Strip(taskDetailView(task, 100))
			if strings.Contains(view, "Sandbox port forwards") != (len(tc.ports) > 0) {
				t.Fatalf("unexpected Inspect section:\n%s", view)
			}
			for _, port := range tc.ports {
				if !strings.Contains(view, fmt.Sprintf("%d → %d", port.HostPort, port.SandboxPort)) {
					t.Fatalf("Inspect lost mapping %+v:\n%s", port, view)
				}
			}
			links := generatedPortLinks(task)
			if len(links) != len(tc.urls) {
				t.Fatalf("generated links = %+v, want %v", links, tc.urls)
			}
			for i, link := range links {
				if link.URL != tc.urls[i] || link.Detail != link.URL || link.Action != "" || !strings.Contains(link.Label, "configured") {
					t.Fatalf("generated link = %+v, want browser URL %s", link, tc.urls[i])
				}
			}
			m := model{tasks: []protocol.Task{task}}
			row := ansi.Strip(m.taskList(100, 20))
			if tc.badge == "" && strings.Contains(row, portIcon) || tc.badge != "" && !strings.Contains(row, tc.badge) {
				t.Fatalf("unexpected task row:\n%s", row)
			}
		})
	}
}

func TestPortProjectionDeduplicatesWithoutMutatingResourceActions(t *testing.T) {
	task := resourceBadgeFixture()
	task.SourceRefs[len(task.SourceRefs)-1].Presentation.PortForwards = []protocol.PortForward{{HostPort: 8080, SandboxPort: 3000}, {HostPort: 5173, SandboxPort: 5173}, {HostPort: 8080, SandboxPort: 3000}}
	before := append([]protocol.PortForward(nil), task.SourceRefs[len(task.SourceRefs)-1].Presentation.PortForwards...)
	links := taskLinks(task)
	var actions []string
	for _, link := range links {
		if link.Action != "" {
			actions = append(actions, link.Action)
		}
	}
	withoutPorts := resourceBadgeFixture()
	var originalActions []string
	for _, link := range taskLinks(withoutPorts) {
		if link.Action != "" {
			originalActions = append(originalActions, link.Action)
		}
	}
	if len(actions) == 0 || !reflect.DeepEqual(actions, originalActions) {
		t.Fatalf("resource actions changed: %v, want %v", actions, originalActions)
	}
	if len(generatedPortLinks(task)) != 2 || taskPortBadge(task) != "🔌 5173, 8080" {
		t.Fatal("port mappings were not deterministically deduplicated")
	}
	if !reflect.DeepEqual(task.SourceRefs[len(task.SourceRefs)-1].Presentation.PortForwards, before) {
		t.Fatal("rendering changed configured forwards")
	}
	for _, icon := range []string{gitWorktreeIcon, sandboxIcon, tmuxIcon, obsidianIcon} {
		if strings.Contains(ansi.Strip(taskLine(task, false, 140)), icon) {
			t.Fatalf("removed overview badge %s is still visible", icon)
		}
	}
}

func TestLongPortListsTruncateOnlyOverviewAndRetainOtherBadges(t *testing.T) {
	task := resourceBadgeFixture()
	task.Title = strings.Repeat("Long title 界 ", 20)
	for i := range 45 {
		task.SourceRefs[len(task.SourceRefs)-1].Presentation.PortForwards = append(task.SourceRefs[len(task.SourceRefs)-1].Presentation.PortForwards, protocol.PortForward{HostPort: 10000 + i, SandboxPort: 3000 + i})
	}
	for _, width := range []int{60, 80, 100} {
		for _, selected := range []bool{false, true} {
			row := ansi.Strip(taskLine(task, selected, width))
			for _, want := range []string{portIcon, "10000", "…", unresolvedIcon + " unresolved"} {
				if !strings.Contains(row, want) {
					t.Fatalf("width %d: lost %q in %s", width, want, row)
				}
			}
			if lipgloss.Width(row) > width {
				t.Fatalf("row exceeds width %d: %s", width, row)
			}
		}
	}
	inspect := ansi.Strip(taskDetailView(task, 100))
	links := generatedPortLinks(task)
	if len(links) != 45 {
		t.Fatalf("Open lost mappings: %d", len(links))
	}
	keys := map[string]bool{"j": true, "k": true, "q": true}
	for i, link := range links {
		if !strings.Contains(inspect, fmt.Sprintf("%d → %d", 10000+i, 3000+i)) || link.URL != fmt.Sprintf("http://localhost:%d", 10000+i) {
			t.Fatalf("Inspect/Open lost mapping %d", i)
		}
		if link.Key != "" {
			if keys[link.Key] {
				t.Fatalf("duplicate/reserved shortcut %q", link.Key)
			}
			keys[link.Key] = true
		}
	}
}

func TestPortRefreshUpdatesOverviewInspectAndPinnedOpenPicker(t *testing.T) {
	first := portTask(protocol.PortForward{HostPort: 8080, SandboxPort: 3000})
	first.URL = "https://example.test/original"
	m := model{tasks: []protocol.Task{first}, width: 100, height: 24, selectedCurrentTask: true}
	m = pressSectionKey(t, m, "o")
	if m.mode != "open_link" {
		t.Fatal("Open picker not available")
	}
	m.linkCursor = len(m.links) - 1
	for revision, ports := range [][]protocol.PortForward{
		{{HostPort: 5173, SandboxPort: 5173}, {HostPort: 8080, SandboxPort: 3000}},
		{{HostPort: 9000, SandboxPort: 3000}},
		nil,
	} {
		latest := portTask(ports...)
		latest.URL = "https://example.test/new"
		m.applyResponse(protocol.Response{OK: true, Revision: int64(revision + 1), Tasks: []protocol.Task{latest}}, false)
		if m.mode != "open_link" || m.links[0].URL != first.URL {
			t.Fatal("refresh replaced the pinned source-owned link or picker identity")
		}
		var generated []linkChoice
		for _, link := range m.links {
			if link.PortForward {
				generated = append(generated, link)
			}
		}
		if len(generated) != len(ports) {
			t.Fatalf("refresh kept stale port choices: %+v", m.links)
		}
		for i, port := range ports {
			if generated[i].URL != fmt.Sprintf("http://localhost:%d", port.HostPort) {
				t.Fatalf("refresh lost port %+v: %+v", port, generated)
			}
		}
		if m.linkCursor < 0 || m.linkCursor >= len(m.links) {
			t.Fatal("refresh left selection outside the picker")
		}
		if revision == 0 && m.links[m.linkCursor].URL != "http://localhost:8080" {
			t.Fatal("adding a port lost the selected forwarding")
		}
		row := ansi.Strip(m.taskList(100, 20))
		if len(ports) == 0 && strings.Contains(row, portIcon) || len(ports) > 0 && !strings.Contains(row, taskPortBadge(latest)) {
			t.Fatalf("overview did not refresh:\n%s", row)
		}
		m.mode = "detail"
		m.detail = detailState{task: first, available: true}
		m.applyResponse(protocol.Response{OK: true, Revision: int64(revision + 1), Tasks: []protocol.Task{latest}}, false)
		if !reflect.DeepEqual(taskPortForwards(m.detail.task), ports) {
			t.Fatalf("Inspect did not refresh: %+v", m.detail.task)
		}
		m.mode = "open_link"
	}
}

func TestForwardedPortPickerOpensHostDefaultBrowserWithHostPort(t *testing.T) {
	bin := t.TempDir()
	output := filepath.Join(t.TempDir(), "opened-url")
	t.Setenv("PATH", bin)
	t.Setenv("RADAR_TEST_OPENED_URL", output)
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RADAR_TEST_OPENED_URL\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	task := portTask(protocol.PortForward{HostPort: 8080, SandboxPort: 3000})
	m := model{tasks: []protocol.Task{task}, selectedCurrentTask: true}
	m = pressSectionKey(t, m, "o")
	updated, cmd := m.Update(inspectKey("enter"))
	if cmd == nil || updated.(model).mode != "" {
		t.Fatal("selecting the forwarding did not dispatch the browser")
	}
	if result := cmd().(actionMsg); result.err != nil {
		t.Fatal(result.err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(output)
		if err == nil && string(data) == "http://localhost:8080\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("browser received %q, err=%v", data, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
