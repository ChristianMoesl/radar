package workspacelease

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitBranchMutationMatchesCanonicalCommonDirectory(t *testing.T) {
	for _, aliasFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "physical-first", true: "alias-first"}[aliasFirst], func(t *testing.T) {
			t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
			physical := t.TempDir()
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(physical, alias); err != nil {
				t.Fatal(err)
			}
			first, second := physical, alias
			if aliasFirst {
				first, second = second, first
			}
			release, err := AcquireGitBranchMutation(first, " feature ")
			if err != nil {
				t.Fatal(err)
			}
			if other, err := AcquireGitBranchMutation(second, "feature"); err == nil {
				other()
				t.Error("physical alias bypassed branch admission")
			} else if !strings.Contains(err.Error(), "git branch mutation is in use") {
				t.Error("wrong conflict", err)
			}
			release()
			release, err = AcquireGitBranchMutation(second, "feature")
			if err != nil {
				t.Fatal("branch admission leaked", err)
			}
			release()
		})
	}
}

func TestGitBranchMutationKeepsIndependentKeysAndNamespaces(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	common := t.TempDir()
	release, err := AcquireGitBranchMutation(common, "feature")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, acquire := range []func() (func(), error){
		func() (func(), error) { return AcquireGitBranchMutation(common, "Feature") },
		func() (func(), error) { return AcquireGitBranchMutation(common, "origin/feature") },
		func() (func(), error) { return AcquireGitBranchMutation(t.TempDir(), "feature") },
		func() (func(), error) { return Acquire(common, true) },
		func() (func(), error) { return AcquireMutation(common) },
		func() (func(), error) { return AcquireSandboxMutation("feature") },
	} {
		release, err := acquire()
		if err != nil {
			t.Fatal("independent resource conflicted", err)
		}
		release()
	}
}

func TestGitBranchMutationCrossProcess(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	common := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	release, err := AcquireGitBranchMutation(common, "feature")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release() }()
	for _, state := range []string{"held", "released"} {
		if state == "released" {
			release()
			release = func() {}
		}
		command := exec.Command(executable, "-test.run=^TestGitBranchMutationProcessHelper$")
		command.Env = append(os.Environ(), "RADAR_TEST_GIT_COMMON="+common, "RADAR_TEST_GIT_LEASE="+state)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("cross-process %s admission: %v\n%s", state, err, output)
		}
	}
}

func TestGitBranchMutationProcessHelper(t *testing.T) {
	common := os.Getenv("RADAR_TEST_GIT_COMMON")
	if common == "" {
		return
	}
	release, err := AcquireGitBranchMutation(common, "feature")
	if os.Getenv("RADAR_TEST_GIT_LEASE") == "held" {
		if err == nil {
			release()
			t.Fatal("child bypassed parent's branch admission")
		}
		if !strings.Contains(err.Error(), "git branch mutation is in use") {
			t.Fatal("wrong child conflict", err)
		}
	} else {
		if err != nil {
			t.Fatal("parent's branch admission leaked", err)
		}
		release()
	}
}

func TestGitBranchMutationRejectsInvalidIdentity(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	common := t.TempDir()
	for _, branch := range []string{"", " \t ", "bad\nbranch", "bad\x00branch"} {
		if release, err := AcquireGitBranchMutation(common, branch); err == nil {
			release()
			t.Fatal("invalid branch accepted")
		}
	}
	dangling := filepath.Join(t.TempDir(), "dangling")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, common := range []string{"", dangling, filepath.Join(t.TempDir(), "missing"), file} {
		if release, err := AcquireGitBranchMutation(common, "feature"); err == nil {
			release()
			t.Fatal("invalid common directory accepted")
		}
	}
}
