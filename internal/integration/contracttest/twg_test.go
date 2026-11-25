package contracttest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestTWGFixtureProcess(t *testing.T) {
	if os.Getenv("RADAR_TWG_FIXTURE_ENV_PROBE") == "1" {
		fmt.Print(os.Getenv("GORACE"))
		os.Exit(0)
	}
	RunTWGFixtureProcess(t)
}

func TestTWGFixtureChildRaceOptionsAreScoped(t *testing.T) {
	for _, inherited := range []string{"", "exitcode=67 halt_on_error=1", "atexit_sleep_ms=1000 exitcode=67"} {
		t.Run(inherited, func(t *testing.T) {
			t.Setenv("GORACE", inherited)
			t.Setenv("RADAR_TWG_FIXTURE_ENV_PROBE", "1")
			InstallTWGFixture(t, "http://127.0.0.1/unused")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, "twg", "--version").Output()
			if err != nil {
				t.Fatal("fixture child:", err)
			}
			want := strings.TrimSpace(inherited + " atexit_sleep_ms=0")
			if string(output) != want {
				t.Fatalf("child race options = %q, want %q", output, want)
			}
			if got := os.Getenv("GORACE"); got != inherited {
				t.Fatalf("launcher changed parent race options: %q, want %q", got, inherited)
			}
		})
	}
}
