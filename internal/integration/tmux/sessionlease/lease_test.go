package sessionlease

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/workspacelease"
)

type fixtureRunner struct {
	identities map[string]Identity
	resolve    func(string) (Identity, error)
	raw        string
	calls      int
}

func (r *fixtureRunner) Run(ctx context.Context, _ string, _ string, args ...string) (string, error) {
	r.calls++
	if err := ctx.Err(); err != nil {
		return "", err
	}
	selector := strings.TrimSuffix(args[3], ":")
	if args[4] == "#{session_name}" {
		for _, id := range r.identities {
			if id.ID == selector {
				return id.Name + "\n", nil
			}
		}
		return "", nil
	}
	if r.raw != "" {
		return r.raw, nil
	}
	var id Identity
	var err error
	if r.resolve != nil {
		id, err = r.resolve(selector)
	} else {
		id = r.identities[selector]
	}
	if err != nil {
		return "", err
	}
	if id.ID == "" {
		return "12::\n", nil
	}
	return id.ServerPID + ":" + id.Created + ":" + id.ID + "\n", nil
}

func runtimeFixture(t *testing.T) (*fixtureRunner, Identity) {
	t.Helper()
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	id := Identity{ServerPID: "12", Created: "34", ID: "$7", Name: "workspace-alpha"}
	r := &fixtureRunner{identities: map[string]Identity{}}
	for _, selector := range []string{"workspace-alpha", "=workspace-alpha", "workspace-a", "workspace-*", "$7"} {
		r.identities[selector] = id
	}
	return r, id
}

func TestAbsentAdmissionRejectsAmbiguousSelectorsBeforeNativeCalls(t *testing.T) {
	for _, selector := range []string{"workspace-a", "workspace-*", "workspace-alpha", "=other", "$bad"} {
		t.Run(selector, func(t *testing.T) {
			r, _ := runtimeFixture(t)
			set := New(false)
			defer set.Close()
			_, err := set.Admit(context.Background(), r, Request{Selector: selector, Name: "workspace-alpha", AllowMissing: true})
			if err == nil || r.calls != 0 {
				t.Fatal("ambiguous absent selector reached native resolution", err, r.calls)
			}
		})
	}
}

func TestAliasesAndAdmissionModes(t *testing.T) {
	for _, exclusive := range []bool{false, true} {
		for _, alias := range []string{"workspace-alpha", "=workspace-alpha", "workspace-a", "workspace-*", "$7"} {
			t.Run(alias+map[bool]string{false: "/shared", true: "/exclusive"}[exclusive], func(t *testing.T) {
				r, id := runtimeFixture(t)
				first := New(exclusive)
				defer first.Close()
				actual, err := first.Admit(context.Background(), r, Request{Selector: alias})
				if err != nil || actual != id {
					t.Fatal(actual, err)
				}
				for _, otherExclusive := range []bool{false, true} {
					other := New(otherExclusive)
					_, err := other.Admit(context.Background(), r, Request{Selector: "$7"})
					other.Close()
					if (err != nil) != (exclusive || otherExclusive) {
						t.Fatal("alias/mode contention mismatch", err)
					}
				}
				first.Close()
				first.Close()
				next := New(true)
				defer next.Close()
				if _, err := next.Admit(context.Background(), r, Request{Selector: "$7"}); err != nil {
					t.Fatal("lease leaked", err)
				}
			})
		}
	}
}

func TestRuntimeIncarnationAndRename(t *testing.T) {
	r, id := runtimeFixture(t)
	first := New(false)
	defer first.Close()
	if _, err := first.Admit(context.Background(), r, Request{Selector: "$7"}); err != nil {
		t.Fatal(err)
	}
	renamed := id
	renamed.Name = "renamed-runtime"
	r.identities = map[string]Identity{"$7": renamed, "=renamed-runtime": renamed}
	other := New(true)
	if _, err := other.Admit(context.Background(), r, Request{Selector: "=renamed-runtime"}); err == nil {
		other.Close()
		t.Fatal("rename bypassed incarnation lease")
	}
	// Different server incarnation must not collide solely because ID is $7.
	restarted := renamed
	restarted.ServerPID = "56"
	r.identities = map[string]Identity{"$7": restarted, "=renamed-runtime": restarted}
	next := New(true)
	defer next.Close()
	if _, err := next.Admit(context.Background(), r, Request{Selector: "$7"}); err != nil {
		t.Fatal("different server with unrelated name was blocked", err)
	}
}

