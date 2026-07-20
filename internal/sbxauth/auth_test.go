package sbxauth

import "testing"

func TestIsRequired(t *testing.T) {
	for _, test := range []struct {
		name   string
		detail string
		want   bool
	}{
		{name: "unauthorized", detail: "401 Unauthorized", want: true},
		{name: "legacy session", detail: "no valid user session found", want: true},
		{name: "current sign in", detail: "Sign-in required", want: true},
		{name: "current token", detail: "docker Hub session has no access token (run 'sbx login' to refresh)", want: true},
		{name: "radar status", detail: "not signed in; run sbx login", want: true},
		{name: "unrelated", detail: "sbx daemon is unavailable", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsRequired(test.detail); got != test.want {
				t.Fatalf("IsRequired(%q) = %t, want %t", test.detail, got, test.want)
			}
		})
	}
}
