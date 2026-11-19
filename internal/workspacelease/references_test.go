package workspacelease

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReferenceAdmissionProtectsLexicalAndPhysicalAncestorsInBothOrders(t *testing.T) {
	for _, spelling := range []string{"physical", "alias", "alias-dotdot"} {
		for _, missing := range []bool{false, true} {
			for _, deletionFirst := range []bool{false, true} {
				t.Run(spelling+map[bool]string{false: "/existing", true: "/missing"}[missing]+map[bool]string{false: "/writer-first", true: "/cleanup-first"}[deletionFirst], func(t *testing.T) {
					base := t.TempDir()
					t.Setenv("RADAR_SOCKET", filepath.Join(base, "radar.sock"))
					lexical, physical := filepath.Join(base, "view"), filepath.Join(base, "physical")
					for _, dir := range []string{lexical, filepath.Join(physical, "child")} {
						if err := os.MkdirAll(dir, 0700); err != nil {
							t.Fatal(err)
						}
					}
					link := filepath.Join(lexical, "link")
					if err := os.Symlink(filepath.Join(physical, "child"), link); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(physical, "child", "reference")
					targets := []string{physical, filepath.Join(physical, "child")}
					if spelling != "physical" {
						path = link + "/reference"
						targets = append(targets, lexical, link)
					}
					if spelling == "alias-dotdot" {
						path = link + "/../reference"
						// The resolved file lives below physical, not lexical.
						targets = []string{physical, lexical, link}
					}
					if !missing {
						if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					for _, target := range append(targets, path) {
						if deletionFirst {
							release, err := Acquire(target, true)
							if err != nil {
								t.Fatal(err)
							}
							other, err := AcquireReferences(context.Background(), path, path)
							release()
							if err == nil {
								other()
								t.Fatal("reference bypassed deleting ancestor", target)
							}
						} else {
							release, err := AcquireReferences(context.Background(), path, path)
							if err != nil {
								t.Fatal(err)
							}
							other, err := Acquire(target, true)
							release()
							if err == nil {
								other()
								t.Fatal("deletion bypassed referenced descendant", target)
							}
						}
						release, err := Acquire(target, true)
						if err != nil {
							t.Fatal("reference admission leaked after release/conflict", err)
						}
						release()
					}
				})
			}
		}
	}
}

func TestReferenceAdmissionAllowsAgentsSharedReferencesAndUnrelatedDeletion(t *testing.T) {
	base := t.TempDir()
	t.Setenv("RADAR_SOCKET", filepath.Join(base, "radar.sock"))
	path := filepath.Join(base, "reference", "missing")
	release, err := AcquireReferences(context.Background(), "", path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, acquire := range []func() (func(), error){
		func() (func(), error) { return Acquire(base, false) },
		func() (func(), error) { return AcquireReferences(context.Background(), path) },
		func() (func(), error) { return AcquireReferences(context.Background(), filepath.Join(base, "sibling")) },
		func() (func(), error) { return Acquire(filepath.Join(base, "sibling"), true) },
		func() (func(), error) { return AcquireMutation(path) },
	} {
		other, err := acquire()
		if err != nil {
			t.Fatal("shared ancestor admission overblocked independent work", err)
		}
		other()
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if other, err := Acquire(path, true); err == nil {
		other()
		t.Fatal("creating missing reference changed deletion identity")
	}
}

type cancelReferenceAdmission struct {
	context.Context
	remaining int
}

func (c *cancelReferenceAdmission) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestReferenceAdmissionReleasesPartialSetsOnConflictCancellationAndInvalidPaths(t *testing.T) {
	base := t.TempDir()
	t.Setenv("RADAR_SOCKET", filepath.Join(base, "radar.sock"))
	first, last := filepath.Join(base, "aa", "reference"), filepath.Join(base, "zz", "reference")
	assertReleased := func() {
		t.Helper()
		for _, path := range []string{base, filepath.Dir(first), first, filepath.Dir(last), last} {
			release, err := Acquire(path, true)
			if err != nil {
				t.Fatal("partial reference admission leaked", path, err)
			}
			release()
		}
	}
	blocked, err := Acquire(last, true)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := AcquireReferences(context.Background(), first, last); err == nil {
		release()
		t.Fatal("later deletion conflict was ignored")
	} else if !strings.Contains(err.Error(), "in use") {
		t.Fatal(err)
	}
	blocked()
	assertReleased()
	// Exercise cancellation before resolution, between admissions, and after
	// the final lease. Err is queried synchronously by this admission helper.
	for cancelAt := 1; cancelAt <= 80; cancelAt++ {
		ctx := &cancelReferenceAdmission{Context: context.Background(), remaining: cancelAt}
		release, err := AcquireReferences(ctx, first, last)
		if err == nil {
			release()
		} else if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		assertReleased()
	}
	link := filepath.Join(base, "dangling")
	if err := os.Symlink(filepath.Join(base, "absent"), link); err != nil {
		t.Fatal(err)
	}
	if release, err := AcquireReferences(context.Background(), first, filepath.Join(link, "reference")); err == nil {
		release()
		t.Fatal("unresolvable physical reference fell back to lexical identity")
	}
	assertReleased()
}
