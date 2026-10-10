package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/update"
)

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fixtureManifest() update.Manifest {
	m := update.Manifest{Schema: 1, Version: "v1.2.3", StateEpoch: update.StateEpoch, MinMacOS: "13.0", PiVersion: "1.2.3", MinPi: "0.85.1", MinNode: 24, Artifacts: map[string]update.Artifact{}}
	for _, arch := range []string{"arm64", "amd64"} {
		m.Artifacts[arch] = update.Artifact{File: "radar_v1.2.3_darwin_" + arch + ".tar.gz", Size: int64(len(arch)), SHA256: digest([]byte(arch)), BinarySHA256: digest([]byte("binary")), NotifierVersion: "1.0.0", NotifierSHA256: digest([]byte("notifier"))}
	}
	return m
}

func TestRenderFormula(t *testing.T) {
	m := fixtureManifest()
	data, err := renderFormula(m)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/Formula/radar.rb")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Fatal("formula differs from the Homebrew style-checked fixture; update both together")
	}
	for _, wanted := range []string{
		`class Radar < Formula`, `version "1.2.3"`, `license "MIT"`,
		`on_arm do`, `on_intel do`, `depends_on macos: :ventura`,
		`depends_on "tmux"`, `depends_on "fd"`, `depends_on "gh"`,
		`Node.js >= 24`, `Pi >= 0.85.1`, `Git must be available on PATH`,
		`does not`, `install or update Git, Node or Pi`, `post_install_steps do`, `{{opt_prefix}}/libexec/radar`,
		`bin.install "bin/radar"`, `(libexec/"radar").install "libexec/radar/RadarNotifier.app"`,
		`man1.install "share/man/man1/radar.1"`, `man5.install "share/man/man5/radar-config.5"`,
		`pkgshare.install "share/radar/AGENTS.md", "LICENSE"`,
		`lsregister`, `radar setup`, `brew upgrade radar`, `radar restart`, `which -a radar`,
	} {
		if !strings.Contains(string(data), wanted) {
			t.Errorf("missing formula behavior %q", wanted)
		}
	}
	for _, arch := range []string{"arm64", "amd64"} {
		a := m.Artifacts[arch]
		if !strings.Contains(string(data), "/releases/download/v1.2.3/"+a.File) || !strings.Contains(string(data), a.SHA256) {
			t.Errorf("missing authenticated %s URL/hash", arch)
		}
	}
	for _, forbidden := range []string{"npm install", "install.sh", "xattr", "ENV[\"HOME\"]", "radar update\"", `depends_on "node"`, `depends_on "pi-coding-agent"`, `depends_on "git"`, `Formula["node"]`, `Formula["pi-coding-agent"]`} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("formula must not run user installers/updaters or bypass Gatekeeper: %q", forbidden)
		}
	}
}

func TestRenderRejectsUnsupportedPoliciesAndRubyInjection(t *testing.T) {
	for _, mutate := range []func(*update.Manifest){
		func(m *update.Manifest) { m.MinMacOS = "14.0" },
		func(m *update.Manifest) { m.StateEpoch++ },
		func(m *update.Manifest) { m.Version = "v1.2.3-rc.1" },
		func(m *update.Manifest) { m.MinPi = `#{system("bad")}` },
		func(m *update.Manifest) { m.PiVersion = `1.2.3"; system("bad")` },
		func(m *update.Manifest) { delete(m.Artifacts, "amd64") },
	} {
		m := fixtureManifest()
		mutate(&m)
		if _, err := renderFormula(m); err == nil {
			t.Fatal("accepted unsupported/unsafe manifest")
		}
	}
}

func TestGenerateOnlyCompleteAuthenticatedReleases(t *testing.T) {
	for _, scenario := range []string{"ready", "unsigned", "unknown key", "wrong tag", "draft", "prerelease", "missing asset", "npm staged", "wrong npm", "corrupt arm64", "corrupt amd64", "missing archive", "incompatible epoch"} {
		t.Run(scenario, func(t *testing.T) {
			manifest := fixtureManifest()
			if scenario == "incompatible epoch" {
				manifest.StateEpoch++
			}
			public, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(manifest)
			signature, _ := json.Marshal(update.Signature{KeyID: "test", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, data))})
			var assets []map[string]any
			for _, arch := range []string{"arm64", "amd64"} {
				if scenario == "missing asset" && arch == "amd64" {
					continue
				}
				a := manifest.Artifacts[arch]
				assets = append(assets, map[string]any{"name": a.File, "size": a.Size, "state": "uploaded"})
			}
			downloads := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/releases":
					_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": manifest.Version, "draft": scenario == "draft", "prerelease": scenario == "prerelease", "assets": assets}})
				case "/v1.2.3/release.json":
					_, _ = w.Write(data)
				case "/v1.2.3/release.json.sig":
					if scenario == "unsigned" {
						http.NotFound(w, r)
						return
					}
					_, _ = w.Write(signature)
				case "/@christianmoesl/pi-radar/1.2.3":
					if scenario == "npm staged" {
						http.NotFound(w, r)
					} else if scenario == "wrong npm" {
						fmt.Fprint(w, `{"name":"@christianmoesl/pi-radar","version":"1.2.2"}`)
					} else {
						fmt.Fprint(w, `{"name":"@christianmoesl/pi-radar","version":"1.2.3"}`)
					}
				default:
					for _, arch := range []string{"arm64", "amd64"} {
						if r.URL.Path == "/v1.2.3/"+manifest.Artifacts[arch].File {
							downloads[arch]++
							if scenario == "corrupt "+arch {
								fmt.Fprint(w, "wrong")
							} else if scenario == "missing archive" {
								http.NotFound(w, r)
							} else {
								fmt.Fprint(w, arch)
							}
							return
						}
					}
					t.Errorf("unexpected request %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := &update.Client{HTTP: server.Client(), API: server.URL, Downloads: server.URL, Registry: server.URL, Keys: map[string]string{"test": base64.StdEncoding.EncodeToString(public)}}
			if scenario == "unknown key" {
				client.Keys = map[string]string{"other": base64.StdEncoding.EncodeToString(public)}
			}
			tag := manifest.Version
			if scenario == "wrong tag" {
				tag = "v1.2.4"
			}
			var output bytes.Buffer
			err = generate(context.Background(), client, tag, &output)
			if scenario != "ready" {
				if err == nil || output.Len() != 0 {
					t.Fatalf("must fail without partial formula: %v, %s", err, output.String())
				}
				return
			}
			if err != nil || !strings.Contains(output.String(), `version "1.2.3"`) {
				t.Fatalf("generate: %v, %s", err, output.String())
			}
			for _, arch := range []string{"arm64", "amd64"} {
				if downloads[arch] != 1 {
					t.Fatalf("did not verify %s archive", arch)
				}
			}
		})
	}
}

func TestCheckSourceVersion(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := checkSourceVersion("v1.2.3"); err == nil {
		t.Fatal("accepted missing source manifest")
	}
	for _, tc := range []struct {
		data, tag string
		valid     bool
	}{
		{`{"version":"1.2.3"}`, "v1.2.3", true},
		{`{"version":"1.2.4"}`, "v1.2.3", false},
		{`{"version":"1.2.3-rc.1"}`, "v1.2.3-rc.1", false},
		{`broken`, "v1.2.3", false},
	} {
		if err := os.WriteFile(filepath.Join("package.json"), []byte(tc.data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := checkSourceVersion(tc.tag); (err == nil) != tc.valid {
			t.Fatalf("checkSourceVersion(%s): %v", tc.tag, err)
		}
	}
}
