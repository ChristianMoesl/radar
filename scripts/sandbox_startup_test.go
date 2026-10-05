package scripts_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSandboxStartupRunner(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for image startup-runner tests")
	}
	command := exec.Command(python, "-B", "../sandbox/test_startup.py")
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sandbox startup tests: %v\n%s", err, output)
	}
}

func TestSandboxStartupImageContract(t *testing.T) {
	for _, test := range []struct {
		path string
		want string
	}{
		{"../Dockerfile", "COPY --chmod=0755 sandbox/startup.py /usr/local/bin/sandbox-startup"},
		{"../.dockerignore", "!sandbox/startup.py"},
		{"../sandbox/kit/spec.yaml", "command: [sh, -c, \"sandbox-startup run || true\"]"},
		{"../sandbox/kit/spec.yaml", "user: \"1000\""},
	} {
		contents, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), test.want) {
			t.Fatalf("%s does not wire the startup runner: missing %q", test.path, test.want)
		}
	}
}
