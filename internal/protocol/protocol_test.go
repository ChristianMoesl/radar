package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponseIncludesEmptyTasksAndSources(t *testing.T) {
	data, err := json.Marshal(Response{OK: true, Revision: 7, Summary: &Summary{}, Tasks: []Task{}, Sources: []SourceStatus{}})
	if err != nil {
		t.Fatal(err)
	}

	body := string(data)
	for _, want := range []string{`"revision":7`, `"tasks":[]`, `"sources":[]`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response should include %s for GUI clearing, got %s", want, body)
		}
	}
}
