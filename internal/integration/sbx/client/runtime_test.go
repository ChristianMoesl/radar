package client

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestFindRuntimeRequiresVerifiableAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, listing    string
		found, wantError bool
	}{
		{"absent", "[]", false, false},
		{"present", `[{"name":"fixture","id":"one"}]`, true, false},
		{"unrelated", `[{"name":"other","id":"two"}]`, false, false},
		{"duplicate", `[{"name":"fixture","id":"one"},{"name":"fixture","id":"two"}]`, false, true},
		{"aliased target ID", `[{"name":"fixture","id":"one"},{"name":"private-alias","id":"one"}]`, false, true},
		{"reversed target alias", `[{"name":"private-alias","id":"one"},{"name":"fixture","id":"one"}]`, false, true},
		{"absent with duplicate foreign ID", `[{"name":"private-other","id":"two"},{"name":"private-another","id":"two"}]`, false, true},
		{"absent with duplicate foreign name", `[{"name":"private-other","id":"two"},{"name":"private-other","id":"three"}]`, false, true},
		{"present with duplicate foreign ID", `[{"name":"fixture","id":"one"},{"name":"private-other","id":"two"},{"name":"private-another","id":"two"}]`, false, true},
		{"present with duplicate foreign name", `[{"name":"fixture","id":"one"},{"name":"private-other","id":"two"},{"name":"private-other","id":"three"}]`, false, true},
		{"missing name", `[{"id":"one"}]`, false, true},
		{"missing ID", `[{"name":"fixture"}]`, false, true},
		{"unidentified foreign row", `[{"name":"other"}]`, false, true},
		{"null", "null", false, true},
		{"null row", "[null]", false, true},
		{"object", "{}", false, true},
		{"malformed", "invalid", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{installed: map[string]bool{"sbx": true}, output: tc.listing}
			runtime, err := New(runner).FindRuntime(context.Background(), "fixture")
			if (err != nil) != tc.wantError || (runtime != nil) != tc.found {
				t.Fatalf("runtime=%+v err=%v", runtime, err)
			}
			if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, []string{"ls", "--json"}) {
				t.Fatal("broadened or repeated native observation", runner.calls)
			}
			if err != nil && strings.Contains(err.Error(), "private-") {
				t.Fatal("private listing identity leaked", err)
			}
		})
	}
	for _, name := range []string{"", "fixture"} {
		runner := &fakeRunner{installed: map[string]bool{"sbx": true}, failure: errors.New("unavailable")}
		if _, err := New(runner).FindRuntime(context.Background(), name); err == nil {
			t.Fatal("unverified absence")
		}
	}
}
