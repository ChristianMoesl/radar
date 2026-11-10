package datadog

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/command"
	"radar/internal/setup"
)

// All credentials, storage and TLS trust are disposable fixtures. The released
// CLI itself renews the token before Radar evaluates auth status; production
// Radar never reads the CLI store or implements OAuth refresh.
func TestReleasedPupNativeRenewal(t *testing.T) {
	binary := os.Getenv("RADAR_TEST_PUP")
	if binary == "" {
		t.Skip("set RADAR_TEST_PUP to the verified Pup 2.0.0 binary")
	}
	if runtime.GOOS != "linux" {
		t.Skip("loopback OAuth TLS fixture uses Linux process-local SSL_CERT_FILE trust")
	}
	for _, scenario := range []string{"expired access", "near expiry", "revoked grant", "unreachable renewal", "missing refresh", "missing client"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PUP_CONFIG_DIR", dir)
			t.Setenv("DD_TOKEN_STORAGE", "file")
			// Only the test process trusts its loopback OAuth endpoint. This
			// does not expand Radar's supported sites or system certificate trust.
			t.Setenv("PUP_TRUST_SITE", "true")
			var renewals, reads atomic.Int32
			certificate, root := renewalCertificate(t)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v2/oauth2/token":
					renewals.Add(1)
					if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh-fixture" || r.Form.Get("client_id") != "client-fixture" {
						t.Error("unexpected native refresh request")
					}
					if scenario == "revoked grant" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"private-fixture"}`))
						return
					}
					if scenario == "unreachable renewal" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					_, _ = w.Write([]byte(`{"access_token":"renewed-fixture","refresh_token":"rotated-fixture","token_type":"Bearer","expires_in":3600,"scope":"monitors_read"}`))
				case "/api/v1/monitor/search":
					reads.Add(1)
					if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer renewed-fixture" || r.Header.Get("DD-API-KEY") != "" || r.Header.Get("DD-APPLICATION-KEY") != "" {
						t.Error("request did not use renewed named OAuth session exclusively")
					}
					_, _ = w.Write([]byte(`{"metadata":{"total_count":0,"page_count":0},"monitors":[]}`))
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
			server.StartTLS()
			defer server.Close()
			cert := filepath.Join(dir, "ca.pem")
			if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root}), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SSL_CERT_FILE", cert)
			site := strings.TrimPrefix(server.URL, "https://")
			key := strings.NewReplacer(".", "_", ":", "_").Replace(site)
			write := func(name string, value any) {
				t.Helper()
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			issued := time.Now().Add(-2 * time.Hour).Unix()
			if scenario == "near expiry" {
				issued = time.Now().Add(-58 * time.Minute).Unix()
			}
			refresh := "refresh-fixture"
			if scenario == "missing refresh" {
				refresh = ""
			}
			write("tokens_"+key+".json", map[string]any{"radar": map[string]any{
				"access_token": "expired-fixture", "refresh_token": refresh, "token_type": "Bearer",
				"expires_in": 3600, "issued_at": issued, "scope": "monitors_read",
			}})
			if scenario != "missing client" {
				write("client_"+key+".json", map[string]any{"client_id": "client-fixture", "client_name": "fixture", "redirect_uris": []string{}, "registered_at": time.Now().Unix(), "site": site})
			}
			output := func(ctx context.Context, c setup.Command) (string, error) {
				data, err := command.Output(ctx, binary, c.Args, append(c.Env, "PUP_MOCK_SERVER="+server.URL))
				return string(data), err
			}
			_, err := (apiClient{output: output}).Search(context.Background(), connection{Site: site, Org: "radar"}, "tag:team:platform", []string{"Alert"})
			if scenario == "expired access" || scenario == "near expiry" {
				if err != nil || renewals.Load() != 1 || reads.Load() != 1 {
					t.Fatalf("native renewal: renewals=%d reads=%d err=%v", renewals.Load(), reads.Load(), err)
				}
			} else {
				if err == nil || reads.Load() != 0 || strings.Contains(err.Error(), "private-fixture") {
					t.Fatalf("failed renewal dispatched or disclosed diagnostics: reads=%d err=%v", reads.Load(), err)
				}
				if scenario == "missing refresh" || scenario == "missing client" {
					if renewals.Load() != 0 {
						t.Fatal("renewal attempted without a saved refresh grant/client")
					}
				} else if renewals.Load() == 0 {
					t.Fatal("Radar precheck blocked the native renewal attempt")
				}
			}
		})
	}
}

func renewalCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Radar disposable OAuth CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Radar disposable OAuth endpoint"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: key}, rootDER
}
