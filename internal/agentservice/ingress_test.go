package agentservice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIngressAuthenticationAndAcknowledgement(t *testing.T) {
	calls := 0
	fail := true
	handler := NewIngress(func(id string) (string, error) {
		if id != "counter" {
			return "", fmt.Errorf("unknown agent")
		}
		return "fixture-token", nil
	}, func(_ context.Context, id string, event json.RawMessage) (json.RawMessage, error) {
		calls++
		if fail {
			return nil, fmt.Errorf("response lost")
		}
		return json.RawMessage(`{"taskId":1}`), nil
	})
	request := func(path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/agents/counter/events", "/agents/other/events"} {
		if got := request(path, "wrong", `{}`).Code; got != http.StatusUnauthorized {
			t.Fatal(got)
		}
	}
	if calls != 0 {
		t.Fatal("unauthenticated delivery reached runner")
	}
	if got := request("/agents/counter/events", "fixture-token", `{}`).Code; got != http.StatusServiceUnavailable {
		t.Fatal(got)
	}
	fail = false
	if got := request("/agents/counter/events", "fixture-token", `{}`).Code; got != http.StatusAccepted {
		t.Fatal(got)
	}
	if got := request("/agents/counter/events", "fixture-token", `not-json`).Code; got != http.StatusBadRequest {
		t.Fatal(got)
	}
	if got := request("/agents/counter/events", "fixture-token", strings.Repeat("a", 1024*1024+1)).Code; got != http.StatusRequestEntityTooLarge {
		t.Fatal(got)
	}
}
