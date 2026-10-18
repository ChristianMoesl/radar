package settings

import (
	"errors"
	"testing"
)

func TestNativeWorkspaceDefaultsAndOverrides(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows", "freebsd"} {
		for _, installed := range []bool{false, true} {
			lookup := func(name string) error {
				if installed && name == "sbx" {
					return nil
				}
				return errors.New("absent")
			}
			want := (platform == "linux" || platform == "darwin") && installed
			if got := Default().WorkspaceEnabled(platform, lookup); got != want {
				t.Fatalf("%s installed=%t: %t", platform, installed, got)
			}
			for _, explicit := range []bool{false, true} {
				cfg := Default()
				cfg.Enabled = &explicit
				if cfg.WorkspaceEnabled(platform, lookup) != explicit {
					t.Fatal("override lost")
				}
			}
		}
	}
}
