package linking

import (
	"reflect"
	"testing"
)

func TestMarkMatcherOnlyMatchesConfiguredPrefixes(t *testing.T) {
	matcher := NewMarkMatcher([]string{" abc ", "RAD"})
	got := matcher.Values(
		"ABC-722 XC Service to inspect Translations Origin-096e274f",
		"preview-deploy-96724c48 workflow-8f23 RAD-9",
	)
	want := []string{"ABC-722", "RAD-9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Values() = %#v, want %#v", got, want)
	}
}

func TestMarkMatcherRequiresWholeIdentifier(t *testing.T) {
	matcher := NewMarkMatcher([]string{"RAD"})
	got := matcher.Values("XRAD-1 RAD-2x RAD-3/RAD-4")
	want := []string{"RAD-3", "RAD-4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Values() = %#v, want %#v", got, want)
	}
}

func TestMarkKeyRoundTrip(t *testing.T) {
	key := MarkKey(" rad-7 ")
	if key != "mark:RAD-7" {
		t.Fatalf("MarkKey() = %q", key)
	}
	if value, ok := MarkValue(key); !ok || value != "RAD-7" {
		t.Fatalf("MarkValue() = %q, %v", value, ok)
	}
}
