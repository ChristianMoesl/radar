package terraform

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/terraform/settings"
	"radar/internal/setup"
)

// Exercise the checksum-verified released binary against a TLS fixture. This
// qualifies CLI output and request construction, not live account permissions.
func TestReleasedTFCTL(t *testing.T) {
	binary := os.Getenv("RADAR_TEST_TFCTL")
	if binary == "" {
		t.Skip("set RADAR_TEST_TFCTL to the verified tfctl 0.4.0 binary")
	}
	var rejected atomic.Bool
	var reads atomic.Int32
	run := runFixture("run-fixture", "planning", "user-me")
	run.Relationships["plan"] = rel("plan-fixture", "plans")
	plan := resource{ID: "plan-fixture", Type: "plans", Attributes: raw(map[string]any{"resource-additions": 1, "resource-changes": 0, "resource-destructions": 2})}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer terraform-fixture" {
			t.Error("request must use the profile's token for GET only")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if rejected.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"errors":[{"status":"401","title":"unauthorized"}]}`)
			return
		}
		switch r.URL.Path {
		case "/api/v2/account/details":
			fmt.Fprint(w, `{"data":{"type":"users","id":"user-me","attributes":{"username":"fixture","is-service-account":false},"relationships":{"authenticated-resource":{"data":{"id":"user-me","type":"users"}}}}}`)
		case "/api/v2/organizations/acme/workspaces/production-network":
			fmt.Fprint(w, `{"data":{"type":"workspaces","id":"ws-one","attributes":{"name":"production-network"}}}`)
		case "/api/v2/workspaces/ws-one/runs":
			reads.Add(1)
			if r.URL.Query().Get("filter[operation]") != operations || r.URL.Query().Get("page[size]") != "100" {
				t.Error("explicit operation/page query lost")
			}
			runs := []resource{}
			if r.URL.Query().Get("filter[status_group]") == "non_final" {
				runs = append(runs, run)
			}
			fmt.Fprint(w, encode(map[string]any{"data": runs, "included": []resource{plan}, "links": map[string]any{"next": nil}}))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "profiles"), 0700); err != nil {
		t.Fatal(err)
	}
	// Without the active-profile marker tfctl initializes a new default profile.
	if err := os.WriteFile(filepath.Join(dir, "active_profile.hcl"), []byte("name = \"default\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(server.URL, "https://")
	if err := os.WriteFile(filepath.Join(dir, "profiles", "default.hcl"), []byte(fmt.Sprintf("name = \"default\"\ndefault_organization = \"acme\"\nhostname = %q\ntoken = \"terraform-fixture\"\n", host)), 0600); err != nil {
		t.Fatal(err)
	}
	cert := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	f := func(ctx context.Context, c setup.Command) (string, error) {
		cmd := exec.CommandContext(ctx, binary, c.Args...)
		if c.Env == nil {
			c.Env = os.Environ()
		}
		cmd.Env = append(c.Env, "TFCTL_CONFIG_DIR="+dir, "SSL_CERT_FILE="+cert)
		if c.CaptureStderr {
			data, err := cmd.CombinedOutput()
			return string(data), err
		}
		data, err := cmd.Output()
		return string(data), err
	}
	c := client{profile: "default", output: f}
	version, err := c.version(context.Background())
	if err != nil || !setup.VersionAtLeast(version, minimumVersion) {
		t.Fatal("release version contract", version, err)
	}
	cfg := settings.Config{Profile: "default", Organization: "acme", Workspaces: []string{"production-network"}}
	session, err := c.connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if session.host != host || session.userID != "user-me" {
		t.Fatal("account/host mismatch")
	}
	result := (Source{}).collect(context.Background(), session, cfg, integration.CollectRequest{})
	if !result.Complete || reads.Load() != 2 || len(result.Observations) != 1 || result.Observations[0].Ref.Metadata["destructions"] != "2" {
		t.Fatalf("released collection: %+v, reads=%d", result, reads.Load())
	}
	rejected.Store(true)
	data, err := c.run(context.Background(), "auth", "status")
	if err == nil || !strings.Contains(data, `"active": false`) {
		t.Fatal("inactive auth must retain structured stdout on nonzero exit")
	}
	if _, err = c.connect(context.Background(), cfg); err == nil {
		t.Fatal("rejected token accepted")
	}
}
