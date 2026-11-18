package workspacelease

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandboxLeasesShareNameButNotWorkspacePathIdentity(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	for _, exclusive := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "exclusive"}[exclusive], func(t *testing.T) {
			release, err := AcquireSandbox("fixture-sandbox", exclusive)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if other, err := AcquireSandbox(" fixture-sandbox ", true); err == nil {
				other()
				t.Fatal("same sandbox admitted twice exclusively")
			}
			if other, err := AcquireSandbox("fixture-sandbox", false); (err != nil) != exclusive {
				if other != nil {
					other()
				}
				t.Fatal("incorrect shared admission", err)
			} else if other != nil {
				other()
			}
			for _, name := range []string{"different-sandbox", "Fixture-sandbox"} {
				other, err := AcquireSandbox(name, true)
				if err != nil {
					t.Fatal(err)
				}
				other()
			}
			pathRelease, err := Acquire(filepath.Join(t.TempDir(), "fixture-sandbox"), true)
			if err != nil {
				t.Fatal(err)
			}
			pathRelease()
		})
	}
	release, err := AcquireSandbox("fixture-sandbox", true)
	if err != nil {
		t.Fatal("name lease leaked", err)
	}
	release()
}

func TestSandboxLeaseInvalidIdentityCreatesNoNativeState(t *testing.T) {
	for _, name := range []string{"", "  ", "fixture\x00name", "fixture\nname", "fixture\tname"} {
		t.Run(strings.ReplaceAll(name, "\x00", "NUL"), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
			if release, err := AcquireSandbox(name, true); err == nil {
				release()
				t.Fatal("invalid sandbox identity admitted")
			}
			if _, err := os.Lstat(filepath.Join(root, "leases")); !os.IsNotExist(err) {
				t.Fatal("invalid identity created lease state", err)
			}
		})
	}
}
