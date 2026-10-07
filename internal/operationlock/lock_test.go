package operationlock

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestMutationsAndUpdateExcludeEachOther(t *testing.T) {
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
	update, err := Acquire(true)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := Acquire(false); err == nil {
		release()
		t.Fatal("accepted mutation during activation")
	}
	update()
	release, err := Acquire(false)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
