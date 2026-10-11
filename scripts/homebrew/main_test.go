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
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"radar/internal/update"
)

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fixtureManifest() update.Manifest {
	m := update.Manifest{Schema: 1, Version: "v1.2.3", StateEpoch: update.StateEpoch, MinMacOS: "13.0", PiVersion: "1.2.3", MinPi: "0.85.1", MinNode: 24, Artifacts: map[string]update.Artifact{}}
	for _, arch := range formulaArtifacts {
		platform := arch
		if arch == "arm64" {
			platform = "darwin_arm64"
		}
		a := update.Artifact{File: "radar_v1.2.3_" + platform + ".tar.gz", Size: int64(len(arch)), SHA256: digest([]byte(arch)), BinarySHA256: digest([]byte("binary"))}
		if arch == "arm64" {
			a.NotifierVersion, a.NotifierSHA256 = "1.0.0", digest([]byte("notifier"))
		}
		m.Artifacts[arch] = a
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
	if os.Getenv("UPDATE_HOMEBREW_FIXTURE") == "1" {
		if err := os.WriteFile("testdata/Formula/radar.rb", data, 0644); err != nil {
			t.Fatal(err)
		}
		want = data
	}
	if !bytes.Equal(data, want) {
		t.Fatal("formula differs from the Homebrew style-checked fixture; update both together")
	}
	for _, wanted := range []string{
		`class Radar < Formula`, `version "1.2.3"`, `license "MIT"`,
		`on_linux do`, `on_intel do`, `on_arm do`, `depends_on arch: :arm64`, `depends_on macos: :ventura`,
		`depends_on "tmux"`, `depends_on "fd"`, `depends_on "gh"`,
		`Node.js >= 24`, `Pi >= 0.85.1`, `Git must be available on PATH`,
		`does not`, `install or update Git, Node or Pi`, `run outside Homebrew`,
		`bin.install "bin/radar"`, `(libexec/"radar").install "libexec/radar/RadarNotifier.app"`,
		`man1.install "share/man/man1/radar.1"`, `man5.install "share/man/man5/radar-config.5"`,
		`pkgshare.install "share/radar/AGENTS.md", "LICENSE"`,
		`radar setup`, `brew upgrade radar`, `radar restart`, `which -a radar`, `if OS.mac?`, `xdg-open on PATH`, `refute_path_exists libexec/"radar/RadarNotifier.app"`,
	} {
		if !strings.Contains(string(data), wanted) {
			t.Errorf("missing formula behavior %q", wanted)
		}
	}
	for _, arch := range formulaArtifacts {
		a := m.Artifacts[arch]
		if !strings.Contains(string(data), "/releases/download/v1.2.3/"+a.File) || !strings.Contains(string(data), a.SHA256) {
			t.Errorf("missing authenticated %s URL/hash", arch)
		}
	}
	for _, forbidden := range []string{"darwin_amd64", "post_install", "lsregister", "npm install", "install.sh", "xattr", "ENV[\"HOME\"]", "radar update\"", `depends_on "node"`, `depends_on "pi-coding-agent"`, `depends_on "git"`, `Formula["node"]`, `Formula["pi-coding-agent"]`} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("formula must not run user installers/updaters or bypass Gatekeeper: %q", forbidden)
		}
	}
}

func TestFormulaNotificationProbeUsesHelpAndSkip(t *testing.T) {
	data, err := renderFormula(fixtureManifest())
	if err != nil {
		t.Fatal(err)
	}
	probe := regexp.MustCompile(`pipe_output\("#\{testpath\}/radar-link setup notifications", ("(?:[^"\\]|\\.)*")\)`)
	match := probe.FindSubmatch(data)
	if match == nil {
		t.Fatal("formula must probe notification setup through the installed binary's symlink")
	}
	input, err := strconv.Unquote(string(match[1]))
	if err != nil || input != "h\ns\n" {
		t.Fatalf("notification probe must choose Help then Skip, never launch the companion: %q; %v", input, err)
	}
	if runtime.GOOS != "darwin" {
		return
	}
	prefix, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(prefix, "bin", "radar")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/radar")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build notification probe: %v; %s", err, output)
	}
	link := filepath.Join(prefix, "radar-link")
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, link, "setup", "notifications")
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = append(os.Environ(), "HOME="+prefix, "XDG_CONFIG_HOME="+filepath.Join(prefix, "config"), "PI_CODING_AGENT_DIR="+filepath.Join(prefix, "pi"))
	output, err := cmd.CombinedOutput()
	helper := filepath.Join(prefix, "libexec/radar/RadarNotifier.app/Contents/MacOS/radar-notifier")
	if err != nil || !strings.Contains(string(output), "Companion: "+helper) || !strings.Contains(string(output), "Skipped — notification settings left as they are.") || strings.Contains(string(output), "Choose one of the actions above.") {
		t.Fatalf("formula notification probe does not match the real CLI: %v; %s", err, output)
	}
}

