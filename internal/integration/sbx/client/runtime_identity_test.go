package client

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestFindRuntimeIdentityRequiresNameAndLifetimeAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, listing    string
		found, wantError bool
	}{
		{"absent", "[]", false, false},
		{"present", `[{"name":"fixture","id":"one"}]`, true, false},
		{"replacement", `[{"name":"fixture","id":"two"}]`, true, false},
		{"unrelated", `[{"name":"other","id":"two"}]`, false, false},
		{"renamed", `[{"name":"private-renamed","id":"one"}]`, false, true},
		{"renamed-with-replacement", `[{"name":"fixture","id":"two"},{"name":"private-renamed","id":"one"}]`, false, true},
		{"same-id-alias", `[{"name":"fixture","id":"one"},{"name":"private-alias","id":"one"}]`, false, true},
		{"duplicate-foreign-id", `[{"name":"other","id":"two"},{"name":"another","id":"two"}]`, false, true},
		{"duplicate-foreign-name", `[{"name":"other","id":"two"},{"name":"other","id":"three"}]`, false, true},
		{"duplicate-target", `[{"name":"fixture","id":"one"},{"name":"fixture","id":"one"}]`, false, true},
		{"missing-name", `[{"id":"one"}]`, false, true},
		{"missing-id", `[{"name":"fixture"}]`, false, true},
		{"unidentified-foreign-row", `[{"name":"other"}]`, false, true},
		{"null", "null", false, true},
		{"null-row", "[null]", false, true},
		{"object", "{}", false, true},
		{"malformed", "invalid", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{installed: map[string]bool{"sbx": true}, output: tc.listing}
			runtime, err := New(runner).FindRuntimeIdentity(context.Background(), "fixture", "one")
			if (err != nil) != tc.wantError || (runtime != nil) != tc.found {
				t.Fatalf("runtime=%+v err=%v", runtime, err)
			}
			if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, []string{"ls", "--json"}) {
				t.Fatal("broadened or repeated native observation", runner.calls)
			}
			if err != nil && strings.Contains(err.Error(), "private-") {
				t.Fatal("private runtime identity leaked", err)
			}
		})
	}
	for _, tc := range []struct{ name, id string }{{"", "one"}, {"fixture", ""}, {"fixture", " "}} {
		t.Run("missing-selector/"+tc.name+"/"+tc.id, func(t *testing.T) {
			runner := &fakeRunner{installed: map[string]bool{"sbx": true}, output: "[]"}
			if _, err := New(runner).FindRuntimeIdentity(context.Background(), tc.name, tc.id); err == nil || len(runner.calls) != 0 {
				t.Fatal("unscoped native observation", err, runner.calls)
			}
		})
	}
	t.Run("native-error", func(t *testing.T) {
		failure := errors.New("fixture read failure")
		runner := &fakeRunner{installed: map[string]bool{"sbx": true}, failure: failure}
		if _, err := New(runner).FindRuntimeIdentity(context.Background(), "fixture", "one"); !errors.Is(err, failure) {
			t.Fatal("native error identity lost", err)
		}
	})
}
