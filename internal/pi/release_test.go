package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/update"
)

func releaseFixture(t *testing.T, packages string) (string, string) {
	t.Helper()
	home := t.TempDir()
	profile := filepath.Join(home, "agent")
	cwd := filepath.Join(home, "workspace")
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", profile)
	for _, p := range []string{profile, cwd} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(t, filepath.Join(profile, "settings.json"), `{"packages":`+packages+`}`)
	for _, name := range []string{update.Package, SandboxPackage} {
		writeFixture(t, filepath.Join(profile, "npm/node_modules", name, "package.json"), `{"name":"`+name+`","version":"0.1.0"}`)
	}
	return profile, cwd
}

func writeFixture(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseSourceClassification(t *testing.T) {
	for _, name := range []string{update.Package, SandboxPackage} {
		for _, tc := range []struct {
			packages string
			want     bool
		}{
			{`[]`, false}, {`["npm:PACKAGE"]`, true}, {`[{"source":"npm:PACKAGE"}]`, true},
			{`["npm:PACKAGE@0.1.0"]`, false}, {`["npm:PACKAGE@latest"]`, false},
			{`[{"source":"npm:PACKAGE","extensions":[]}]`, false}, {`[{"source":"npm:PACKAGE","autoload":false}]`, false},
			{`["git:github.com/ChristianMoesl/radar"]`, false}, {`["npm:unrelated"]`, false},
			{`["npm:PACKAGE","npm:PACKAGE"]`, false},
		} {
			t.Run(name+tc.packages, func(t *testing.T) {
				_, cwd := releaseFixture(t, strings.ReplaceAll(tc.packages, "PACKAGE", name))
				s := InspectRelease(cwd, name)
				if s.CanUpdate != tc.want {
					t.Fatalf("%+v", s)
				}
			})
		}
	}
}

func TestProjectOverrideAndDevelopmentSymlinkAreNotUpdated(t *testing.T) {
	for _, name := range []string{update.Package, SandboxPackage} {
		t.Run(name, func(t *testing.T) {
			profile, cwd := releaseFixture(t, `["npm:`+name+`"]`)
			writeFixture(t, filepath.Join(cwd, ".pi/settings.json"), `{"packages":["npm:`+name+`"]}`)
			if s := InspectRelease(cwd, name); s.CanUpdate || !strings.Contains(s.Reason, "Project-local") {
				t.Fatal(s)
			}
			os.Remove(filepath.Join(cwd, ".pi/settings.json"))
			packagePath := filepath.Join(profile, "npm/node_modules", name)
			local := filepath.Join(t.TempDir(), "development")
			if err := os.Rename(packagePath, local); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(local, packagePath); err != nil {
				t.Fatal(err)
			}
			if s := InspectRelease(cwd, name); s.CanUpdate || !strings.Contains(s.Reason, "development") {
				t.Fatal(s)
			}
		})
	}
}

func TestLocalDisabledMissingAndMalformedPackagesAreNotUpdated(t *testing.T) {
	for _, name := range []string{update.Package, SandboxPackage} {
		for _, kind := range []string{"local", "disabled", "missing", "malformed", "wrong-name", "project-parent", "bad-settings"} {
			t.Run(name+kind, func(t *testing.T) {
				profile, cwd := releaseFixture(t, `["npm:`+name+`"]`)
				manifest := filepath.Join(profile, "npm/node_modules", name, "package.json")
				switch kind {
				case "local":
					writeFixture(t, filepath.Join(profile, "extensions", strings.TrimPrefix(name, "@christianmoesl/")+".ts"), "// local")
				case "disabled":
					writeFixture(t, filepath.Join(profile, "settings.json"), `{"packages":["npm:`+name+`"],"extensions":["-npm:`+name+`"]}`)
				case "missing":
					os.RemoveAll(filepath.Dir(manifest))
				case "malformed":
					writeFixture(t, manifest, "{")
				case "wrong-name":
					writeFixture(t, manifest, `{"name":"other","version":"1.2.3"}`)
				case "project-parent":
					writeFixture(t, filepath.Join(filepath.Dir(cwd), ".pi/settings.json"), `{"packages":["npm:`+name+`"]}`)
				case "bad-settings":
					writeFixture(t, filepath.Join(profile, "settings.json"), "{")
				}
				if s := InspectRelease(cwd, name); s.CanUpdate {
					t.Fatal(s)
				}
			})
		}
	}
}

func TestLoadedVersionIsSeparateAndStaleReportsAreIgnored(t *testing.T) {
	profile, cwd := releaseFixture(t, `["npm:@christianmoesl/pi-radar","npm:@christianmoesl/pi-sbx"]`)
	dir := filepath.Join(profile, "radar/loaded")
	r := LoadedRelease{PID: os.Getpid(), Version: "0.0.9", Profile: profile, CWD: cwd, Updated: time.Now().UnixMilli()}
	data, _ := json.Marshal(r)
	writeFixture(t, filepath.Join(dir, "live.json"), string(data))
	r.Updated -= 600000
	data, _ = json.Marshal(r)
	writeFixture(t, filepath.Join(dir, "stale.json"), string(data))
	s := InspectRelease(cwd, update.Package)
	if s.Installed != "0.1.0" || len(s.Loaded) != 1 || s.Loaded[0].Version != "0.0.9" {
		t.Fatal(s)
	}
	if s := InspectRelease(cwd, SandboxPackage); !s.CanUpdate || len(s.Loaded) != 0 {
		t.Fatal(s)
	}
}

func TestAllPinsAreLeftAloneRegardlessOfOldMarker(t *testing.T) {
	profile, cwd := releaseFixture(t, `["npm:@christianmoesl/pi-radar@0.1.0"]`)
	writeFixture(t, filepath.Join(profile, "radar/release-pin.json"), `{"source":"npm:@christianmoesl/pi-radar@0.1.0"}`)
	if s := InspectRelease(cwd, update.Package); s.CanUpdate {
		t.Fatal(s)
	}
}

func fakePi(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(bin, "node"), []byte("#!/bin/sh\necho v24.0.0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.0.0; exit 0; fi\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestPiUpdateTargetsOnlyUnpinnedHostPackage(t *testing.T) {
	for _, name := range []string{update.Package, SandboxPackage} {
		t.Run(name, func(t *testing.T) {
			profile, cwd := releaseFixture(t, `["npm:`+name+`","npm:other"]`)
			settings, _ := os.ReadFile(filepath.Join(profile, "settings.json"))
			fakePi(t, `
[ "$1" = update ] && [ "$2" = npm:`+name+` ] && [ "$3" = --no-approve ] && [ "$#" = 3 ] || exit 9
[ "$(pwd -P)" = "$(cd "$HOME" && pwd -P)" ] || exit 10
printf '%s\n' "$@" > "$PI_CODING_AGENT_DIR/command"
cat > "$PI_CODING_AGENT_DIR/npm/node_modules/`+name+`/package.json" <<'JSON'
{"name":"`+name+`","version":"0.1.1"}
JSON
`)
			state, err := updateRelease(context.Background(), InspectRelease(cwd, name), cwd, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !state.CanUpdate || state.Installed != "0.1.1" || state.Source != "npm:"+name {
				t.Fatal(state)
			}
			actual, _ := os.ReadFile(filepath.Join(profile, "settings.json"))
			if string(actual) != string(settings) {
				t.Fatal("changed declarations")
			}
			if _, err := os.Stat(filepath.Join(profile, "radar/release-pin.json")); !os.IsNotExist(err) {
				t.Fatal("wrote a pin marker")
			}
		})
	}
}

func TestPiUpdateDetectsChangesAndFailures(t *testing.T) {
	for _, kind := range []string{"failure", "cancel", "changed-before", "pinned-after", "downgrade", "wrong-name", "unchanged"} {
		t.Run(kind, func(t *testing.T) {
			profile, cwd := releaseFixture(t, `["npm:@christianmoesl/pi-radar"]`)
			before := InspectRelease(cwd, update.Package)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			script := `echo called > "$PI_CODING_AGENT_DIR/command"` + "\n"
			switch kind {
			case "failure":
				script += "exit 1\n"
			case "cancel":
				cancel()
			case "changed-before":
				writeFixture(t, filepath.Join(profile, "settings.json"), `{"packages":["npm:@christianmoesl/pi-radar@0.1.0"]}`)
			case "pinned-after":
				script += `echo '{"packages":["npm:@christianmoesl/pi-radar@0.1.1"]}' > "$PI_CODING_AGENT_DIR/settings.json"`
			case "downgrade":
				script += `echo '{"name":"@christianmoesl/pi-radar","version":"0.0.1"}' > "$PI_CODING_AGENT_DIR/npm/node_modules/@christianmoesl/pi-radar/package.json"`
			case "wrong-name":
				script += `echo '{"name":"other","version":"0.1.1"}' > "$PI_CODING_AGENT_DIR/npm/node_modules/@christianmoesl/pi-radar/package.json"`
			}
			fakePi(t, script)
			_, err := updateRelease(ctx, before, cwd, nil)
			if (err == nil) != (kind == "unchanged") {
				t.Fatalf("%s: %v", kind, err)
			}
			if kind == "changed-before" || kind == "cancel" {
				if _, err := os.Stat(filepath.Join(profile, "command")); !os.IsNotExist(err) {
					t.Fatal("ran pi")
				}
			}
			if kind == "failure" && !strings.Contains(err.Error(), "may have partially changed") {
				t.Fatal(err)
			}
		})
	}
}
