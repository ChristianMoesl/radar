package jira

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Qualification of the actual checksum-verified release, independent of Jira
// accounts. This does not establish saved-profile OAuth enforcement, live PR
// relationships, refresh, or a qualified production transport.
func TestReleasedTWGMissingProfile(t *testing.T) {
	binary := os.Getenv("RADAR_TEST_TWG")
	if binary == "" {
		t.Skip("set RADAR_TEST_TWG to the verified TWG 1.3.8 binary")
	}
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// TWG also contacts its update and telemetry hosts. Reject everything
		// locally; count only the authenticated API endpoint used by this build.
		if r.Host == "api.atlassian.com:443" {
			requests.Add(1)
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
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
	env = append(env, "TWG_CONFIG_DIR="+t.TempDir(), "TWG_USER=fixture@example.invalid", "TWG_TOKEN=static-fixture",
		"HTTP_PROXY="+proxy.URL, "HTTPS_PROXY="+proxy.URL, "NO_PROXY=")
	run := func(t *testing.T, args ...string) ([]byte, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = env
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		data, err := cmd.Output()
		if ctx.Err() != nil {
			t.Fatal("headless TWG command did not finish before its deadline")
		}
		return data, err
	}
	version, err := run(t, "--version")
	if err != nil || strings.TrimSpace(string(version)) != "1.3.8" {
		t.Fatal("this contract requires TWG 1.3.8")
	}
	// Calibrate the network fixture: an unprofiled static-token request must
	// reach this local rejecting proxy, never a real Atlassian endpoint.
	if _, err := run(t, "--output", "json", "--output-summary", "none", "--timeout-ms", "1000", "whoami"); err == nil || requests.Load() == 0 {
		t.Fatal("local proxy did not observe the static-auth control request")
	}
	requests.Store(0)
	for _, args := range [][]string{
		{"whoami"},
		{"jira", "workitem", "query", "--jql", "project = ABC", "--first", "1"},
		{"jira", "workitem", "get", "ABC-123"},
		{"context", "jira", "workitem", "ABC-123", "--relationships", "jira_work_item_links_external_pull_request", "--first", "1"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			global := []string{"--profile", "missing", "--site", "example.atlassian.net", "--output", "json", "--output-summary", "none", "--timeout-ms", "1000"}
			data, err := run(t, append(global, args...)...)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatal("missing profile must exit with status 1")
			}
			var failure struct {
				OK    *bool `json:"ok"`
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(data, &failure) != nil || failure.OK == nil || *failure.OK || failure.Error.Code != "AUTH_PROFILE_MISSING" {
				t.Fatal("missing profile did not return the qualified JSON error")
			}
			if requests.Load() != 0 {
				t.Fatal("missing profile attempted a request using inherited static credentials")
			}
		})
	}
}
