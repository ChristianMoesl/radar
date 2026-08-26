package sbx

import (
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestAuthenticationRequired(t *testing.T) {
	for _, test := range []struct {
		name string
		req  integration.AuthenticationRequest
		want bool
	}{
		{name: "normal startup", req: integration.AuthenticationRequest{Operation: "startup"}},
		{name: "create", req: integration.AuthenticationRequest{Operation: "create"}, want: true},
		{name: "fork", req: integration.AuthenticationRequest{Operation: "fork"}, want: true},
		{name: "expired session", req: integration.AuthenticationRequest{Operation: "startup", SourceStatuses: []protocol.SourceStatus{{Name: "sbx", Status: "error", Detail: "not signed in; run sbx login"}}}, want: true},
		{name: "unrelated failure", req: integration.AuthenticationRequest{Operation: "startup", SourceStatuses: []protocol.SourceStatus{{Name: "sbx", Status: "error", Detail: "sbx daemon is unavailable"}}}},
		{name: "cleanup", req: integration.AuthenticationRequest{Operation: "cleanup", CleanupTargets: []protocol.CleanupTarget{{Source: "sbx"}}}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := authenticationRequired(test.req); got != test.want {
				t.Fatalf("authenticationRequired() = %t, want %t", got, test.want)
			}
		})
	}
}
