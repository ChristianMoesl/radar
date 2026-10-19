package config

import (
	"os"
	"strings"
	"testing"
)

func TestLegacyDatadogSecretsAreRejectedWithoutPrintingOrChangingThem(t *testing.T) {
	setupDraftFixture(t)
	path, _ := SecretsPath()
	body := []byte("datadog:\n  api_key: private-fixture\n  app_key: private-fixture\n")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSetupDraft()
	if err == nil || !strings.Contains(err.Error(), "remove the datadog mapping manually") || strings.Contains(err.Error(), "private-fixture") {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(body) {
		t.Fatal("obsolete credentials changed")
	}
}
func TestLegacyDatadogConfigIsRejected(t *testing.T) {
	for _, field := range []string{"api_key", "app_key", "access_token"} {
		path := setupDraftFixture(t)
		if err := os.WriteFile(path, []byte("datadog:\n  "+field+": private-fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "remove it manually") || strings.Contains(err.Error(), "private-fixture") {
			t.Fatal(err)
		}
	}
}
