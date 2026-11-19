package workspacelease

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMutationLeaseMatchesCanonicalMissingAliasesWithoutBlockingAgents(t *testing.T) {
	for _, missing := range []bool{false, true} {
		for _, aliasFirst := range []bool{false, true} {
			t.Run(map[bool]string{false: "existing", true: "missing"}[missing]+"/"+map[bool]string{false: "physical-first", true: "alias-first"}[aliasFirst], func(t *testing.T) {
				t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
				first, second := aliasPaths(t, missing)
				if aliasFirst {
					first, second = second, first
				}
				agent, err := Acquire(first, false)
				if err != nil {
					t.Fatal(err)
				}
				defer agent()
				writer, err := AcquireMutation(second)
				if err != nil {
					t.Fatal("agent prevented mutation admission", err)
				}
				if other, err := AcquireMutation(first); err == nil {
					other()
					t.Error("alias bypassed mutation admission")
				} else if !strings.Contains(err.Error(), "workspace mutation is in use") {
					t.Error("incorrect conflict", err)
				}
				if missing {
					if err := os.MkdirAll(first, 0700); err != nil {
						t.Fatal(err)
					}
					if other, err := AcquireMutation(first); err == nil {
						other()
						t.Error("directory creation changed mutation identity")
					}
				}
				writer()
				writer, err = AcquireMutation(first)
				if err != nil {
					t.Fatal("mutation admission was not released", err)
				}
				writer()
			})
		}
	}
}

func TestMutationNamespacesRemainDistinctAndSandboxNamesNormalize(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	path := t.TempDir()
	writer, err := AcquireMutation(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer()
	for _, acquire := range []func() (func(), error){
		func() (func(), error) { return Acquire(path, true) },
		func() (func(), error) { return AcquireSandboxMutation(path) },
		func() (func(), error) { return AcquireTmux(path, true) },
	} {
		release, err := acquire()
		if err != nil {
			t.Fatal("distinct lease namespaces collided", err)
		}
		release()
	}
	sandbox, err := AcquireSandboxMutation(" shared-runtime ")
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox()
	if release, err := AcquireSandboxMutation("shared-runtime"); err == nil {
		release()
		t.Fatal("sandbox whitespace bypassed mutation admission")
	}
	for _, invalid := range []string{"", " \t ", "bad\nname"} {
		if release, err := AcquireSandboxMutation(invalid); err == nil {
			release()
			t.Error("invalid sandbox mutation identity accepted")
		}
	}
}
