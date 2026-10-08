package scripts_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"radar/internal/update"
)

func TestBootstrapTrustMatchesUpdater(t *testing.T) {
	script := mustReadScript(t, "../install.sh")
	match := regexp.MustCompile(`const trustedKeys = (\{[^\n]+\});`).FindSubmatch(script)
	if len(match) != 2 {
		t.Fatal("bootstrap must embed independently committed publisher roots")
	}
	var roots map[string]string
	if err := json.Unmarshal(match[1], &roots); err != nil || !reflect.DeepEqual(roots, update.TrustedKeys()) {
		t.Fatalf("bootstrap and updater trust diverged: %v", err)
	}
	if !bytes.Contains(script, []byte(fmt.Sprintf("const stateEpoch = %d;", update.StateEpoch))) || update.MaxArchiveSize != 128*1024*1024 || update.MaxExpandedSize != 512*1024*1024 {
		t.Fatal("bootstrap release contract must track the updater's epoch and bounds")
	}
}

func TestOneCommandBootstrap(t *testing.T) {
	for _, state := range []string{
		"ready", "Intel", "piped script", "PATH accepted", "profile already configured", "profile symlink", "broken profile symlink", "profile declined", "bash profile", "unsupported shell",
		"install Node", "install Homebrew and Node", "Homebrew for tools", "Homebrew for tools declined", "Homebrew declined", "Node declined", "Homebrew failed", "Node failed", "Node still old",
		"already installed", "repair missing gh", "managed journal", "root", "Linux", "unsupported architecture", "symlink prefix", "shared prefix",
		"npm pending", "wrong npm version", "missing architecture asset", "duplicate asset", "newer incomplete", "later release page",
		"old installer", "unsigned", "unknown publisher", "bad signature", "wrong tag", "bad epoch", "extra manifest field", "wrong filename", "old macOS",
		"archive hash", "binary identity", "bundle identity", "wrong CPU", "traversal", "symlink entry", "duplicate entry", "special entry", "unexpected file", "truncated tar", "oversized entry", "HTTP redirect", "codesign failed",
	} {
		t.Run(state, func(t *testing.T) {
			cmd, answers, success := bootstrapFixture(t, state)
			output, err := runBootstrapPTY(t, cmd, answers)
			if success {
				assertReleaseExit(t, err, 0, output)
			} else if err == nil {
				t.Fatalf("unsafe/refused bootstrap succeeded: %s", output)
			}
			wantMessage := map[string]string{
				"Homebrew declined": "Homebrew declined", "Homebrew for tools declined": "Homebrew declined", "Node declined": "Node.js declined",
				"Node still old": "Node.js 24+ is still unavailable", "already installed": "Radar is already installed", "managed journal": "Existing Radar update state",
				"root": "without sudo", "Linux": "for macOS", "unsupported architecture": "Unsupported macOS architecture",
				"symlink prefix": "private-to-your-user directories", "shared prefix": "private-to-your-user directories",
				"old installer": "predates prerequisite-aware installation", "old macOS": "requires macOS 13.0+", "archive hash": "archive size/hash verification failed",
				"binary identity": "binary identity mismatch", "bundle identity": "bundle identity mismatch", "wrong CPU": "architecture mismatch",
				"traversal": "Unsafe or duplicate archive path", "duplicate entry": "Unsafe or duplicate archive path",
				"symlink entry": "special files", "special entry": "special files", "unexpected file": "Unexpected release file",
				"truncated tar": "Truncated or oversized archive", "oversized entry": "Truncated or oversized archive",
			}[state]
			if wantMessage != "" && !strings.Contains(string(output), wantMessage) {
				t.Fatalf("fixture did not reach its intended refusal/validation boundary %q: %s", wantMessage, output)
			}
			home := cmd.Dir
			calls, _ := os.ReadFile(filepath.Join(home, "calls"))
			installed := filepath.Join(home, ".local", "bin", "radar")
			if state == "already installed" || state == "repair missing gh" {
				data, _ := os.ReadFile(installed)
				if string(data) != "existing executable" || strings.Contains(string(calls), "register notifier") || strings.Contains(string(calls), "radar launched") {
					t.Fatalf("prerequisite repair changed Radar: %s; %s", data, calls)
				}
				if state == "repair missing gh" {
					if strings.Count(string(calls), "brew install gh") != 1 {
						t.Fatalf("missing repair: %s", calls)
					}
					receipt, _ := os.ReadFile(filepath.Join(home, ".local", "libexec", "radar", "install.json"))
					if string(receipt) != "preserve receipt" {
						t.Fatal("repair changed update receipt")
					}
				}
				if !strings.Contains(string(output), "Required tools are ready") {
					t.Fatalf("repair did not complete: %s", output)
				}
				return
			}
			if success {
				assertMode(t, installed, 0755)
				if !strings.Contains(string(calls), "radar launched") || !strings.Contains(string(output), "Verified Radar v1.2.3") {
					t.Fatalf("verified installation must launch the existing first-run flow: %s; %s", calls, output)
				}
				if !strings.Contains(string(calls), "register notifier") {
					t.Fatal("existing archive installer did not install/register the companion")
				}
			} else if state != "already installed" {
				if _, err := os.Lstat(installed); !os.IsNotExist(err) {
					t.Fatalf("failed bootstrap installed Radar: %s", output)
				}
				if strings.Contains(string(calls), "register notifier") || strings.Contains(string(calls), "radar launched") {
					t.Fatalf("failure crossed activation boundary: %s", calls)
				}
			}
			for _, call := range []string{"brew install node", "Homebrew installed"} {
				want := call == "brew install node" && (state == "install Node" || state == "install Homebrew and Node" || state == "Node failed" || state == "Node still old") || call == "Homebrew installed" && (state == "install Homebrew and Node" || state == "Homebrew for tools")
				if strings.Contains(string(calls), call) != want {
					t.Fatalf("prerequisite installation crossed its consent boundary: %s", calls)
				}
			}
			profile := filepath.Join(home, ".zshrc")
			if state == "bash profile" {
				profile = filepath.Join(home, ".bash_profile")
			}
			data, err := os.ReadFile(profile)
			if state == "broken profile symlink" {
				if !os.IsNotExist(err) {
					t.Fatal("broken profile link was modified")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantProfile := state == "PATH accepted" || state == "profile already configured" || state == "profile symlink" || state == "bash profile" || state == "install Node" || state == "install Homebrew and Node" || state == "Homebrew for tools"
			if !bytes.HasPrefix(data, []byte("# User-owned profile\n")) || strings.Contains(string(data), "# Radar command PATH") != wantProfile {
				t.Fatalf("profile consent/preservation violated: %s", data)
			}
			if strings.Count(string(data), "# Radar command PATH") > 1 {
				t.Fatal("PATH block was duplicated")
			}
			assertMode(t, profile, 0600)
			if state == "profile symlink" {
				if info, err := os.Lstat(profile); err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("profile symlink was replaced")
				}
			}
			if success {
				for _, name := range []string{"config.yaml", "secrets.yaml"} {
					data, err := os.ReadFile(filepath.Join(home, ".config", "radar", name))
					if err != nil || string(data) != "user-owned "+name+"\n" {
						t.Fatalf("bootstrap changed existing Radar data: %s; %v", name, err)
					}
				}
			}
		})
	}
}

func TestBootstrapCannotConsumePipedConsentWithoutTerminal(t *testing.T) {
	cmd, _, _ := bootstrapFixture(t, "install Homebrew and Node")
	cmd.Stdin = strings.NewReader("y\ny\ny\n")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "interactive terminal") {
		t.Fatalf("headless bootstrap must reject piped approval: %v; %s", err, output)
	}
	if data, _ := os.ReadFile(filepath.Join(cmd.Dir, "calls")); len(data) != 0 {
		t.Fatalf("headless bootstrap ran network/install commands: %s", data)
	}
}