func TestRenderRejectsUnsupportedPoliciesAndRubyInjection(t *testing.T) {
	for _, mutate := range []func(*update.Manifest){
		func(m *update.Manifest) { m.MinMacOS = "14.0" },
		func(m *update.Manifest) { m.StateEpoch++ },
		func(m *update.Manifest) { m.Version = "v1.2.3-rc.1" },
		func(m *update.Manifest) { m.MinPi = `#{system("bad")}` },
		func(m *update.Manifest) { m.PiVersion = `1.2.3"; system("bad")` },
		func(m *update.Manifest) { delete(m.Artifacts, "arm64") },
		func(m *update.Manifest) { delete(m.Artifacts, "linux_amd64") },
		func(m *update.Manifest) { delete(m.Artifacts, "linux_arm64") },
		func(m *update.Manifest) {
			a := m.Artifacts["linux_amd64"]
			a.File = "../bad"
			m.Artifacts["linux_amd64"] = a
		},
		func(m *update.Manifest) {
			a := m.Artifacts["linux_arm64"]
			a.SHA256 = "bad"
			m.Artifacts["linux_arm64"] = a
		},
		func(m *update.Manifest) {
			a := m.Artifacts["linux_arm64"]
			a.Size = update.MaxArchiveSize + 1
			m.Artifacts["linux_arm64"] = a
		},
		func(m *update.Manifest) {
			a := m.Artifacts["linux_arm64"]
			a.NotifierVersion = "1.0.0"
			m.Artifacts["linux_arm64"] = a
		},
	} {
		m := fixtureManifest()
		mutate(&m)
		if _, err := renderFormula(m); err == nil {
			t.Fatal("accepted unsupported/unsafe manifest")
		}
	}
}

func TestGenerateOnlyCompleteAuthenticatedReleases(t *testing.T) {
	for _, scenario := range []string{"ready", "unsigned", "unknown key", "wrong tag", "draft", "prerelease", "missing asset", "npm staged", "wrong npm", "corrupt arm64", "corrupt linux_amd64", "corrupt linux_arm64", "missing linux asset", "wrong linux asset size", "duplicate linux asset", "unfinished linux asset", "old manifest", "missing archive", "missing linux archive", "incompatible epoch"} {
		t.Run(scenario, func(t *testing.T) {
			manifest := fixtureManifest()
			if scenario == "incompatible epoch" {
				manifest.StateEpoch++
			}
			if scenario == "old manifest" {
				delete(manifest.Artifacts, "linux_amd64")
				delete(manifest.Artifacts, "linux_arm64")
			}
			public, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(manifest)
			signature, _ := json.Marshal(update.Signature{KeyID: "test", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, data))})
			var assets []map[string]any
			for _, arch := range formulaArtifacts {
				if scenario == "missing asset" || (scenario == "missing linux asset" && arch == "linux_arm64") {
					continue
				}
				a := manifest.Artifacts[arch]
				asset := map[string]any{"name": a.File, "size": a.Size, "state": "uploaded"}
				if arch == "linux_arm64" {
					if scenario == "wrong linux asset size" {
						asset["size"] = a.Size + 1
					}
					if scenario == "unfinished linux asset" {
						asset["state"] = "new"
					}
					if scenario == "duplicate linux asset" {
						assets = append(assets, asset)
					}
				}
				assets = append(assets, asset)
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
					for _, arch := range formulaArtifacts {
						if r.URL.Path == "/v1.2.3/"+manifest.Artifacts[arch].File {
							downloads[arch]++
							if scenario == "corrupt "+arch {
								fmt.Fprint(w, "wrong")
							} else if scenario == "missing archive" || (scenario == "missing linux archive" && arch == "linux_arm64") {
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
			for _, arch := range formulaArtifacts {
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
