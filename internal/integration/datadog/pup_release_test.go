package datadog

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
	"testing"
	"time"

	"radar/internal/setup"
)

// Runs the actual checksum-verified release against a local fixture. It proves
// CLI parsing/auth precedence, not live Datadog OAuth or provider permissions.
func TestReleasedPupOAuthGuard(t *testing.T) {
	binary := os.Getenv("RADAR_TEST_PUP")
	if binary == "" {
		t.Skip("set RADAR_TEST_PUP to the verified Pup 2.0.0 binary")
	}
	dir := t.TempDir()
	t.Setenv("PUP_CONFIG_DIR", dir)
	t.Setenv("DD_TOKEN_STORAGE", "file")
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("access_token: static-fixture\napi_key: key-fixture\napp_key: app-fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tokens, _ := json.Marshal(map[string]any{"radar": map[string]any{"access_token": "oauth-fixture", "refresh_token": "refresh-fixture", "token_type": "Bearer", "expires_in": 3600, "issued_at": time.Now().Unix(), "scope": "monitors_read"}})
	if err := os.WriteFile(filepath.Join(dir, "tokens_datadoghq_eu.json"), tokens, 0600); err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Header.Get("Authorization") != "Bearer oauth-fixture" || r.Header.Get("DD-API-KEY") != "" || r.Header.Get("DD-APPLICATION-KEY") != "" {
			t.Error("request used something other than the named OAuth session")
		}
		if !strings.Contains(r.URL.Query().Get("query"), "status:(Alert)") {
			t.Error("status filtering missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"metadata":{"total_count":0,"page_count":0},"monitors":[]}`))
	}))
	defer server.Close()
	output := func(ctx context.Context, c setup.Command) (string, error) {
		cmd := exec.CommandContext(ctx, binary, c.Args...)
		cmd.Env = append(c.Env, "PUP_MOCK_SERVER="+server.URL)
		b, e := cmd.Output()
		return string(b), e
	}
	cfg := connection{Site: defaultSite, Org: "radar"}
	if _, err := (apiClient{output: output}).Search(context.Background(), cfg, "tag:team:platform", []string{"Alert"}); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatal("monitor query count", count.Load())
	}
	cfg.Org = "missing"
	// Exercise the guarded request directly to cover a session disappearing after
	// status verification. Valid config-file API keys must never reach the wire.
	if _, err := pupOutput(context.Background(), cfg, pupArgs(cfg, "api", "/api/v1/monitor/search"), output); err == nil {
		t.Fatal("missing OAuth session accepted")
	}
	if count.Load() != 1 {
		t.Fatal("fallback sent a request")
	}
}
