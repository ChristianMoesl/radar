package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFeedbackAvailabilityRequiresFreshLiveRuntimeInCurrentProfile(t *testing.T) {
	profile := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", profile)
	if FeedbackAvailable() {
		t.Fatal("missing report enabled feedback")
	}
	for _, tc := range []struct {
		name string
		edit func(*LoadedRelease)
		want bool
	}{
		{"connected", func(*LoadedRelease) {}, true},
		{"unavailable", func(r *LoadedRelease) { r.LinearAvailable = false }, false},
		{"stale", func(r *LoadedRelease) { r.Updated -= 16000 }, false},
		{"future", func(r *LoadedRelease) { r.Updated += 120000 }, false},
		{"other-profile", func(r *LoadedRelease) { r.Profile += "-other" }, false},
		{"dead-process", func(r *LoadedRelease) { r.PID = 2147483647 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := LoadedRelease{PID: os.Getpid(), Version: "0.7.0", Profile: profile, Updated: time.Now().UnixMilli(), LinearAvailable: true}
			tc.edit(&report)
			data, _ := json.Marshal(report)
			writeFixture(t, filepath.Join(profile, "radar/loaded/live.json"), string(data))
			if got := FeedbackAvailable(); got != tc.want {
				t.Fatalf("available=%v, want %v", got, tc.want)
			}
		})
	}
}
