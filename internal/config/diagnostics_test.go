package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func diagnosticConfigFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func settingMap(view DiagnosticPolicy) map[string]DiagnosticSetting {
	result := map[string]DiagnosticSetting{}
	for _, setting := range view.Settings {
		result[setting.Key] = setting
	}
	return result
}

func TestDiagnosticSettingsShareNativeValuesAndDeclarations(t *testing.T) {
	path := diagnosticConfigFixture(t)
	for _, declaration := range []string{"", "null", "true", "false"} {
		t.Run("enablement_"+declaration, func(t *testing.T) {
			data := "workspace: {auto_confirm: false}\n"
			if declaration != "" {
				for _, provider := range []string{"sbx", "github", "jira", "datadog", "terraform"} {
					data += provider + ": {enabled: " + declaration + "}\n"
				}
			}
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			native, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			view := DiagnosticSettings()
			if !view.Available || len(view.Settings) != 10 || view.Scope != "cli_process_global_defaults" || view.LocationOrigin != "xdg_config_home" || view.Evidence != "fresh_bounded_native_configuration" || len(view.Revision) != 64 {
				t.Fatalf("invalid projection: %+v", view)
			}
			if _, err := time.Parse(time.RFC3339Nano, view.CapturedAt); err != nil {
				t.Fatal(err)
			}
			values := settingMap(view)
			if values["workspace.auto_confirm"].Value != strconv.FormatBool(native.Workspace.AutoConfirm) || values["workspace.auto_confirm"].Origin != "configuration_file" {
				t.Fatal("auto-confirm differs from production")
			}
			for provider, enabled := range map[string]*bool{"sbx": native.SBX.Enabled, "github": native.GitHub.Enabled, "jira": native.Jira.Enabled, "datadog": native.Datadog.Enabled, "terraform": native.Terraform.Enabled} {
				setting := values[provider+".enabled"]
				expected := "automatic"
				if enabled != nil {
					expected = "disabled"
					if *enabled {
						expected = "required"
					}
				}
				origin, source := "compiled_default", "absent"
				if declaration == "null" {
					source = "null"
				} else if declaration != "" {
					origin, source = "configuration_file", "value"
				}
				if setting.Value != expected || setting.Origin != origin || setting.Declaration != source {
					t.Fatalf("%s: %+v, wanted %s/%s/%s", provider, setting, expected, origin, source)
				}
			}
		})
	}
}

func TestDiagnosticSettingsDefaultsNullAliasesAndMerges(t *testing.T) {
	path := diagnosticConfigFixture(t)
	cases := []struct {
		name, data, value, origin, declaration string
	}{
		{"missing", "", "true", "compiled_default", "absent"},
		{"blank", "  \n", "true", "compiled_default", "absent"},
		{"empty_map", "{}", "true", "compiled_default", "absent"},
		{"null", "workspace: {auto_confirm: null}", "true", "compiled_default", "null"},
		{"equal_to_default", "workspace: {auto_confirm: true}", "true", "configuration_file", "value"},
		{"alias", "flag: &flag false\nworkspace: {auto_confirm: *flag}", "false", "configuration_file", "value"},
		{"merge", "base: &base {auto_confirm: false}\nworkspace: {<<: *base}", "false", "configuration_file", "value"},
		{"merge_sequence", "a: &a {auto_confirm: false}\nb: &b {auto_confirm: true}\nworkspace: {<<: [*a, *b]}", "false", "configuration_file", "value"},
		{"override_merge", "base: &base {auto_confirm: false}\nworkspace: {<<: *base, auto_confirm: true}", "true", "configuration_file", "value"},
		{"null_override_merge", "base: &base {auto_confirm: false}\nworkspace: {<<: *base, auto_confirm: null}", "true", "compiled_default", "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "missing" {
				_ = os.Remove(path)
			} else if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			native, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			view := DiagnosticSettings()
			setting := settingMap(view)["workspace.auto_confirm"]
			if !view.Available || setting.Value != tc.value || setting.Value != strconv.FormatBool(native.Workspace.AutoConfirm) || setting.Origin != tc.origin || setting.Declaration != tc.declaration {
				t.Fatalf("%+v %+v", view, setting)
			}
			if tc.name == "missing" {
				if view.Evidence != "compiled_defaults_configuration_absent" {
					t.Fatal(view.Evidence)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("inspection created a configuration file")
				}
			}
		})
	}
}

