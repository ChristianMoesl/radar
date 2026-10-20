package contracttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Domain tests keep their dynamic Go fixture handlers. This executable adapts
// only those local fixture responses to the native TWG command contract.
func RunTWGFixtureProcess(t *testing.T) {
	endpoint := os.Getenv("RADAR_TWG_FIXTURE_URL")
	if endpoint == "" {
		return
	}
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	value := func(flag string) string {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--version") {
		fmt.Print("1.3.8")
		os.Exit(0)
	}
	var result any
	switch {
	case strings.Contains(joined, "auth profile list"):
		result = map[string]any{"apiVersion": "v2", "data": map[string]any{"profiles": []any{map[string]any{"name": "radar", "authMethod": "oauth", "cloudId": "cloud-fixture", "userId": "fixture-account", "user": "fixture@example.invalid", "domain": "jira.example.test", "environment": "prod"}}}}
	case strings.Contains(joined, "doctor"):
		result = map[string]any{"data": map[string]any{"auth": map[string]any{"resolved": map[string]any{"authMethod": "oauth"}}, "connectivity": map[string]any{"attempted": true, "ok": true, "userId": "ari:cloud:identity::user/fixture-account"}}}
	default:
		var response *http.Response
		var err error
		cmd := "jira.workitem.query"
		if strings.Contains(joined, "workitem get") {
			key := ""
			for i, a := range args {
				if a == "get" && i+1 < len(args) {
					key = args[i+1]
				}
			}
			response, err = http.Get(endpoint + "/issue/" + key)
			cmd = "jira.workitem.get"
		} else {
			body, _ := json.Marshal(map[string]any{"jql": value("--jql"), "maxResults": json.Number(value("--first")), "fields": strings.Split(value("--fields"), ",")})
			response, err = http.Post(endpoint+"/search/jql", "application/json", bytes.NewReader(body))
		}
		if err != nil {
			os.Exit(1)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode >= 300 {
			os.Exit(1)
		}
		flatten := func(row map[string]any) map[string]any {
			if fields, ok := row["fields"].(map[string]any); ok {
				delete(row, "fields")
				for k, v := range fields {
					row[k] = v
				}
			}
			return row
		}
		var raw map[string]any
		if json.Unmarshal(data, &raw) != nil {
			os.Exit(1)
		}
		if cmd == "jira.workitem.get" {
			result = map[string]any{"apiVersion": "v2", "command": cmd, "data": []any{flatten(raw)}, "meta": map[string]any{"acquisition": map[string]int{"requested": 1, "returned": 1, "succeeded": 1}}}
		} else {
			rows := []any{}
			if items, ok := raw["issues"].([]any); ok {
				for _, r := range items {
					rows = append(rows, flatten(r.(map[string]any)))
				}
			}
			result = map[string]any{"apiVersion": "v2", "command": cmd, "data": map[string]any{"issues": rows}}
		}
	}
	json.NewEncoder(os.Stdout).Encode(result)
	os.Exit(0)
}

func InstallTWGFixture(t *testing.T, endpoint string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run=^TestTWGFixtureProcess$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "twg"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RADAR_TWG_FIXTURE_URL", endpoint)
}
