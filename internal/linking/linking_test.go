package linking

import (
	"reflect"
	"testing"
)

func TestMarkMatcherOnlyMatchesConfiguredPrefixes(t *testing.T) {
	matcher := NewMarkMatcher([]string{" abc ", "XYZ"})
	got := matcher.Values(
		"ABC-722 XC Service to inspect Translations Origin-096e274f",
		"preview-deploy-96724c48 workflow-8f23 XYZ-9",
	)
	want := []string{"ABC-722", "XYZ-9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Values() = %#v, want %#v", got, want)
	}
}

func TestMarkMatcherRequiresWholeIdentifier(t *testing.T) {
	matcher := NewMarkMatcher([]string{"XYZ"})
	got := matcher.Values("XXYZ-1 XYZ-2x XYZ-3/XYZ-4")
	want := []string{"XYZ-3", "XYZ-4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Values() = %#v, want %#v", got, want)
	}
}

func TestMarkKeyRoundTrip(t *testing.T) {
	key := MarkKey(" xyz-7 ")
	if key != "mark:XYZ-7" {
		t.Fatalf("MarkKey() = %q", key)
	}
	if value, ok := MarkValue(key); !ok || value != "XYZ-7" {
		t.Fatalf("MarkValue() = %q, %v", value, ok)
	}
}
