package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"radar/internal/protocol"
)

const portIcon = "🔌"

func taskPortForwards(task protocol.Task) []protocol.PortForward {
	seen := make(map[protocol.PortForward]bool)
	var ports []protocol.PortForward
	for _, ref := range task.SourceRefs {
		for _, port := range ref.Presentation.PortForwards {
			if !seen[port] {
				seen[port] = true
				ports = append(ports, port)
			}
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].HostPort != ports[j].HostPort {
			return ports[i].HostPort < ports[j].HostPort
		}
		return ports[i].SandboxPort < ports[j].SandboxPort
	})
	return ports
}

func taskPortBadge(task protocol.Task) string {
	var hosts []string
	last := 0
	for _, port := range taskPortForwards(task) {
		if port.HostPort != last {
			hosts = append(hosts, strconv.Itoa(port.HostPort))
			last = port.HostPort
		}
	}
	if len(hosts) == 0 {
		return ""
	}
	return portIcon + " " + strings.Join(hosts, ", ")
}

// Keep source-owned actions/URLs pinned as before, replacing only generated
// port links when the registered mappings change during a dashboard refresh.
func withPortLinks(links []linkChoice, task protocol.Task) []linkChoice {
	usedKeys := map[string]bool{"j": true, "k": true, "q": true}
	seen := make(map[string]bool)
	var result []linkChoice
	for _, link := range links {
		if link.PortForward {
			continue
		}
		result = append(result, link)
		usedKeys[link.Key] = true
		if link.URL != "" {
			seen[link.URL] = true
		}
		if link.Ref.URL != "" {
			seen[link.Ref.URL] = true
		}
	}
	for _, port := range taskPortForwards(task) {
		url := fmt.Sprintf("http://localhost:%d", port.HostPort)
		if seen[url] {
			continue
		}
		seen[url] = true
		key := linkMnemonic("portabcdefghijklmnopqrstuvwxyz1234567890", usedKeys)
		usedKeys[key] = true
		result = append(result, linkChoice{
			Key: key, Source: "port", Label: fmt.Sprintf("%d → %d (configured)", port.HostPort, port.SandboxPort),
			Detail: url, URL: url, PortForward: true,
		})
	}
	return result
}

func (m *model) refreshPortLinks() {
	if m.mode != "open_link" {
		return
	}
	task, ok := m.linkPickerTask()
	if !ok {
		return
	}
	var selected linkChoice
	if m.linkCursor >= 0 && m.linkCursor < len(m.links) {
		selected = m.links[m.linkCursor]
	}
	m.links = withPortLinks(m.links, task)
	if selected.PortForward {
		for i, link := range m.links {
			if link.PortForward && link.URL == selected.URL {
				m.linkCursor = i
				break
			}
		}
	}
	m.syncLinkScroll()
}