func TestDiagnosticSettingsPrivacyAndRevisionOnlyBindAllowlist(t *testing.T) {
	path := diagnosticConfigFixture(t)
	private := "private-token-command-endpoint-path-ABC-123"
	data := "model: " + private + "\nsbx:\n  env_file: /" + private + "\n  ready_command: [" + private + "]\n  additional_mounts: [/" + private + ":ro]\nworkspace:\n  cleanup: {disposable_entries: [" + private + "]}\nunknown: {secret: " + private + "}\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	// Named sensitive files are never opened, even when configured as env_file.
	if err := syscall.Mkfifo(filepath.Join(filepath.Dir(path), "secrets.yaml"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	view := DiagnosticSettings()
	if !view.Available {
		t.Fatalf("%+v", view)
	}
	encoded, _ := json.Marshal(view)
	if len(encoded) > 4096 || strings.Contains(string(encoded), private) || strings.Contains(string(encoded), path) {
		t.Fatalf("private/oversized policy: %s", encoded)
	}
	settings := settingMap(view)
	for key, expected := range map[string]string{"sbx.env_file": "true", "sbx.ready_command": "true", "sbx.additional_mounts": "1", "workspace.cleanup.disposable_entries": "1"} {
		if settings[key].Value != expected || settings[key].Origin != "configuration_file" {
			t.Fatalf("%s: %+v", key, settings[key])
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("inspection wrote configuration")
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(data, private, "different-private-value")), 0600); err != nil {
		t.Fatal(err)
	}
	next := DiagnosticSettings()
	if !next.Available || view.Revision != next.Revision || !reflect.DeepEqual(view.Settings, next.Settings) {
		t.Fatal("revision leaked changes in withheld values")
	}
	if err := os.WriteFile(path, append(before, []byte("github: {enabled: false}\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if DiagnosticSettings().Revision == view.Revision {
		t.Fatal("allowlisted policy change did not invalidate revision")
	}
}

func TestDiagnosticSettingsInvalidOrUnsafeEvidenceIsUnknown(t *testing.T) {
	for _, kind := range []string{"invalid_yaml", "validation", "duplicate", "extra_document", "alias_cycle", "directory", "fifo", "symlink", "dangling_symlink", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			path := diagnosticConfigFixture(t)
			private := "private-parser-value-ABC-123"
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0700)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			case "symlink", "dangling_symlink":
				target := filepath.Join(t.TempDir(), private)
				if kind == "symlink" {
					if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				err = os.Symlink(target, path)
			default:
				data := map[string]string{"invalid_yaml": "workspace: [" + private, "validation": "thinking: " + private, "duplicate": "unknown: " + private + "\nunknown: duplicate", "extra_document": "{}\n---\nsecret: " + private, "alias_cycle": "unknown: &cycle {self: *cycle}", "oversized": strings.Repeat(" ", diagnosticConfigLimit+1)}[kind]
				err = os.WriteFile(path, []byte(data), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			view := DiagnosticSettings()
			encoded, _ := json.Marshal(view)
			if view.Available || len(view.Settings) != 0 || view.Revision != "" || view.Reason == "" || view.Evidence != "unavailable" || strings.Contains(string(encoded), private) || strings.Contains(string(encoded), path) {
				t.Fatalf("invalid input became usable policy or leaked details: %s", encoded)
			}
		})
	}
}

func TestDiagnosticSettingsHomeOriginDoesNotDiscloseEnvironment(t *testing.T) {
	diagnosticConfigFixture(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("RADAR_DISABLE_COLLECTION", "1")
	t.Setenv("PRIVATE_TOKEN", "withheld-environment-value")
	view := DiagnosticSettings()
	data, _ := json.Marshal(view)
	if !view.Available || view.LocationOrigin != "user_home" || strings.Contains(string(data), "RADAR_DISABLE_COLLECTION") || strings.Contains(string(data), "withheld-environment-value") {
		t.Fatalf("%s", data)
	}
}

func TestDiagnosticSettingsAcceptsCompleteReadAtBound(t *testing.T) {
	path := diagnosticConfigFixture(t)
	data := "{}" + strings.Repeat(" ", diagnosticConfigLimit-2)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	view := DiagnosticSettings()
	if !view.Available || len(view.Settings) != 10 {
		t.Fatalf("complete bounded file rejected: %+v", view)
	}
}