// Every network/tool boundary is test-local. Only the fixture publisher key is
// substituted in a private copy of the source; production has no trust override.
func bootstrapFixture(t *testing.T, state string) (*exec.Cmd, string, bool) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to test authenticated bootstrap")
	}
	home := filepath.Join(t.TempDir(), "new user with spaces")
	bin := filepath.Join(home, "tools")
	brewPrefix := filepath.Join(home, "brew")
	for _, dir := range []string{bin, filepath.Join(brewPrefix, "bin"), filepath.Join(home, "server"), filepath.Join(home, ".config", "radar")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	bin, brewPrefix = filepath.Join(home, "tools"), filepath.Join(home, "brew")
	write := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"config.yaml", "secrets.yaml"} {
		write(filepath.Join(home, ".config", "radar", name), "user-owned "+name+"\n", 0600)
	}
	for _, name := range []string{".zshrc", ".bash_profile"} {
		write(filepath.Join(home, name), "# User-owned profile\n", 0600)
	}
	if state == "profile already configured" {
		write(filepath.Join(home, ".zshrc"), "# User-owned profile\n# Radar command PATH\nexport PATH=\"$HOME/.local/bin:$PATH\"\n", 0600)
	}
	if state == "profile symlink" || state == "broken profile symlink" {
		if err := os.Rename(filepath.Join(home, ".zshrc"), filepath.Join(home, "profile target")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("profile target", filepath.Join(home, ".zshrc")); err != nil {
			t.Fatal(err)
		}
		if state == "broken profile symlink" {
			if err := os.Remove(filepath.Join(home, "profile target")); err != nil {
				t.Fatal(err)
			}
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	roots, _ := json.Marshal(map[string]string{"fixture": base64.StdEncoding.EncodeToString(public)})
	script := string(mustReadScript(t, "../install.sh"))
	script = regexp.MustCompile(`const trustedKeys = \{[^\n]+\};`).ReplaceAllString(script, "const trustedKeys = "+string(roots)+";")
	// No host Homebrew path, Launch Services registration or actual Mach-O launch.
	script = strings.ReplaceAll(script, "native_brew=/opt/homebrew/bin/brew", "native_brew=\""+filepath.Join(brewPrefix, "bin", "brew")+"\"")
	script = strings.ReplaceAll(script, "native_brew=/usr/local/bin/brew", "native_brew=\""+filepath.Join(brewPrefix, "bin", "brew")+"\"")
	script = strings.ReplaceAll(script, `"$prefix/bin/radar" <&3`, `"$BOOTSTRAP_LAUNCH" <&3`)
	write(filepath.Join(home, "install.sh"), script, 0700)
	write(filepath.Join(home, "fetch.mjs"), `
import {readFileSync,appendFileSync} from 'node:fs';
const home=process.env.HOME, routes=JSON.parse(readFileSync(home+'/routes.json'));
globalThis.fetch=async(address, options)=>{
 const url=String(address); appendFileSync(home+'/calls','fetch '+url+'\n');
 const r=routes[url]; if(!r) return new Response('not found',{status:404});
 if(r.redirect) return new Response(null,{status:302,headers:{location:r.redirect}});
 const b=readFileSync(home+'/server/'+r.file);
 return new Response(b,{status:r.status??200,headers:{'content-length':String(b.length)}});
};
`, 0600)
	nodeStub := "#!/bin/sh\nexec \"$BOOTSTRAP_NODE\" --import \"$HOME/fetch.mjs\" \"$@\"\n"
	write(filepath.Join(home, "node.stub"), nodeStub, 0700)
	brewStub := `#!/bin/sh
case "$*" in
 --prefix) printf '%s\n' "$HOME/brew";;
 'install node')
   echo 'brew install node' >> "$HOME/calls"
   if [ "$BOOTSTRAP_STATE" = 'Node failed' ]; then exit 7; fi
   if [ "$BOOTSTRAP_STATE" != 'Node still old' ]; then cp "$HOME/node.stub" "$HOME/brew/bin/node"; fi;;
 'install gh') echo 'brew install gh' >> "$HOME/calls"; printf '#!/bin/sh\necho gh 2.70\n' > "$HOME/brew/bin/gh"; chmod +x "$HOME/brew/bin/gh";;
 'install fd') printf '#!/bin/sh\necho fd 10.0\n' > "$HOME/brew/bin/fd"; chmod +x "$HOME/brew/bin/fd";;
 *) echo 'unexpected brew invocation' >&2; exit 99;;
esac
`
	write(filepath.Join(home, "brew.stub"), brewStub, 0700)
	for _, name := range []string{"git", "tmux", "fd", "npm", "pi", "gh"} {
		if (state == "Homebrew for tools" || state == "Homebrew for tools declined") && name == "fd" {
			continue
		}
		version := "ready"
		if name == "tmux" {
			version = "tmux 3.6"
		}
		if name == "pi" {
			version = "0.85.1"
		}
		write(filepath.Join(bin, name), "#!/bin/sh\necho '"+version+"'\n", 0700)
	}
	write(filepath.Join(bin, "uname"), `#!/bin/sh
if [ "$1" = -s ]; then
 if [ "$BOOTSTRAP_STATE" = Linux ]; then echo Linux; else echo Darwin; fi
else
 if [ "$BOOTSTRAP_STATE" = 'unsupported architecture' ]; then echo unknown; elif [ "$BOOTSTRAP_STATE" = Intel ]; then echo x86_64; else echo arm64; fi
fi
`, 0700)
	write(filepath.Join(bin, "id"), "#!/bin/sh\nif [ \"$BOOTSTRAP_STATE\" = root ]; then echo 0; else echo 501; fi\n", 0700)
	write(filepath.Join(bin, "sw_vers"), "#!/bin/sh\nif [ \"$BOOTSTRAP_STATE\" = 'old macOS' ]; then echo 12.0; else echo 26.0; fi\n", 0700)
	write(filepath.Join(bin, "curl"), `#!/bin/sh
out=
while [ $# -gt 0 ]; do
 if [ "$1" = --output ]; then shift; out=$1; fi
 shift
done
printf 'curl Homebrew\n' >> "$HOME/calls"
cat > "$out" <<'BREW'
#!/bin/sh
if [ "$BOOTSTRAP_STATE" = 'Homebrew failed' ]; then exit 7; fi
cp "$HOME/brew.stub" "$HOME/brew/bin/brew"
echo 'Homebrew installed' >> "$HOME/calls"
BREW
`, 0700)
	write(filepath.Join(bin, "codesign"), "#!/bin/sh\necho codesign >> \"$HOME/calls\"\nif [ \"$BOOTSTRAP_STATE\" = 'codesign failed' ]; then exit 7; fi\n", 0700)
	write(filepath.Join(bin, "register"), "#!/bin/sh\necho 'register notifier' >> \"$HOME/calls\"\n", 0700)
	write(filepath.Join(bin, "launch"), "#!/bin/sh\necho 'radar launched' >> \"$HOME/calls\"\n", 0700)
	missingNode := state == "install Node" || state == "install Homebrew and Node" || state == "Homebrew declined" || state == "Node declined" || state == "Homebrew failed" || state == "Node failed" || state == "Node still old"
	if !missingNode {
		write(filepath.Join(bin, "node"), nodeStub, 0700)
	}
	if missingNode && state != "install Homebrew and Node" && state != "Homebrew declined" && state != "Homebrew failed" {
		write(filepath.Join(bin, "brew"), brewStub, 0700)
	}
	if state == "already installed" || state == "repair missing gh" {
		if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0700); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(home, ".local", "bin", "radar"), "existing executable", 0700)
	}
	if state == "repair missing gh" {
		write(filepath.Join(bin, "gh"), "#!/bin/sh\nexit 1\n", 0700)
		write(filepath.Join(bin, "brew"), brewStub, 0700)
		if err := os.MkdirAll(filepath.Join(home, ".local", "libexec", "radar"), 0700); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(home, ".local", "libexec", "radar", "install.json"), "preserve receipt", 0600)
	}
	if state == "managed journal" {
		if err := os.MkdirAll(filepath.Join(home, ".local", "libexec", "radar", ".upgrade"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if state == "shared prefix" {
		if err := os.Mkdir(filepath.Join(home, ".local"), 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(home, ".local"), 0777); err != nil {
			t.Fatal(err)
		}
	}
	if state == "symlink prefix" {
		if err := os.Symlink(brewPrefix, filepath.Join(home, ".local")); err != nil {
			t.Fatal(err)
		}
	}
	routes := map[string]map[string]any{}
	put := func(url, name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, "server", name), data, 0600); err != nil {
			t.Fatal(err)
		}
		routes[url] = map[string]any{"file": name}
	}
	publish := func(version string) map[string]any {
		t.Helper()
		artifacts := map[string]update.Artifact{}
		assets := []map[string]any{}
		for _, arch := range []string{"arm64", "amd64"} {
			archive, binaryHash, treeHash := bootstrapArchive(t, version, arch, state, bin)
			file := "radar_" + version + "_darwin_" + arch + ".tar.gz"
			artifacts[arch] = update.Artifact{File: file, Size: int64(len(archive)), SHA256: bootstrapHash(archive), BinarySHA256: binaryHash, NotifierVersion: "1.0.0", NotifierSHA256: treeHash}
			if state == "archive hash" && arch == "arm64" {
				archive = append([]byte(nil), archive...)
				archive[len(archive)-1] ^= 1
			}
			put("https://github.com/ChristianMoesl/radar/releases/download/"+version+"/"+file, version+arch+".tar.gz", archive)
			assets = append(assets, map[string]any{"name": file, "size": artifacts[arch].Size, "state": "uploaded"})
		}
		if state == "binary identity" {
			a := artifacts["arm64"]
			a.BinarySHA256 = strings.Repeat("a", 64)
			artifacts["arm64"] = a
		}
		if state == "bundle identity" {
			a := artifacts["arm64"]
			a.NotifierSHA256 = strings.Repeat("a", 64)
			artifacts["arm64"] = a
		}
		if state == "wrong filename" {
			a := artifacts["arm64"]
			a.File = "../wrong.tar.gz"
			artifacts["arm64"] = a
		}
		m := update.Manifest{Schema: 1, Version: version, StateEpoch: update.StateEpoch, MinMacOS: "13.0", PiVersion: strings.TrimPrefix(version, "v"), MinPi: "0.85.1", MinNode: 24, Artifacts: artifacts}
		if state == "wrong tag" {
			m.Version = "v9.9.9"
		}
		if state == "bad epoch" {
			m.StateEpoch++
		}
		data, _ := json.Marshal(m)
		if state == "extra manifest field" {
			data = append(data[:len(data)-1], []byte(`,"unknown":true}`)...)
		}
		sig := ed25519.Sign(private, data)
		if state == "bad signature" {
			sig[0] ^= 1
		}
		id := "fixture"
		if state == "unknown publisher" {
			id = "untrusted"
		}
		signature, _ := json.Marshal(update.Signature{KeyID: id, Signature: base64.StdEncoding.EncodeToString(sig)})
		base := "https://github.com/ChristianMoesl/radar/releases/download/" + version
		put(base+"/release.json", version+".json", data)
		if state != "unsigned" {
			put(base+"/release.json.sig", version+".sig", signature)
		}
		if state == "HTTP redirect" {
			routes[base+"/release.json"] = map[string]any{"redirect": "http://untrusted.example/metadata"}
		}
		pkg, _ := json.Marshal(map[string]string{"name": "@christianmoesl/pi-radar", "version": m.PiVersion})
		if state == "wrong npm version" {
			pkg = []byte(`{"name":"@christianmoesl/pi-radar","version":"9.9.9"}`)
		}
		if state != "npm pending" {
			put("https://registry.npmjs.org/%40christianmoesl%2Fpi-radar/"+m.PiVersion, version+"-npm.json", pkg)
		}
		if state == "missing architecture asset" || state == "newer incomplete" && version == "v1.2.4" {
			assets = assets[:1]
		}
		if state == "duplicate asset" {
			assets = append(assets, assets[0])
		}
		return map[string]any{"tag_name": version, "draft": false, "prerelease": false, "assets": assets}
	}
	releases := []map[string]any{publish("v1.2.3")}
	if state == "newer incomplete" {
		releases = append(releases, publish("v1.2.4"))
	}
	if state == "later release page" {
		page2, _ := json.Marshal(releases)
		put("https://api.github.com/repos/ChristianMoesl/radar/releases?per_page=100&page=2", "page2.json", page2)
		releases = []map[string]any{}
		for i := 0; i < 100; i++ {
			releases = append(releases, map[string]any{"tag_name": "notifier-v1.0.0", "draft": false, "prerelease": false})
		}
	}
	listing, _ := json.Marshal(releases)
	put("https://api.github.com/repos/ChristianMoesl/radar/releases?per_page=100&page=1", "page1.json", listing)
	routeJSON, _ := json.Marshal(routes)
	write(filepath.Join(home, "routes.json"), string(routeJSON), 0600)
	answers, success := "n\n", true
	switch state {
	case "PATH accepted", "profile already configured", "profile symlink", "broken profile symlink", "bash profile":
		answers = "y\n"
	case "repair missing gh":
		answers = "\n"
	case "install Node":
		answers = "\ny\n"
	case "install Homebrew and Node":
		answers = "y\ny\ny\n"
	case "Homebrew for tools":
		answers = "y\n\ny\n"
	case "Homebrew declined", "Homebrew for tools declined", "Node declined":
		success = false
	case "Homebrew failed", "Node failed", "Node still old":
		answers = "y\n"
		success = false
	case "already installed", "ready", "Intel", "piped script", "profile declined", "unsupported shell", "newer incomplete", "later release page":
	default:
		success = false
	}
	shell := "/bin/zsh"
	if state == "bash profile" {
		shell = "/bin/bash"
	}
	if state == "unsupported shell" {
		shell = "/usr/local/bin/fish"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "/bin/bash", filepath.Join(home, "install.sh"))
	cmd.Dir = home
	if state == "piped script" {
		cmd.Args = []string{"/bin/bash"}
		cmd.Stdin = strings.NewReader(script)
	}
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin", "SHELL=" + shell, "BOOTSTRAP_NODE=" + node, "BOOTSTRAP_STATE=" + state, "BOOTSTRAP_LAUNCH=" + filepath.Join(bin, "launch"), "NODE_OPTIONS=--import /must-not-run"}
	return cmd, answers, success
}

