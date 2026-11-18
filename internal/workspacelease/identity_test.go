package workspacelease

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func aliasPaths(t *testing.T, missing bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	real := filepath.Join(root, "physical")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	if missing {
		real, alias = filepath.Join(real, "missing", "member"), filepath.Join(alias, "missing", "member")
	}
	return real, alias
}

func TestLeaseAliasesContendInBothDirections(t *testing.T) {
	for _, missing := range []bool{false, true} {
		for _, aliasFirst := range []bool{false, true} {
			for _, exclusive := range []bool{false, true} {
				name := map[bool]string{false: "existing", true: "missing"}[missing] + "/" + map[bool]string{false: "physical-first", true: "alias-first"}[aliasFirst] + "/" + map[bool]string{false: "shared", true: "exclusive"}[exclusive]
				t.Run(name, func(t *testing.T) {
					t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
					first, second := aliasPaths(t, missing)
					if aliasFirst {
						first, second = second, first
					}
					release, err := Acquire(first, exclusive)
					if err != nil {
						t.Fatal(err)
					}
					defer release()
					if other, err := Acquire(second, true); err == nil {
						other()
						t.Fatal("alias bypassed active resource lease")
					}
					if !exclusive {
						other, err := Acquire(second, false)
						if err != nil {
							t.Fatal("alias prevented shared/shared admission", err)
						}
						other()
					}
				})
			}
		}
	}
}

func TestLeaseProbeUsesSameAliasIdentity(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	real, alias := aliasPaths(t, true)
	if value, _ := ProbeExclusive(context.Background(), alias); value != "unknown_no_lock_file" {
		t.Fatalf("initial probe = %s", value)
	}
	release, err := Acquire(real, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	value, fromAlias := ProbeExclusive(context.Background(), alias)
	_, fromPhysical := ProbeExclusive(context.Background(), real)
	if value != "contended_at_probe" || fromAlias != fromPhysical {
		t.Fatal("alias probe did not observe the native holder")
	}
}

func TestLeaseIdentitySurvivesMissingDirectoryCreation(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	real, alias := aliasPaths(t, true)
	release, err := Acquire(alias, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := os.MkdirAll(real, 0700); err != nil {
		t.Fatal(err)
	}
	if other, err := Acquire(real, true); err == nil {
		other()
		t.Fatal("creating the missing path changed its lease identity")
	}
}

func TestLeaseRejectsUnresolvableAliasesBeforeCreatingLockState(t *testing.T) {
	for _, kind := range []string{"dangling", "loop", "non-directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("RADAR_SOCKET", filepath.Join(root, "runtime", "radar.sock"))
			path := filepath.Join(root, "bad")
			var err error
			switch kind {
			case "dangling":
				err = os.Symlink(filepath.Join(root, "absent"), path)
			case "loop":
				err = os.Symlink(path, path)
			case "non-directory":
				err = os.WriteFile(path, nil, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, candidate := range []string{path + "/child"} {
				if release, err := Acquire(candidate, false); err == nil {
					release()
					t.Fatal("unresolvable alias was admitted")
				}
				if evidence, _ := ProbeExclusive(context.Background(), candidate); evidence != "unavailable" {
					t.Fatalf("unresolved alias probe = %s", evidence)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "runtime")); !os.IsNotExist(err) {
				t.Fatal("failed identity resolution created runtime lock state", err)
			}
		})
	}
}

func TestCanonicalPathPreservesFilesystemTraversalAndRejectsMissingTraversal(t *testing.T) {
	real, _ := aliasPaths(t, false)
	deep := filepath.Join(real, "deep")
	if err := os.Mkdir(deep, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(real), "to-deep")
	if err := os.Symlink(deep, link); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link + "/..", link + "/../missing/member"} {
		got, err := CanonicalPath(path)
		expected := want
		if path != link+"/.." {
			expected = filepath.Join(want, "missing/member")
		}
		if err != nil || got != expected {
			t.Fatalf("identity for %s = %s, %v; want %s", path, got, err, expected)
		}
	}
	t.Chdir(filepath.Dir(real))
	if got, err := CanonicalPath("to-deep/.."); err != nil || got != want {
		t.Fatalf("relative symlink traversal = %s, %v", got, err)
	}
	if _, err := CanonicalPath(real + "/absent/../member"); err == nil {
		t.Fatal("missing traversal was silently cleaned")
	}
	if _, err := CanonicalPath(""); err == nil {
		t.Fatal("empty workspace path was admitted")
	}
}

func TestLeaseReleaseDoesNotDependOnAliasStillExisting(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	real, alias := aliasPaths(t, false)
	release, err := Acquire(alias, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if other, err := Acquire(real, true); err == nil {
		other()
		t.Fatal("unlinking alias dropped its physical lease")
	}
	release()
	other, err := Acquire(real, true)
	if err != nil {
		t.Fatal("release retained original physical lease", err)
	}
	other()
}
