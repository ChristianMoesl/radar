package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"
	"radar/internal/configfile"
)

const diagnosticConfigLimit = 64 << 10

// DiagnosticSetting contains only a fixed boolean, mode or count, never a
// configuration string, command, path, identity, endpoint or secret value.
type DiagnosticSetting struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Origin      string `json:"origin"`
	Declaration string `json:"declaration"`
}

type DiagnosticPolicy struct {
	Available      bool                `json:"available"`
	LocationOrigin string              `json:"location_origin"`
	Scope          string              `json:"scope"`
	Evidence       string              `json:"evidence"`
	Reason         string              `json:"reason,omitempty"`
	CapturedAt     string              `json:"captured_at"`
	Revision       string              `json:"revision,omitempty"`
	Settings       []DiagnosticSetting `json:"settings"`
}

// DiagnosticSettings observes the configured native file, never secrets.yaml,
// environment/secret files referenced by it, providers, tools or workspaces.
// These are CLI-process global defaults, not daemon environment, repository
// overrides, live access, execution eligibility or permission to perform actions.
func DiagnosticSettings() DiagnosticPolicy {
	view := DiagnosticPolicy{Scope: "cli_process_global_defaults", Evidence: "unavailable", CapturedAt: time.Now().UTC().Format(time.RFC3339Nano), Settings: []DiagnosticSetting{}}
	view.LocationOrigin = "user_home"
	if os.Getenv("XDG_CONFIG_HOME") != "" {
		view.LocationOrigin = "xdg_config_home"
	}
	path, err := Path()
	if err != nil {
		view.Reason = "configuration_path_unavailable"
		return view
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	var data []byte
	if os.IsNotExist(err) {
		// A dangling symlink is not an absent configuration file.
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			view.Reason = "configuration_read_unavailable"
			return view
		}
		view.Evidence = "compiled_defaults_configuration_absent"
	} else if err != nil {
		view.Reason = "configuration_read_unavailable"
		return view
	} else {
		defer file.Close()
		before, err := file.Stat()
		if err != nil || !before.Mode().IsRegular() || before.Size() > diagnosticConfigLimit {
			view.Reason = "configuration_not_regular_or_exceeds_bound"
			return view
		}
		data, err = io.ReadAll(io.LimitReader(file, diagnosticConfigLimit+1))
		after, statErr := file.Stat()
		current, pathErr := os.Lstat(path)
		if err != nil || statErr != nil || pathErr != nil || len(data) > diagnosticConfigLimit ||
			!os.SameFile(before, after) || !os.SameFile(before, current) || !current.Mode().IsRegular() ||
			before.Size() != after.Size() || int64(len(data)) != after.Size() || !before.ModTime().Equal(after.ModTime()) ||
			after.Size() != current.Size() || !after.ModTime().Equal(current.ModTime()) {
			view.Reason = "configuration_changed_or_read_incomplete"
			return view
		}
		view.Evidence = "fresh_bounded_native_configuration"
	}
	cfg, doc, err := decodeConfiguration(data, "")
	if err != nil {
		view.Evidence, view.Reason = "unavailable", "configuration_invalid"
		return view // Raw parser/validation errors can contain private values.
	}
	add := func(key, value string) {
		setting := DiagnosticSetting{Key: key, Value: value, Origin: "compiled_default", Declaration: "absent"}
		node := doc
		for _, part := range strings.Split(key, ".") {
			if node == nil {
				break
			}
			node = configfile.Field(node, part)
		}
		for node != nil && node.Kind == yaml.AliasNode {
			node = node.Alias
		}
		if node != nil {
			if node.Tag == "!!null" {
				setting.Declaration = "null"
			} else {
				setting.Origin, setting.Declaration = "configuration_file", "value"
			}
		}
		view.Settings = append(view.Settings, setting)
	}
	add("workspace.auto_confirm", strconv.FormatBool(cfg.Workspace.AutoConfirm))
	add("sbx.enabled", diagnosticMode(cfg.SBX.Enabled))
	add("github.enabled", diagnosticMode(cfg.GitHub.Enabled))
	add("jira.enabled", diagnosticMode(cfg.Jira.Enabled))
	add("datadog.enabled", diagnosticMode(cfg.Datadog.Enabled))
	add("terraform.enabled", diagnosticMode(cfg.Terraform.Enabled))
	// Counts/presence disclose configured policy shape, never accepted targets.
	add("workspace.cleanup.disposable_entries", strconv.Itoa(len(cfg.Workspace.Cleanup.DisposableEntries)))
	add("sbx.additional_mounts", strconv.Itoa(len(cfg.SBX.AdditionalMounts)))
	add("sbx.env_file", strconv.FormatBool(cfg.SBX.EnvFile != ""))
	add("sbx.ready_command", strconv.FormatBool(len(cfg.SBX.ReadyCommand) != 0))
	encoded, _ := json.Marshal(view.Settings)
	revision := sha256.Sum256(encoded) // Only allowlisted evidence, not private bytes.
	view.Revision = hex.EncodeToString(revision[:])
	view.Available = true
	return view
}

func diagnosticMode(value *bool) string {
	if value == nil {
		return "automatic"
	}
	if *value {
		return "required"
	}
	return "disabled"
}
