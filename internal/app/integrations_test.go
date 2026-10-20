package app

import (
	"testing"

	"radar/internal/integration"
)

func TestDefaultIntegrationsExposeRegisteredCapabilities(t *testing.T) {
	registry := DefaultIntegrations()

	var sources []string
	for _, source := range registry.Sources() {
		sources = append(sources, source.Descriptor().Name)
	}
	wantSources := []string{"obsidian", "github", "terraform", "jira", "datadog", "workspace", "git", "tmux", "sbx", "agent"}
	if !equalStrings(sources, wantSources) {
		t.Fatalf("sources = %v, want %v", sources, wantSources)
	}

	var cleanupSources []string
	for _, provider := range registry.CleanupProviders() {
		cleanupSources = append(cleanupSources, provider.Descriptor().Name)
	}
	wantCleanupSources := []string{"tmux", "sbx", "git", "workspace"}
	if !equalStrings(cleanupSources, wantCleanupSources) {
		t.Fatalf("cleanup sources = %v, want %v", cleanupSources, wantCleanupSources)
	}

	if _, err := registry.TaskAuthoring(); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Workspace(); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Multiplexer(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		phase integration.SetupPhase
		names []string
	}{
		{integration.SetupEnvironment, []string{"sbx", "tmux"}},
		{integration.SetupDirectories, []string{"git", "workspace", "obsidian"}},
		{integration.SetupConnections, []string{"github", "jira", "datadog", "terraform"}},
	} {
		var names []string
		for _, provider := range registry.SetupProviders(tc.phase) {
			names = append(names, provider.Descriptor().Name)
		}
		if !equalStrings(names, tc.names) {
			t.Fatalf("setup phase %d = %v, want %v", tc.phase, names, tc.names)
		}
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
