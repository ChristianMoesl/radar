package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadOptionalSandboxReadyCommand(t *testing.T) {
	for _, tt := range []struct {
		name, sbx string
		want      []string
		invalid   bool
	}{
		{name: "omitted", sbx: `{}`},
		{name: "disabled", sbx: `{"ready_command":[]}`, want: []string{}},
		{name: "argv", sbx: `{"ready_command":["/only/in/sandbox"," spaced argument ","","$(not-a-shell)"]}`, want: []string{"/only/in/sandbox", " spaced argument ", "", "$(not-a-shell)"}},
		{name: "empty executable", sbx: `{"ready_command":["","sentinel-secret"]}`, invalid: true},
		{name: "blank executable", sbx: `{"ready_command":["  "]}`, invalid: true},
		{name: "NUL executable", sbx: `{"ready_command":["sentinel-secret\u0000"]}`, invalid: true},
		{name: "NUL argument", sbx: `{"ready_command":["ready","sentinel-secret\u0000"]}`, invalid: true},
		{name: "not argv", sbx: `{"ready_command":"ready"}`, invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			path, err := Path()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(`{"sbx":`+tt.sbx+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load()
			if tt.invalid {
				if err == nil || strings.Contains(err.Error(), "sentinel-secret") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(cfg.SBX.ReadyCommand, tt.want) {
				t.Fatalf("ready command = %#v, %v", cfg.SBX.ReadyCommand, err)
			}
			data, err := json.Marshal(cfg.SBX)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), `"ready_command"`) != (len(tt.want) > 0) {
				t.Fatalf("omitempty = %s", data)
			}
		})
	}
}

func TestDefaultConfigOmitsSandboxReadyCommand(t *testing.T) {
	cfg := Default()
	if cfg.SBX.ReadyCommand != nil {
		t.Fatalf("default = %#v", cfg.SBX.ReadyCommand)
	}
	data, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(data), `"ready_command"`) {
		t.Fatalf("default JSON = %s, %v", data, err)
	}
}
