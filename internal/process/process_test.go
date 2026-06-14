package process

import "testing"

func TestIsRadarDaemonRecognizesInstalledAndDevelopmentBinaries(t *testing.T) {
	for _, args := range [][]string{
		{"/usr/local/bin/radar", "daemon"},
		{"/tmp/radar-test", "daemon"},
	} {
		if !isRadarDaemon(args) {
			t.Fatalf("isRadarDaemon(%q) = false, want true", args)
		}
	}
	if isRadarDaemon([]string{"/tmp/not-radar", "daemon"}) {
		t.Fatal("isRadarDaemon() recognized unrelated binary")
	}
}
