package terraform

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/terraform/settings"
	"radar/internal/setup"
	"radar/internal/setup/setuptest"
)

// Profile initialization is entirely local and needs no TLS trust changes.
func TestReleasedTFCTLProfileCreation(t *testing.T) {
	binary := os.Getenv("RADAR_TEST_TFCTL")
	if binary == "" {
		t.Skip("set RADAR_TEST_TFCTL to the verified tfctl 0.4.0 binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(binary, filepath.Join(dir, "tfctl")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", dir)
	t.Setenv("TFCTL_CONFIG_DIR", filepath.Join(dir, "config"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sys := setup.RealSystem{}
	output := func(args ...string) string {
		t.Helper()
		data, err := sys.Output(ctx, setup.Command{Name: "tfctl", Args: args, Env: cliEnv()})
		if err != nil {
			t.Fatal(args, data, err)
		}
		return data
	}
	// Initialize the CLI's default profile before capturing its active marker.
	output("profile", "profiles", "list", "--json", "--no-color")
	marker := filepath.Join(dir, "config", "active_profile.hcl")
	before, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	ui := &setuptest.UI{Decisions: []bool{true}}
	var completed []string
	req := integration.SetupRequest{UI: ui, System: sys, Completed: func(v string) { completed = append(completed, v) }}
	ready, err := prepareProfile(ctx, req, "radar")
	if err != nil || !ready || len(completed) != 1 {
		t.Fatal("released profile initialization failed", ready, completed, err)
	}
	after, err := os.ReadFile(marker)
	if err != nil || string(before) != string(after) {
		t.Fatal("profile initialization changed the active profile", string(after), err)
	}
	if data := output("profile", "display", "--profile=radar", "--json", "--no-color"); !strings.Contains(data, `"Name": "radar"`) || !strings.Contains(data, `"Hostname": "app.terraform.io"`) {
		t.Fatal("created profile cannot be selected", data)
	}
	// Existing custom hostnames must never be replaced or prompt for creation.
	output("profile", "profiles", "create", "enterprise", "--hostname=tfe.example.test", "--no-activate")
	existing := output("profile", "display", "--profile=enterprise", "--json", "--no-color")
	ready, err = prepareProfile(ctx, req, "enterprise")
	if err != nil || !ready || len(completed) != 1 || existing != output("profile", "display", "--profile=enterprise", "--json", "--no-color") {
		t.Fatal("existing profile was not preserved", ready, completed, err)
	}
}

// Exercise the checksum-verified released binary against a TLS fixture. This
// qualifies CLI output and request construction, not live account permissions.
func TestReleasedTFCTL(t *testing.T) {
	binary := os.Getenv("RADAR_TEST_TFCTL")
	if binary == "" {
		t.Skip("set RADAR_TEST_TFCTL to the verified tfctl 0.4.0 binary")
	}
	if runtime.GOOS == "darwin" && os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("macOS TLS fixture requires system trust on a disposable GitHub runner")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var rejected atomic.Bool
	var reads atomic.Int32
	run := runFixture("run-fixture", "planning", "user-me")
	run.Relationships["plan"] = rel("plan-fixture", "plans")
	plan := resource{ID: "plan-fixture", Type: "plans", Attributes: raw(map[string]any{"resource-additions": 1, "resource-changes": 0, "resource-destructions": 2})}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		case "/api/v2/organizations/acme/workspaces":
			if r.URL.Query().Get("page[size]") != "100" || r.URL.Query().Get("page[number]") != "1" {
				t.Error("workspace discovery pagination query lost")
			}
			fmt.Fprint(w, `{"data":[{"type":"workspaces","id":"ws-one","attributes":{"name":"production-network"}}],"links":{"next":null}}`)
		case "/api/v2/organizations/acme/workspaces/production-network":
			fmt.Fprint(w, `{"data":{"type":"workspaces","id":"ws-one","attributes":{"name":"production-network"}}}`)
		case "/api/v2/workspaces/ws-one/runs":
			reads.Add(1)
			if r.URL.Query().Get("filter[operation]") != operations || r.URL.Query().Get("page[size]") != "100" || r.URL.Query().Has("filter[status_group]") {
				t.Error("explicit operation/page query lost or duplicate status scans restored")
			}
			runs := []resource{run}
			fmt.Fprint(w, encode(map[string]any{"data": runs, "included": []resource{plan}, "links": map[string]any{"next": nil}}))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{releaseFixtureCertificate(t)}}
	server.StartTLS()
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
	if err := os.WriteFile(filepath.Join(dir, "profiles", "default.hcl"), []byte(fmt.Sprintf("name = \"default\"\ndefault_organization = \"acme\"\nhostname = %q\n", host)), 0600); err != nil {
		t.Fatal(err)
	}
	cert := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		// Go's macOS verifier uses Keychain rather than SSL_CERT_FILE. This is
		// restricted above to disposable CI runners; never alter developer trust.
		fingerprint := fmt.Sprintf("%X", sha1.Sum(server.Certificate().Raw))
		keychain := "/Library/Keychains/System.keychain"
		security := func(args ...string) ([]byte, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sudo", append([]string{"-n", "security"}, args...)...)
			cmd.WaitDelay = time.Second
			return cmd.CombinedOutput()
		}
		t.Cleanup(func() {
			// Native trust-record removal requires GUI authorization on macOS 26.
			// Delete the certificate; the disposable runner owns the remaining
			// trust record for this unique, one-hour fixture certificate.
			if output, err := security("delete-certificate", "-Z", fingerprint, keychain); err != nil {
				t.Errorf("delete fixture certificate: %s: %v", output, err)
			}
		})
		t.Log("trusting the disposable fixture certificate")
		if output, err := security("add-trusted-cert", "-d", "-r", "trustRoot", "-k", keychain, cert); err != nil {
			t.Fatalf("trust fixture certificate: %s: %v", output, err)
		}
	}
	f := func(ctx context.Context, c setup.Command) (string, error) {
		cmd := exec.CommandContext(ctx, binary, c.Args...)
		cmd.WaitDelay = time.Second
		if c.Env == nil {
			c.Env = os.Environ()
		}
		cmd.Env = append(c.Env, "TFCTL_CONFIG_DIR="+dir, "SSL_CERT_FILE="+cert, "TFCTL_TOKEN=terraform-fixture")
		if c.CaptureStderr {
			data, err := cmd.CombinedOutput()
			return string(data), err
		}
		data, err := cmd.Output()
		return string(data), err
	}
	c := client{profile: "default", output: f}
	version, err := c.version(ctx)
	if err != nil || !setup.VersionAtLeast(version, minimumVersion) {
		t.Fatal("release version contract", version, err)
	}
	cfg := settings.Config{Profile: "default", Organization: "acme", Workspaces: []string{"production-network"}}
	session, err := c.connect(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if session.host != host || session.userID != "user-me" {
		t.Fatal("account/host mismatch")
	}
	names, err := session.workspaceNames(ctx, cfg.Organization)
	if err != nil || len(names) != 1 || names[0] != "production-network" {
		t.Fatal("released workspace discovery", names, err)
	}
	result := (Source{}).collect(ctx, session, cfg, integration.CollectRequest{})
	if !result.Complete || reads.Load() != 1 || len(result.Observations) != 1 || result.Observations[0].Ref.Metadata["destructions"] != "2" {
		t.Fatalf("released collection: %+v, reads=%d", result, reads.Load())
	}
	rejected.Store(true)
	data, err := c.run(ctx, "auth", "status")
	if err == nil || !strings.Contains(data, `"active": false`) {
		t.Fatal("inactive auth must retain structured stdout on nonzero exit")
	}
	if _, err = c.connect(ctx, cfg); err == nil {
		t.Fatal("rejected token accepted")
	}
}

func releaseFixtureCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "Radar disposable tfctl fixture"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
