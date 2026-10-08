package scripts_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Exercise the real Bash publisher, inventory selector and Ed25519 verifier.
// Only gh and the Swift build are fake; keys, releases and tags are test-local.
func TestNotifierReleasePreservesHistoryAndRefs(t *testing.T) {
	for _, state := range []string{
		"new", "new missing tag", "published", "published with historical draft", "complete draft",
		"retired", "ambiguous published", "ambiguous drafts", "missing tag",
		"inventory failure", "download failure", "missing asset", "duplicate asset", "unexpected asset",
		"untrusted signature", "changed inputs", "archive tampering", "bundle tampering",
		"create failure", "new publication failure", "draft publication failure", "invalid inventory", "invalid history",
	} {
		t.Run(state, func(t *testing.T) {
			cmd := notifierFixture(t, state)
			before := publishedFixtureBytes(t, cmd.Dir)
			output, err := cmd.CombinedOutput()
			wantExit := 1
			switch state {
			case "new", "published", "published with historical draft", "complete draft":
				wantExit = 0
			case "inventory failure", "download failure", "missing tag", "new missing tag", "create failure", "new publication failure", "draft publication failure":
				wantExit = 7
			}
			assertReleaseExit(t, err, wantExit, output)
			calls := readNotifierCalls(t, cmd.Dir)
			var creates, edits, patches, downloads int
			for _, args := range calls {
				if len(args) < 2 {
					t.Fatalf("invalid fixture command: %q", args)
				}
				switch {
				case args[0] == "release" && args[1] == "create":
					creates++
					if args[2] != "notifier-v1.2.3" || !containsArg(args, "--verify-tag") || containsArg(args, "--target") || !containsArg(args, "--draft") {
						t.Fatalf("CI must not create its own component ref: %q", args)
					}
				case args[0] == "release" && args[1] == "edit":
					edits++
				case args[0] == "api" && containsArg(args, "PATCH"):
					patches++
					if !reflect.DeepEqual(args, []string{"api", "repos/ChristianMoesl/radar/releases/42", "--method", "PATCH", "-F", "draft=false", "-f", "make_latest=false"}) {
						t.Fatalf("only the verified never-published draft may be resumed: %q", args)
					}
				case args[0] == "api" && strings.Contains(args[1], "/releases/assets/"):
					downloads++
					if !reflect.DeepEqual(args[2:], []string{"--header", "Accept: application/octet-stream"}) {
						t.Fatalf("asset IDs must be downloaded as bytes: %q", args)
					}
				}
			}
			wantCreates, wantEdits, wantPatches := 0, 0, 0
			if state == "new" || state == "create failure" || state == "new publication failure" {
				wantCreates = 1
				if state != "create failure" {
					wantEdits = 1
				}
			}
			if state == "complete draft" || state == "draft publication failure" {
				wantPatches = 1
			}
			if creates != wantCreates || edits != wantEdits || patches != wantPatches {
				t.Fatalf("unexpected release mutations for %s: %q", state, calls)
			}
			built, err := os.ReadFile(filepath.Join(cmd.Dir, "built"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if (len(built) > 0) != (state == "new" || state == "create failure" || state == "new publication failure") {
				t.Fatalf("existing or unavailable components must never be rebuilt: %s; %s", built, output)
			}
			if state == "published" || state == "published with historical draft" || state == "complete draft" {
				if downloads != 4 {
					t.Fatalf("expected all four selected immutable asset IDs, got %d", downloads)
				}
			}
			if !reflect.DeepEqual(publishedFixtureBytes(t, cmd.Dir), before) {
				t.Fatal("existing server assets were changed")
			}
			outputDir := filepath.Join(cmd.Dir, "build", "release-notifier")
			if wantExit != 0 {
				if _, err := os.Stat(outputDir); !os.IsNotExist(err) {
					t.Fatal("failed component preparation replaced the reusable artifact directory")
				}
			} else if state != "new" {
				for name, data := range before {
					got, err := os.ReadFile(filepath.Join(outputDir, name))
					if err != nil || string(got) != data {
						t.Fatalf("published bytes not reused exactly for %s: %v", name, err)
					}
				}
			}
		})
	}
}

func TestNotifierFailureKeepsPreviousPreparedDirectory(t *testing.T) {
	cmd := notifierFixture(t, "untrusted signature")
	directory := filepath.Join(cmd.Dir, "build", "release-notifier")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "previous-artifact")
	if err := os.WriteFile(file, []byte("previous verified bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := cmd.CombinedOutput()
	assertReleaseExit(t, err, 1, output)
	if data, err := os.ReadFile(file); err != nil || string(data) != "previous verified bytes" {
		t.Fatalf("failed preparation destroyed previous artifacts: %v", err)
	}
}

func notifierFixture(t *testing.T, state string) *exec.Cmd {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to test component authentication")
	}
	root := filepath.Join(t.TempDir(), "notifier fixture with spaces")
	for _, dir := range []string{"scripts", "bin", "macos/RadarNotifier", "internal/update", "published", "seed"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"prepare-release-notifier.sh", "notifier-release-state.mjs", "release-metadata.mjs"} {
		if err := os.WriteFile(filepath.Join(root, "scripts", file), mustReadScript(t, file), 0700); err != nil {
			t.Fatal(err)
		}
	}
	builder := `#!/bin/sh
printf '%s\n' "$*" >> "$HOME/built"
mkdir -p "$1/Contents/MacOS"
printf 'fixture notifier %s\n' "$2" > "$1/Contents/MacOS/notifier"
chmod 755 "$1/Contents/MacOS/notifier"
`
	if err := os.WriteFile(filepath.Join(root, "scripts", "build-notifier-app.sh"), []byte(builder), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "macos", "RadarNotifier", "VERSION"), []byte("1.2.3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	trust, _ := json.Marshal(map[string]string{"fixture": base64.StdEncoding.EncodeToString(public)})
	if err := os.WriteFile(filepath.Join(root, "internal", "update", "keys.json"), trust, 0600); err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		directory := filepath.Join(root, "seed", arch, "RadarNotifier.app", "Contents", "MacOS")
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		contents := []byte("fixture notifier " + arch + "\n")
		if err := os.WriteFile(filepath.Join(directory, "notifier"), contents, 0755); err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(root, "seed", "radar-notifier_1.2.3_"+arch+".tar.gz")
		writeNotifierArchive(t, archive, contents)
	}
	if err := os.Symlink(node, filepath.Join(root, "bin", "node")); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + root, "PATH=" + filepath.Join(root, "bin") + ":/usr/bin:/bin", "RADAR_RELEASE_KEY_ID=fixture", "RADAR_RELEASE_SIGNING_KEY=" + string(key), "NOTIFIER_FIXTURE=" + root, "NOTIFIER_STATE=" + state}
	sign := exec.Command(node, filepath.Join(root, "scripts", "release-metadata.mjs"), "component", filepath.Join(root, "seed"))
	sign.Env = env
	if output, err := sign.CombinedOutput(); err != nil {
		t.Fatalf("fixture signing failed: %v; %s", err, output)
	}
	files := []string{"notifier.json", "notifier.json.sig", "radar-notifier_1.2.3_amd64.tar.gz", "radar-notifier_1.2.3_arm64.tar.gz"}
	assets := make([]map[string]any, 0, len(files))
	for i, name := range files {
		data, err := os.ReadFile(filepath.Join(root, "seed", name))
		if err != nil {
			t.Fatal(err)
		}
		if state == "archive tampering" && i == 2 {
			data = []byte("not the authenticated archive")
		}
		if state == "untrusted signature" && i == 1 {
			data = []byte(`{"key_id":"untrusted","signature":"AA=="}`)
		}
		if err := os.WriteFile(filepath.Join(root, "published", name), data, 0600); err != nil {
			t.Fatal(err)
		}
		assets = append(assets, map[string]any{"id": i + 100, "name": name})
	}
	if state == "changed inputs" {
		if err := os.WriteFile(filepath.Join(root, "macos", "RadarNotifier", "Info.plist"), []byte("changed input"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if state == "bundle tampering" {
		data := []byte("fixture notifier amd64\n")
		writeNotifierArchive(t, filepath.Join(root, "published", files[2]), append(data, 'x'))
		// Re-sign the archive hash but keep the original expected extracted tree.
		resign := exec.Command(node, "--input-type=module", "-e", `
import {readFileSync,writeFileSync} from 'node:fs'; import {createHash,sign,createPrivateKey} from 'node:crypto';
const dir=process.argv[1]; const m=JSON.parse(readFileSync(dir+'/notifier.json')); const b=readFileSync(dir+'/'+m.artifacts.amd64.file);
m.artifacts.amd64.size=b.length; m.artifacts.amd64.sha256=createHash('sha256').update(b).digest('hex');
const bytes=Buffer.from(JSON.stringify(m)+'\n'); writeFileSync(dir+'/notifier.json',bytes);
writeFileSync(dir+'/notifier.json.sig',JSON.stringify({key_id:'fixture',signature:sign(null,bytes,createPrivateKey(process.env.RADAR_RELEASE_SIGNING_KEY)).toString('base64')}));
`, filepath.Join(root, "published"))
		resign.Env = env
		if output, err := resign.CombinedOutput(); err != nil {
			t.Fatalf("fixture resigning failed: %v; %s", err, output)
		}
	}
	if state == "missing asset" {
		assets = assets[:3]
	}
	if state == "duplicate asset" {
		assets[3] = assets[2]
	}
	if state == "unexpected asset" {
		assets[3] = map[string]any{"id": 103, "name": "../unexpected.tar.gz"}
	}
	release := map[string]any{"id": 42, "tag_name": "notifier-v1.2.3", "draft": false, "published_at": "2026-01-01T00:00:00Z", "assets": assets}
	matches := []map[string]any{release}
	switch state {
	case "new", "new missing tag", "create failure", "new publication failure":
		matches = []map[string]any{}
	case "complete draft", "draft publication failure", "ambiguous drafts":
		release["draft"], release["published_at"] = true, nil
	case "retired":
		release["draft"] = true
	}
	if state == "ambiguous published" || state == "ambiguous drafts" || state == "published with historical draft" {
		other := map[string]any{"id": 41, "tag_name": "notifier-v1.2.3", "draft": release["draft"], "published_at": release["published_at"], "assets": assets}
		if state == "published with historical draft" {
			other["draft"] = true
			oldAssets := make([]map[string]any, 0, len(assets))
			for i, asset := range assets {
				oldAssets = append(oldAssets, map[string]any{"id": 9000 + i, "name": asset["name"]})
			}
			other["assets"] = oldAssets
		}
		matches = append(matches, other)
	}
	if state == "invalid history" {
		release["published_at"] = false
	}
	// Put matches on a later page so missing --paginate/--slurp cannot hide history.
	inventory, _ := json.Marshal([][]map[string]any{{{"id": 30, "tag_name": "v1.0.0"}}, matches})
	if state == "invalid inventory" {
		inventory = []byte(`{"message":"not a release inventory"}`)
	}
	if err := os.WriteFile(filepath.Join(root, "inventory.json"), inventory, 0600); err != nil {
		t.Fatal(err)
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\nexec \"$NOTIFIER_HELPER\" -test.run=^TestNotifierReleaseGHHelper$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(root, "bin", "gh"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	env = append(env, "NOTIFIER_HELPER="+helper)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "/bin/bash", filepath.Join(root, "scripts", "prepare-release-notifier.sh"))
	cmd.Dir, cmd.Env = root, env
	return cmd
}

func writeNotifierArchive(t *testing.T, name string, contents []byte) {
	t.Helper()
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tarball := tar.NewWriter(gz)
	if err := tarball.WriteHeader(&tar.Header{Name: "RadarNotifier.app/Contents/MacOS/notifier", Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarball.Write(contents); err != nil {
		t.Fatal(err)
	}
	for _, closer := range []interface{ Close() error }{tarball, gz, file} {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func readNotifierCalls(t *testing.T, root string) [][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, args)
	}
	return calls
}

func TestNotifierReleaseGHHelper(t *testing.T) {
	root := os.Getenv("NOTIFIER_FIXTURE")
	if root == "" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	log, err := os.OpenFile(filepath.Join(root, "calls.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(log).Encode(args); err != nil {
		panic(err)
	}
	_ = log.Close()
	state := os.Getenv("NOTIFIER_STATE")
	switch {
	case reflect.DeepEqual(args, []string{"api", "repos/ChristianMoesl/radar/releases", "--paginate", "--slurp"}):
		if state == "inventory failure" {
			os.Exit(7)
		}
		fmt.Print(string(mustReadFixture(t, root, "inventory.json")))
	case reflect.DeepEqual(args, []string{"api", "repos/ChristianMoesl/radar/git/ref/tags/notifier-v1.2.3"}):
		if state == "missing tag" || state == "new missing tag" {
			fmt.Fprintln(os.Stderr, "HTTP 404")
			os.Exit(7)
		}
		fmt.Print(`{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
	case len(args) >= 2 && args[0] == "api" && strings.Contains(args[1], "/releases/assets/"):
		if state == "download failure" {
			os.Exit(7)
		}
		ids := map[string]string{"100": "notifier.json", "101": "notifier.json.sig", "102": "radar-notifier_1.2.3_amd64.tar.gz", "103": "radar-notifier_1.2.3_arm64.tar.gz"}
		name := ids[strings.TrimPrefix(args[1], "repos/ChristianMoesl/radar/releases/assets/")]
		if name == "" {
			panic("unselected historical release asset ID was requested")
		}
		os.Stdout.Write(mustReadFixture(t, filepath.Join(root, "published"), name))
	case len(args) >= 2 && args[0] == "api" && args[1] == "repos/ChristianMoesl/radar/releases/42":
		if state == "draft publication failure" {
			os.Exit(7)
		}
	case len(args) >= 3 && args[0] == "release" && args[1] == "create":
		if state == "create failure" {
			os.Exit(7)
		}
	case len(args) >= 3 && args[0] == "release" && args[1] == "edit":
		if state == "new publication failure" {
			os.Exit(7)
		}
	default:
		panic(fmt.Sprintf("unexpected gh command: %q", args))
	}
	os.Exit(0)
}

func mustReadFixture(t *testing.T, root, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
