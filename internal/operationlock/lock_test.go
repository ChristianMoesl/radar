package operationlock

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestMutationsAndUpgradeExcludeEachOther(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS gate")
	}
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	mutation, err := Acquire(false)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := Acquire(true); err == nil {
		release()
		t.Fatal("interrupted accepted work")
	}
	mutation()
	upgrade, err := Acquire(true)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := Acquire(false); err == nil {
		release()
		t.Fatal("accepted mutation during activation")
	}
	upgrade()
	release, err := Acquire(false)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
