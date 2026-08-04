package app

import "testing"

func TestDefaultIntegrationsExposeRegisteredCapabilities(t *testing.T) {
	registry := DefaultIntegrations()

	var sources []string
	for _, source := range registry.Sources() {
		sources = append(sources, source.Descriptor().Name)
	}
	wantSources := []string{"obsidian", "github", "jira", "datadog", "git", "tmux", "sbx"}
	if !equalStrings(sources, wantSources) {
		t.Fatalf("sources = %v, want %v", sources, wantSources)
	}

	var cleanupSources []string
	for _, provider := range registry.CleanupProviders() {
		cleanupSources = append(cleanupSources, provider.Descriptor().Name)
	}
	wantCleanupSources := []string{"tmux", "sbx", "git"}
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
