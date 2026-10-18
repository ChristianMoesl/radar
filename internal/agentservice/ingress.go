package agentservice

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"radar/internal/protocol"
)

func NewIngress(token func(string) (string, error), deliver func(context.Context, string, json.RawMessage) (json.RawMessage, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method != "POST" || len(parts) != 3 || parts[0] != "agents" || parts[2] != "events" {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		expected, err := token(parts[1])
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if err != nil || expected == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, protocol.AgentMaxFrameBytes))
		if err != nil {
			http.Error(w, `{"error":"event too large"}`, http.StatusRequestEntityTooLarge)
			return
		}
		if !json.Valid(body) {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}
		result, err := deliver(r.Context(), parts[1], body)
		if err != nil {
			var rpcErr *protocol.AgentRPCError
			if errors.As(err, &rpcErr) {
				if rpcErr.Code == -32009 || rpcErr.Code == -32010 {
					http.Error(w, `{"error":"event identity conflict or stale interaction"}`, http.StatusConflict)
					return
				}
				if rpcErr.Code == -32602 {
					http.Error(w, `{"error":"invalid event envelope"}`, http.StatusBadRequest)
					return
				}
			}
			http.Error(w, `{"error":"admission failed; retry with the same event identity"}`, http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write(result)
	})
}
