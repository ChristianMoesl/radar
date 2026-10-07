package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func releaseFixture(t *testing.T, packages string) (string, string) {
	t.Helper()
	home := t.TempDir()
	profile := filepath.Join(home, "agent")
	cwd := filepath.Join(home, "workspace")
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", profile)
	for _, p := range []string{profile, cwd, filepath.Join(profile, "npm/node_modules/@christianmoesl/pi-radar")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(profile, "settings.json"), []byte(`{"packages":`+packages+`}`), 0600)
	os.WriteFile(filepath.Join(profile, "npm/node_modules/@christianmoesl/pi-radar/package.json"), []byte(`{"name":"@christianmoesl/pi-radar","version":"0.1.0"}`), 0600)
	return profile, cwd
}
func TestReleaseSourceClassification(t *testing.T) {
	for _, test := range []struct {
		packages string
		want     bool
	}{
		{`[]`, false}, {`["npm:@christianmoesl/pi-radar"]`, true}, {`["npm:@christianmoesl/pi-radar@0.1.0"]`, false},
		{`[{"source":"npm:@christianmoesl/pi-radar","extensions":[]}]`, false}, {`[{"source":"npm:@christianmoesl/pi-radar","autoload":false}]`, false},
		{`["git:github.com/ChristianMoesl/radar"]`, false}, {`["npm:unrelated"]`, false},
	} {
		t.Run(test.packages, func(t *testing.T) {
			_, cwd := releaseFixture(t, test.packages)
			s := InspectRelease(cwd)
			if s.CanUpdate != test.want {
				t.Fatalf("%+v", s)
			}
		})
	}
}
func TestProjectOverrideAndDevelopmentSymlinkAreNotUpdated(t *testing.T) {
	profile, cwd := releaseFixture(t, `["npm:@christianmoesl/pi-radar"]`)
	os.MkdirAll(filepath.Join(cwd, ".pi"), 0700)
	os.WriteFile(filepath.Join(cwd, ".pi/settings.json"), []byte(`{"packages":["npm:@christianmoesl/pi-radar"]}`), 0600)
	if s := InspectRelease(cwd); s.CanUpdate || !strings.Contains(s.Reason, "Project-local") {
		t.Fatal(s)
	}
	os.Remove(filepath.Join(cwd, ".pi/settings.json"))
	packagePath := filepath.Join(profile, "npm/node_modules/@christianmoesl/pi-radar")
	local := filepath.Join(t.TempDir(), "development")
	os.Rename(packagePath, local)
	os.Symlink(local, packagePath)
	if s := InspectRelease(cwd); s.CanUpdate || !strings.Contains(s.Reason, "development") {
		t.Fatal(s)
	}
}
func TestLoadedVersionIsSeparateAndStaleReportsAreIgnored(t *testing.T) {
	profile, cwd := releaseFixture(t, `["npm:@christianmoesl/pi-radar"]`)
	dir := filepath.Join(profile, "radar/loaded")
	os.MkdirAll(dir, 0700)
	r := LoadedRelease{PID: os.Getpid(), Version: "0.0.9", Profile: profile, CWD: cwd, Updated: time.Now().UnixMilli()}
	data, _ := json.Marshal(r)
	os.WriteFile(filepath.Join(dir, "live.json"), data, 0600)
	r.Updated -= 600000
	data, _ = json.Marshal(r)
	os.WriteFile(filepath.Join(dir, "stale.json"), data, 0600)
	s := InspectRelease(cwd)
	if s.Installed != "0.1.0" || len(s.Loaded) != 1 || s.Loaded[0].Version != "0.0.9" {
		t.Fatal(s)
	}
}
func TestOnlyMatchingRadarManagedPinIsEligible(t *testing.T) {
	profile, cwd := releaseFixture(t, `["npm:@christianmoesl/pi-radar@0.1.0"]`)
	dir := filepath.Join(profile, "radar")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "release-pin.json"), []byte(`{"source":"npm:@christianmoesl/pi-radar@0.1.0"}`), 0600)
	if s := InspectRelease(cwd); !s.CanUpdate {
		t.Fatal(s)
	}
	os.WriteFile(filepath.Join(profile, "settings.json"), []byte(`{"packages":["npm:@christianmoesl/pi-radar@0.2.0"]}`), 0600)
	if s := InspectRelease(cwd); s.CanUpdate {
		t.Fatal("silently adopted a user-changed pin")
	}
}
func TestPiUpdateTargetsOnlyExactHostPackageAndReportsPartialFailure(t *testing.T) {
	profile, cwd := releaseFixture(t, `["npm:@christianmoesl/pi-radar","npm:other"]`)
	bin := t.TempDir()
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	script := `#!/bin/sh
[ "$1" = install ] && [ "$2" = npm:@christianmoesl/pi-radar@0.1.1 ] || exit 9
printf '%s\n' "$@" > "$PI_CODING_AGENT_DIR/command"
cat > "$PI_CODING_AGENT_DIR/settings.json" <<'JSON'
{"packages":["npm:@christianmoesl/pi-radar@0.1.1","npm:other"]}
JSON
cat > "$PI_CODING_AGENT_DIR/npm/node_modules/@christianmoesl/pi-radar/package.json" <<'JSON'
{"name":"@christianmoesl/pi-radar","version":"0.1.1"}
JSON
`
	os.WriteFile(filepath.Join(bin, "pi"), []byte(script), 0700)
	before := InspectRelease(cwd)
	if err := InstallRelease(context.Background(), before, cwd, "0.1.1"); err != nil {
		t.Fatal(err)
	}
	state := InspectRelease(cwd)
	if !state.CanUpdate || state.Installed != "0.1.1" || state.Source != "npm:@christianmoesl/pi-radar@0.1.1" {
		t.Fatal(state)
	}
	calls, _ := os.ReadFile(filepath.Join(profile, "command"))
	if string(calls) != "install\nnpm:@christianmoesl/pi-radar@0.1.1\n" {
		t.Fatal(string(calls))
	}
	os.WriteFile(filepath.Join(bin, "pi"), []byte("#!/bin/sh\nexit 1\n"), 0700)
	err := InstallRelease(context.Background(), state, cwd, "0.1.2")
	if err == nil || !strings.Contains(err.Error(), "Radar updated, but Pi install failed") {
		t.Fatal(err)
	}
}
