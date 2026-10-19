package jira

import (
	"testing"

	"radar/internal/setup"
)

func TestSetupValidation(t *testing.T) {
	for _, value := range []string{"", "123", "ABC-123", "a/b"} {
		if _, err := parsePrefixes(value); err == nil {
			t.Errorf("accepted prefix %q", value)
		}
	}
	for _, value := range []string{"http://example.atlassian.net", "https://u:p@example.atlassian.net", "https://example.atlassian.net/path", "https://example.atlassian.net?secret=x"} {
		if setup.ValidateSiteURL(value) == nil {
			t.Errorf("accepted site %q", value)
		}
	}
}