func TestMissingNameReservationAndChangedIncarnation(t *testing.T) {
	for _, existingName := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "replacement"}[existingName], func(t *testing.T) {
			r, id := runtimeFixture(t)
			delete(r.identities, "$7")
			if !existingName {
				r.identities = map[string]Identity{}
			}
			set := New(true)
			defer set.Close()
			got, err := set.Admit(context.Background(), r, Request{Selector: "$7", Name: id.Name, Expected: id.Key(), AllowMissing: true})
			if existingName {
				if err == nil {
					t.Fatal("missing ID selected a replacement by name")
				}
				return
			}
			if err != nil || got.ID != "" || got.Name != id.Name {
				t.Fatal(got, err)
			}
			if release, err := workspacelease.AcquireTmux("name:"+id.Name, false); err == nil {
				release()
				t.Fatal("absent name was not reserved")
			}
		})
	}
}

func TestDedupAndFullSetRollback(t *testing.T) {
	r, id := runtimeFixture(t)
	set := New(true)
	defer set.Close()
	for _, selector := range []string{"$7", "=workspace-alpha", "workspace-*"} {
		if _, err := set.Admit(context.Background(), r, Request{Selector: selector}); err != nil {
			t.Fatal("same set conflicted with itself", err)
		}
	}
	if _, err := set.Admit(context.Background(), r, Request{Selector: "$7", Expected: "tmux:session:99:34:$7"}); err == nil {
		t.Fatal("stale expected incarnation accepted")
	}
	for _, key := range []string{"name:" + id.Name, "runtime:" + id.Key()} {
		release, err := workspacelease.AcquireTmux(key, true)
		if err != nil {
			t.Fatal("failed bundle leaked earlier admission", err)
		}
		release()
	}
}

func TestMalformedAndCancelledRequestsCreateNoState(t *testing.T) {
	for _, output := range []string{"garbage", "12:34:", "12:34:$7:extra", "0:34:$7", "12:0:$7", "12:34:7"} {
		t.Run(output, func(t *testing.T) {
			r, _ := runtimeFixture(t)
			r.raw = output
			set := New(false)
			defer set.Close()
			if _, err := set.Admit(context.Background(), r, Request{Selector: "$7"}); err == nil {
				t.Fatal("malformed native identity accepted")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(os.Getenv("RADAR_SOCKET")), "leases")); !os.IsNotExist(err) {
				t.Fatal("invalid identity created native state", err)
			}
		})
	}
	r, _ := runtimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	set := New(false)
	defer set.Close()
	if _, err := set.Admit(ctx, r, Request{Selector: "$7"}); !errors.Is(err, context.Canceled) || r.calls != 0 {
		t.Fatal("cancelled request reached runtime", err, r.calls)
	}
}

func TestResolutionDriftReleasesAdmission(t *testing.T) {
	r, id := runtimeFixture(t)
	lookups := 0
	r.resolve = func(string) (Identity, error) {
		lookups++
		if lookups == 1 {
			return id, nil
		}
		return Identity{}, nil
	}
	set := New(false)
	defer set.Close()
	if _, err := set.Admit(context.Background(), r, Request{Selector: "workspace-a"}); err == nil {
		t.Fatal("native target disappeared without failing admission")
	}
	release, err := workspacelease.AcquireTmux("runtime:"+id.Key(), true)
	if err != nil {
		t.Fatal("runtime lease leaked on resolution drift", err)
	}
	release()
}
