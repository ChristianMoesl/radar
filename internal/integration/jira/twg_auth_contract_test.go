package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// These tests characterize a release blocker, not an OAuth enforcement guard.
// All stores and credentials below are disposable synthetic fixtures. Production
// Radar must never read or modify a CLI credential store to enforce OAuth.
func TestReleasedTWGSavedProfileAuthModes(t *testing.T) {
	binary := os.Getenv("RADAR_TEST_TWG")
	if binary == "" {
		t.Skip("set RADAR_TEST_TWG to the verified TWG 1.3.8 binary")
	}
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "api.atlassian.com:443" {
			requests.Add(1)
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	root := t.TempDir()
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "TWG_") {
			continue
		}
		switch strings.ToUpper(key) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
			continue
		}
		env = append(env, value)
	}
	env = append(env, "TWG_CONFIG_DIR="+root, "HTTP_PROXY="+proxy.URL, "HTTPS_PROXY="+proxy.URL, "NO_PROXY=")
	run := func(t *testing.T, args ...string) ([]byte, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = env
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		data, err := cmd.Output()
		if ctx.Err() != nil {
			t.Fatal("headless TWG command exceeded its deadline")
		}
		return data, err
	}
	version, err := run(t, "--version")
	if err != nil || strings.TrimSpace(string(version)) != "1.3.8" {
		t.Fatal("this contract requires TWG 1.3.8")
	}
	prefix := []string{"--profile", "radar-fixture", "--site", "00000000-0000-4000-8000-000000000001", "--output", "json", "--output-summary", "none", "--timeout-ms", "1000"}
	profileMethod := func(t *testing.T) string {
		t.Helper()
		data, err := run(t, append(append([]string(nil), prefix...), "auth", "profile", "list")...)
		var result struct {
			Data struct {
				Profiles []struct {
					Name       string `json:"name"`
					AuthMethod string `json:"authMethod"`
				} `json:"profiles"`
			} `json:"data"`
		}
		if err != nil || json.Unmarshal(data, &result) != nil || len(result.Data.Profiles) != 1 || result.Data.Profiles[0].Name != "radar-fixture" {
			t.Fatal("TWG did not recognize the synthetic saved profile")
		}
		if strings.Contains(string(data), "fixture-secret") {
			t.Fatal("profile listing exposed synthetic credentials")
		}
		return result.Data.Profiles[0].AuthMethod
	}
	request := []string{"jira", "workitem", "query", "--jql", "project = ABC", "--fields", "summary,status", "--first", "1"}
	attempt := func(t *testing.T) {
		t.Helper()
		requests.Store(0)
		if _, err := run(t, append(append([]string(nil), prefix...), request...)...); err == nil {
			t.Fatal("the rejecting proxy must fail the request")
		}
		if requests.Load() == 0 {
			t.Fatal("TWG did not attempt API dispatch with the selected synthetic credentials")
		}
	}

	t.Run("saved PAT is accepted with explicit profile and site", func(t *testing.T) {
		writeTWGAuthFixture(t, root, "pat")
		if profileMethod(t) != "pat" {
			t.Fatal("fixture must identify as a saved PAT profile")
		}
		attempt(t)
	})
	t.Run("inherited static credentials override saved OAuth", func(t *testing.T) {
		writeTWGAuthFixture(t, root, "oauth")
		original := env
		env = append(append([]string(nil), env...), "TWG_TOKEN=static-fixture-secret", "TWG_USER=fixture@example.invalid")
		defer func() { env = original }()
		if profileMethod(t) != "oauth" {
			t.Fatal("profile metadata must still identify OAuth")
		}
		requests.Store(0)
		data, _ := run(t, append(append([]string(nil), prefix...), "doctor")...)
		var result struct {
			Data struct {
				Auth struct {
					Resolved struct {
						AuthMethod string `json:"authMethod"`
					} `json:"resolved"`
				} `json:"auth"`
				Connectivity struct {
					Attempted bool `json:"attempted"`
					OK        bool `json:"ok"`
				} `json:"connectivity"`
			} `json:"data"`
		}
		if json.Unmarshal(data, &result) != nil || result.Data.Auth.Resolved.AuthMethod != "pat" ||
			!result.Data.Connectivity.Attempted || result.Data.Connectivity.OK || requests.Load() == 0 {
			t.Fatal("doctor must report PAT authentication and failed connectivity with inherited static credentials")
		}
		attempt(t)
	})
	t.Run("OAuth precheck does not pin the later request method", func(t *testing.T) {
		writeTWGAuthFixture(t, root, "oauth")
		if profileMethod(t) != "oauth" {
			t.Fatal("precheck must observe an OAuth profile")
		}
		// Deterministic interleaving: the selected profile changes after the
		// check and before the next CLI invocation loads its credentials.
		writeTWGAuthFixture(t, root, "pat")
		attempt(t)
		if profileMethod(t) != "pat" {
			t.Fatal("the selected profile changed method without preventing later dispatch")
		}
	})
}

func writeTWGAuthFixture(t *testing.T, root, method string) {
	t.Helper()
	const id = "00000000-0000-4000-8000-000000000002"
	primary := map[string]any{"authMethod": method, "user": "fixture@example.invalid", "token": "fixture-secret"}
	if method == "oauth" {
		primary = map[string]any{
			"authMethod": "oauth", "user": "fixture@example.invalid", "token": "", "oauthAccessToken": "oauth-fixture-secret",
			"oauthRefreshToken": "refresh-fixture-secret", "oauthRefreshAt": "2099-01-01T00:00:00Z",
		}
	}
	document := map[string]any{
		"version": 1, "revision": 0, "activeEnvironment": "prod", "defaults": map[string]string{"prod": id},
		"profiles": []any{map[string]any{
			"id": id, "name": "radar-fixture", "defaultName": "example-" + method,
			"user": "fixture@example.invalid", "site": "example.atlassian.net", "domain": "example.atlassian.net",
			"cloudId": "00000000-0000-4000-8000-000000000001", "environment": "prod",
			"createdAt": "2026-10-20T00:00:00Z", "authMethod": method, "revision": 0, "bitbucket": false,
			"credentials": map[string]any{"primary": primary},
		}},
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "auth-profiles.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
