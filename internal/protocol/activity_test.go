package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestActivityReduction(t *testing.T) {
	states := []Activity{ActivityIdle, ActivityBusy, ActivityWaiting}
	for i, left := range states {
		for j, right := range states {
			want := states[max(i, j)]
			if got := MergeActivity(left, right); got != want {
				t.Fatalf("merge(%q, %q) = %q, want %q", left, right, got, want)
			}
		}
	}
}

func TestActivityParsing(t *testing.T) {
	for _, want := range []Activity{ActivityIdle, ActivityBusy, ActivityWaiting} {
		got, err := ParseActivity(want.String())
		if err != nil || got != want || !got.Valid() {
			t.Fatalf("parse %q: %q, %v", want, got, err)
		}
	}
	for _, value := range []string{"", "true", "1", "WAITING", "unknown"} {
		if _, err := ParseActivity(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if Activity("unknown").Valid() {
		t.Fatal("unknown activity is valid")
	}
}

func TestActivityJSONRoundTrip(t *testing.T) {
	for _, activity := range []Activity{ActivityIdle, ActivityBusy, ActivityWaiting} {
		task := Task{Activity: activity, SourceRefs: []SourceRef{{Activity: activity}}}
		data, err := json.Marshal(task)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Task
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Activity != activity || decoded.SourceRefs[0].Activity != activity {
			t.Fatalf("roundtrip: %s", data)
		}
		if activity == ActivityIdle && strings.Contains(string(data), `"activity"`) {
			t.Fatalf("idle should be omitted: %s", data)
		}
		if strings.Contains(string(data), `"busy":`) {
			t.Fatalf("legacy boolean emitted: %s", data)
		}
	}
}
