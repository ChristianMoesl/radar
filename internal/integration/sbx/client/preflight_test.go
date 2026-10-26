package client

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestVersionContract(t *testing.T) {
	for _, version := range []string{"v0.48.0", "v0.48.1", "v0.49.0", "v0.50.0", "v1.0.0", "v2.0.0"} {
		if err := ValidateVersion(`{"client":{"version":"` + version + `"}}`); err != nil {
			t.Fatal(err)
		}
	}
	for _, output := range []string{"", `null`, `{}`, `{"client":{"version":"v0.46.0"}}`, `{"client":{"version":"v0.47.99"}}`, `{"client":{"version":"v0.48.0-rc.1"}}`, `{"client":{"version":"v1.0.-1"}}`, `{"client":{"version":"v-1.50.0"}}`} {
		if ValidateVersion(output) == nil {
			t.Fatalf("accepted %s", output)
		}
	}
	for _, output := range []string{"", `null`, `{}`, `{"sandboxes":[]}`, `[null]`, `[2]`} {
		if _, err := DecodeList[map[string]any](output); err == nil {
			t.Fatalf("accepted %q", output)
		}
	}
	if rows, err := DecodeList[map[string]any]("[]"); err != nil || rows == nil {
		t.Fatalf("empty inventory: %v, %v", rows, err)
	}
}

func TestDaemonCachePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, status, pid, environ string
		fail                       bool
	}{
		{"not started", `{"status":"stopped"}`, "", "", false},
		{"safe daemon", `{"status":"running","socket":"/state/sandboxd.sock"}`, "123\n", CacheEnvironment + "=0\x00", false},
		{"default daemon", `{"status":"running","socket":"/state/sandboxd.sock"}`, "123\n", "OTHER=value\x00", true},
		{"enabled daemon", `{"status":"running","socket":"/state/sandboxd.sock"}`, "123\n", CacheEnvironment + "=1\x00", true},
		{"invalid pid", `{"status":"running","socket":"/state/sandboxd.sock"}`, "-1", "", true},
		{"unknown state", `{"status":"unknown"}`, "", "", true},
		{"missing socket", `{"status":"running"}`, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func(path string) ([]byte, error) {
				switch path {
				case "/state/sandboxd.pid":
					return []byte(tc.pid), nil
				case "/proc/123/environ":
					return []byte(tc.environ), nil
				default:
					t.Fatalf("unexpected read %q", path)
					return nil, os.ErrNotExist
				}
			}
			err := validateDaemonCache(tc.status, read)
			if (err != nil) != tc.fail {
				t.Fatalf("%v", err)
			}
			if err != nil && !strings.Contains(err.Error(), CacheEnvironment) {
				t.Fatalf("missing recovery guidance: %v", err)
			}
		})
	}
	if err := validateDaemonCache(`{"status":"running","socket":"/state/sandboxd.sock"}`, func(string) ([]byte, error) { return nil, errors.New("denied") }); err == nil {
		t.Fatal("unreadable daemon accepted")
	}
}

func TestPreflightAcceptsNewerVersions(t *testing.T) {
	for _, version := range []string{"v0.50.0", "v1.0.0"} {
		t.Run(version, func(t *testing.T) {
			r := &fakeRunner{installed: map[string]bool{"sbx": true}, output: `{"client":{"version":"` + version + `"}}`}
			if err := NewForPlatform(r, "darwin", false).Preflight(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPreflightDoesNotFallback(t *testing.T) {
	r := &fakeRunner{installed: map[string]bool{"sbx": true, "sbx.exe": true}, output: `{"client":{"version":"v0.46.0"}}`}
	c := NewForPlatform(r, "darwin", false)
	if err := c.Preflight(context.Background()); err == nil {
		t.Fatal("unsupported protocol accepted")
	}
	if len(r.calls) != 1 || r.calls[0].name != "sbx" || !reflect.DeepEqual(r.calls[0].args, []string{"version", "--json"}) {
		t.Fatalf("calls: %+v", r.calls)
	}
}

func TestEnvironmentAndShellPolicy(t *testing.T) {
	env := []string{"PATH=/bin", CacheEnvironment + "=1", "OTHER=value"}
	got := Environment("sbx", env)
	if IsWSL() && !reflect.DeepEqual(got, []string{"PATH=/bin", "OTHER=value", CacheEnvironment + "=0"}) {
		t.Fatal(got)
	}
	if env[1] != CacheEnvironment+"=1" {
		t.Fatal("mutated host environment")
	}
	if !reflect.DeepEqual(Environment("sbx.exe", env), env) {
		t.Fatal("changed Windows environment")
	}
	c := NewForPlatform(&fakeRunner{installed: map[string]bool{"sbx": true}}, "linux", true)
	shell, err := c.ShellExecutable()
	if err != nil || shell != "env "+CacheEnvironment+"=0 sbx" {
		t.Fatalf("%q, %v", shell, err)
	}
}
