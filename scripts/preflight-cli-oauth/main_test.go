package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInventoryReportsOnlyKeyNamesWithoutMutatingFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	before := []byte("repository_dirs: [/source]\njira:\n  email: private-value\ndatadog:\n  app_key: private-value\n")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	result := inventory(path, "config.yaml", map[string][]string{"jira": {"email"}, "datadog": {"app_key"}})
	if !reflect.DeepEqual(result.Fields, []string{"jira.email", "datadog.app_key"}) || !strings.Contains(result.Handling, "rejected") {
		t.Fatal(result)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("preflight changed data")
	}
	if strings.Contains(result.Handling, "private-value") {
		t.Fatal("value leaked")
	}
}

func TestInventoryRejectsEvenEmptyJiraSecretMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.yaml")
	if err := os.WriteFile(path, []byte("jira: {}\ncustom:\n  secret: private-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := inventory(path, "secrets.yaml", map[string][]string{})
	if !reflect.DeepEqual(result.Fields, []string{"jira"}) || !strings.Contains(result.Handling, "rejected") {
		t.Fatal(result)
	}
}
