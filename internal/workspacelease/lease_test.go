package workspacelease

import (
	"path/filepath"
	"testing"
)

func TestActiveWorkPreventsDeletionAndReleaseRestoresIt(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	workspace := t.TempDir()
	first, err := Acquire(workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(workspace, false)
	if err != nil {
		first()
		t.Fatal(err)
	}
	if release, err := Acquire(workspace, true); err == nil {
		release()
		t.Fatal("deletion acquired active workspace")
	}
	first()
	second()
	release, err := Acquire(workspace, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
