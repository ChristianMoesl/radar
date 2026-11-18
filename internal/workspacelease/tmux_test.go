package workspacelease

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTmuxLeaseNamespacesAndAdmissionModes(t *testing.T) {
	for _, exclusive := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "exclusive"}[exclusive], func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("RADAR_SOCKET", filepath.Join(dir, "radar.sock"))
			release, err := AcquireTmux("name:workspace-alpha", exclusive)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			for _, otherExclusive := range []bool{false, true} {
				other, err := AcquireTmux("name:workspace-alpha", otherExclusive)
				if (err != nil) != (exclusive || otherExclusive) {
					t.Fatal("same native identity admission mismatch", err)
				}
				if err == nil {
					other()
				}
			}
			// Prefixes and resource kinds must not collapse onto another resource's
			// file, even if all caller-provided identity text is the same.
			for _, identity := range []string{"runtime:workspace-alpha", "name:workspace-beta"} {
				other, err := AcquireTmux(identity, true)
				if err != nil {
					t.Fatal(err)
				}
				other()
			}
			sandbox, err := AcquireSandbox("name:workspace-alpha", true)
			if err != nil {
				t.Fatal(err)
			}
			sandbox()
			path, err := Acquire(filepath.Join(dir, "name:workspace-alpha"), true)
			if err != nil {
				t.Fatal(err)
			}
			path()
		})
	}
}
func TestTmuxLeaseInvalidIdentityCreatesNoState(t *testing.T) {
	for _, identity := range []string{"", " ", "name:invalid\nidentity", "name:invalid\x00identity"} {
		t.Run(identity, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("RADAR_SOCKET", filepath.Join(dir, "radar.sock"))
			if release, err := AcquireTmux(identity, true); err == nil {
				release()
				t.Fatal("invalid identity admitted")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatal("invalid identity created native state", entries, err)
			}
		})
	}
}