func bootstrapArchive(t *testing.T, version, arch, state, tools string) ([]byte, string, string) {
	t.Helper()
	root := "radar_" + version + "_darwin_" + arch
	binaryData := make([]byte, 64)
	binary.LittleEndian.PutUint32(binaryData, 0xfeedfacf)
	cpu := uint32(0x0100000c)
	if arch == "amd64" || state == "wrong CPU" {
		cpu = 0x01000007
	}
	binary.LittleEndian.PutUint32(binaryData[4:], cpu)
	files := map[string][]byte{"bin/radar": binaryData, "README.md": []byte("fixture"), "LICENSE": []byte("fixture license"), "share/radar/AGENTS.md": []byte("fixture default instructions"), "install.sh": bundledInstaller(t), "install-agent-instructions.sh": mustReadScript(t, "install-agent-instructions.sh")}
	if state == "old installer" {
		files["install.sh"] = []byte("#!/bin/sh\nexit 99\n")
	}
	notifier := string(mustReadScript(t, "install-notifier.sh"))
	notifier = strings.ReplaceAll(notifier, "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister", filepath.Join(tools, "register"))
	// Quote the test path with spaces, just as the production absolute path has no spaces.
	notifier = strings.ReplaceAll(notifier, filepath.Join(tools, "register")+" -f", "\""+filepath.Join(tools, "register")+"\" -f")
	files["install-notifier.sh"] = []byte(notifier)
	files["libexec/radar/RadarNotifier.app/Contents/MacOS/radar-notifier"] = binaryData
	bundleRoot := filepath.Join(t.TempDir(), "app")
	for path, data := range files {
		if !strings.HasPrefix(path, "libexec/radar/RadarNotifier.app/") {
			continue
		}
		rel := strings.TrimPrefix(path, "libexec/radar/RadarNotifier.app/")
		dest := filepath.Join(bundleRoot, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, data, 0755); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := update.TreeDigest(bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for path, data := range files {
		mode := int64(0644)
		if strings.HasSuffix(path, ".sh") || strings.Contains(path, "MacOS/") || path == "bin/radar" {
			mode = 0755
		}
		h := &tar.Header{Name: root + "/" + path, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if state == "traversal" || state == "symlink entry" || state == "duplicate entry" || state == "special entry" || state == "unexpected file" || state == "oversized entry" {
		h := &tar.Header{Name: root + "/libexec/radar/RadarNotifier.app/Contents/extra", Mode: 0644, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		switch state {
		case "traversal":
			h.Name = root + "/../outside"
		case "symlink entry":
			h.Typeflag = tar.TypeSymlink
			h.Linkname = "/tmp/outside"
		case "duplicate entry":
			h.Name = root + "/README.md"
		case "special entry":
			h.Typeflag = tar.TypeFifo
		case "unexpected file":
			h.Name = root + "/unexpected"
		case "oversized entry":
			h.Size = update.MaxExpandedSize + 1
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if state == "oversized entry" { // Intentionally truncated after a declared oversized entry.
		_ = gz.Close()
	} else {
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	data := out.Bytes()
	if state == "truncated tar" {
		plain, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(plain)
		if err != nil {
			t.Fatal(err)
		}
		_ = plain.Close()
		var short bytes.Buffer
		g := gzip.NewWriter(&short)
		_, _ = g.Write(b[:700])
		_ = g.Close()
		data = short.Bytes()
	}
	return data, bootstrapHash(binaryData), tree
}

func bootstrapHash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func runBootstrapPTY(t *testing.T, cmd *exec.Cmd, answers string) ([]byte, error) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	before, err := term.GetState(master.Fd())
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Stdin == nil {
		cmd.Stdin = slave
	}
	cmd.Stdout, cmd.Stderr = slave, slave
	// Piped scripts use stdin for source, but stdout is still the controlling
	// terminal. Consent is read through /dev/tty, never consumed from that pipe.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 1}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var output []byte
	done := make(chan struct{})
	go func() { output, _ = io.ReadAll(master); close(done) }()
	if _, err := master.Write([]byte(answers)); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	after, stateErr := term.GetState(master.Fd())
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	_ = slave.Close()
	<-done
	if !reflect.DeepEqual(before, after) {
		t.Fatal("bootstrap changed the caller's terminal modes")
	}
	return output, err
}
