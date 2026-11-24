package diagnostics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func repositoryPolicyBoundaryFixture() protocol.DiagnosticRepositoryPolicy {
	view := protocol.DiagnosticRepositoryPolicy{
		ID: "workspace-resource:" + strings.Repeat("a", 64), WorkspaceID: "registered",
		Origin: "current_registered_member_repository", Available: true,
		Evidence: "fresh_bounded_repository_configuration", Settings: []protocol.DiagnosticSetting{},
	}
	for _, key := range []string{"copy_files", "setup", "model", "thinking", "sbx.enabled", "sbx.ready_command", "sbx.env_file", "sbx.kit.name", "sbx.kit.path", "sbx.additional_mounts"} {
		value := "false"
		switch key {
		case "copy_files", "setup", "sbx.additional_mounts":
			value = "0"
		case "sbx.enabled":
			value = "inherit"
		}
		view.Settings = append(view.Settings, protocol.DiagnosticSetting{Key: key, Value: value, Origin: "repository_default", Declaration: "absent"})
	}
	return view
}

func TestRepositoryPolicyBoundaryRejectsInvalidPublicEvidence(t *testing.T) {
	private := "private-policy-command-and-path"
	cases := map[string]func(*protocol.DiagnosticRepositoryPolicy){
		"available_evidence": func(v *protocol.DiagnosticRepositoryPolicy) { v.Evidence = private },
		"available_reason":   func(v *protocol.DiagnosticRepositoryPolicy) { v.Reason = private },
		"nil_settings":       func(v *protocol.DiagnosticRepositoryPolicy) { v.Settings = nil },
		"empty_settings":     func(v *protocol.DiagnosticRepositoryPolicy) { v.Settings = []protocol.DiagnosticSetting{} },
		"missing_setting":    func(v *protocol.DiagnosticRepositoryPolicy) { v.Settings = v.Settings[:9] },
		"extra_setting": func(v *protocol.DiagnosticRepositoryPolicy) {
			v.Settings = append(v.Settings, protocol.DiagnosticSetting{Key: private})
		},
		"unknown_setting":      func(v *protocol.DiagnosticRepositoryPolicy) { v.Settings[0].Key = private },
		"duplicate_setting":    func(v *protocol.DiagnosticRepositoryPolicy) { v.Settings[1] = v.Settings[0] },
		"folded_setting":       func(v *protocol.DiagnosticRepositoryPolicy) { v.Settings[0].Key = "Copy_Files" },
		"private_value":        func(v *protocol.DiagnosticRepositoryPolicy) { v.Settings[2].Value = private },
		"private_origin":       func(v *protocol.DiagnosticRepositoryPolicy) { v.Settings[0].Origin = private },
		"private_declaration":  func(v *protocol.DiagnosticRepositoryPolicy) { v.Settings[0].Declaration = private },
		"value_default_origin": func(v *protocol.DiagnosticRepositoryPolicy) { v.Settings[0].Declaration = "value" },
		"absent_file_origin":   func(v *protocol.DiagnosticRepositoryPolicy) { v.Settings[0].Origin = "repository_file" },
		"null_nondefault": func(v *protocol.DiagnosticRepositoryPolicy) {
			v.Settings[2].Declaration, v.Settings[2].Value = "null", "true"
		},
		"absent_nondefault": func(v *protocol.DiagnosticRepositoryPolicy) { v.Settings[0].Value = "1" },
		"absent_configuration_value": func(v *protocol.DiagnosticRepositoryPolicy) {
			v.Evidence = "repository_configuration_absent"
			v.Settings[0].Declaration, v.Settings[0].Origin = "value", "repository_file"
		},
		"absent_configuration_null": func(v *protocol.DiagnosticRepositoryPolicy) {
			v.Evidence = "repository_configuration_absent"
			v.Settings[0].Declaration = "null"
		},
	}
	for _, value := range []string{"", "-1", "+1", "01", "1.0", "1e2", " 1", "1 ", "9007199254740992", "18446744073709551616", private} {
		value := value
		cases["invalid_count_"+value] = func(v *protocol.DiagnosticRepositoryPolicy) {
			v.Settings[0].Value, v.Settings[0].Declaration, v.Settings[0].Origin = value, "value", "repository_file"
		}
	}
	for _, value := range []string{"", "True", "1", "automatic", private} {
		value := value
		cases["invalid_boolean_"+value] = func(v *protocol.DiagnosticRepositoryPolicy) {
			v.Settings[2].Value, v.Settings[2].Declaration, v.Settings[2].Origin = value, "value", "repository_file"
		}
		cases["invalid_mode_"+value] = func(v *protocol.DiagnosticRepositoryPolicy) {
			v.Settings[4].Value, v.Settings[4].Declaration, v.Settings[4].Origin = value, "value", "repository_file"
		}
	}
	for _, reason := range []string{"", private} {
		reason := reason
		cases["unavailable_reason_"+reason] = func(v *protocol.DiagnosticRepositoryPolicy) {
			v.Available, v.Evidence, v.Reason, v.Settings = false, "unavailable", reason, []protocol.DiagnosticSetting{}
		}
	}
	cases["unavailable_settings"] = func(v *protocol.DiagnosticRepositoryPolicy) {
		v.Available, v.Evidence, v.Reason = false, "unavailable", "repository_unavailable"
	}
	cases["unavailable_nil_settings"] = func(v *protocol.DiagnosticRepositoryPolicy) {
		v.Available, v.Evidence, v.Reason, v.Settings = false, "unavailable", "repository_unavailable", nil
	}
	cases["unavailable_evidence"] = func(v *protocol.DiagnosticRepositoryPolicy) {
		v.Available, v.Evidence, v.Reason, v.Settings = false, private, "repository_unavailable", []protocol.DiagnosticSetting{}
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			store, _, _ := fixture(t)
			view := repositoryPolicyBoundaryFixture()
			mutate(&view)
			calls := 0
			provider := repositoryPolicyProvider{calls: &calls, view: view}
			req := protocol.DiagnosticsRequest{Section: "repository_policy", ParentID: "registered", ID: repositoryPolicyBoundaryFixture().ID}
			revision := store.Revision()
			page, err := Probe(context.Background(), store, req, false, integration.NewRegistry(provider))
			if err == nil {
				t.Fatal("invalid provider evidence reached public diagnostic page")
			}
			if strings.Contains(err.Error(), private) || !reflect.DeepEqual(page, protocol.DiagnosticsPage{}) || calls != 1 || store.Revision() != revision {
				t.Fatal("refusal leaked evidence, changed state or retried the provider")
			}
		})
	}
}

func TestRepositoryPolicyBoundaryPreservesFiniteEvidenceAndDetachesSettings(t *testing.T) {
	cases := map[string]func(*protocol.DiagnosticRepositoryPolicy){
		"absent_configuration": func(v *protocol.DiagnosticRepositoryPolicy) { v.Evidence = "repository_configuration_absent" },
		"null_declarations": func(v *protocol.DiagnosticRepositoryPolicy) {
			for i := range v.Settings {
				v.Settings[i].Declaration = "null"
			}
		},
		"explicit_defaults": func(v *protocol.DiagnosticRepositoryPolicy) {
			for i := range v.Settings {
				v.Settings[i].Origin, v.Settings[i].Declaration = "repository_file", "value"
			}
		},
		"explicit_values": func(v *protocol.DiagnosticRepositoryPolicy) {
			for i := range v.Settings {
				v.Settings[i].Origin, v.Settings[i].Declaration = "repository_file", "value"
				v.Settings[i].Value = "true"
			}
			v.Settings[0].Value, v.Settings[1].Value, v.Settings[9].Value, v.Settings[4].Value = "9007199254740991", "12", "3", "required"
		},
		"disabled": func(v *protocol.DiagnosticRepositoryPolicy) {
			v.Settings[4].Value, v.Settings[4].Origin, v.Settings[4].Declaration = "disabled", "repository_file", "value"
		},
	}
	for _, reason := range []string{"repository_unavailable", "configuration_read_unavailable", "configuration_not_regular_or_exceeds_bound", "configuration_changed_or_read_incomplete", "configuration_invalid", "registration_changed_or_unavailable", "cancelled_or_timed_out"} {
		reason := reason
		cases[reason] = func(v *protocol.DiagnosticRepositoryPolicy) {
			v.Available, v.Evidence, v.Reason, v.Settings = false, "unavailable", reason, []protocol.DiagnosticSetting{}
		}
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			store, _, _ := fixture(t)
			view := repositoryPolicyBoundaryFixture()
			mutate(&view)
			calls := 0
			page, err := Probe(context.Background(), store, protocol.DiagnosticsRequest{Section: "repository_policy", ParentID: view.WorkspaceID, ID: view.ID}, true, integration.NewRegistry(repositoryPolicyProvider{calls: &calls, view: view}))
			if err != nil || calls != 1 || len(page.Items) != 1 || !page.ResourceRefreshPending || !reflect.DeepEqual(page.Items[0].RepositoryPolicy, &view) {
				t.Fatalf("valid finite evidence changed: %v", err)
			}
			before, _ := json.Marshal(page)
			if len(view.Settings) != 0 {
				view.Settings[0].Value = "private-late-provider-command"
			}
			after, _ := json.Marshal(page)
			if string(before) != string(after) {
				t.Fatal("published page retained borrowed mutable provider settings")
			}
		})
	}
}

func TestRepositoryPolicyBoundaryAcceptsNativeConfigurationDeclarations(t *testing.T) {
	for name, data := range map[string]string{
		"empty_mapping":       "{}\n",
		"explicit_defaults":   "copy_files: []\nsetup: []\nmodel: ''\nthinking: ''\nsbx: {enabled: false, env_file: '', ready_command: [], additional_mounts: []}\n",
		"nulls":               "copy_files: null\nsetup: null\nmodel: null\nthinking: null\nsbx: {enabled: null, env_file: null, ready_command: null, kit: null, additional_mounts: null}\n",
		"parent_null":         "sbx: null\n",
		"aliases_and_merges":  "defaults: &d {enabled: true, env_file: /never-read-private-env, ready_command: [private-command], kit: {name: private-kit, path: /never-read-private-kit}, additional_mounts: [/never-read-private-mount]}\nsbx: {<<: *d}\ncopy_files: [private-copy]\nsetup: [private-setup]\nmodel: private-model\nthinking: high\n",
		"merge_override_null": "defaults: &d {enabled: true}\nsbx: {<<: *d, enabled: null}\n",
		"whole_alias":         "defaults: &d {enabled: true}\nsbx: *d\n",
	} {
		t.Run(name, func(t *testing.T) {
			store, root, path := fixture(t)
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			repo := filepath.Join(base, "source")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, ".radar.yaml"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			member := workspacegroup.Member{Repository: repo, Path: filepath.Join(path, "member"), Branch: "fixture"}
			group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "fixture", Path: path, Members: []workspacegroup.Member{member}}
			if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
				t.Fatal(err)
			}
			id := protocol.DiagnosticWorkspaceResourceID(group.ID, protocol.DiagnosticWorkspaceResource{Kind: "member", Member: &protocol.DiagnosticMember{Repository: repo, Path: member.Path}})
			page, err := Probe(context.Background(), store, protocol.DiagnosticsRequest{Section: "repository_policy", ParentID: group.ID, ID: id, Workspace: path}, false, integration.NewRegistry(workspace.Source{}))
			if err != nil || len(page.Items) != 1 || !page.Items[0].RepositoryPolicy.Available || page.Items[0].RepositoryPolicy.Evidence != "fresh_bounded_repository_configuration" || len(page.Items[0].RepositoryPolicy.Settings) != 10 {
				t.Fatalf("native declarations refused: %v", err)
			}
			encoded, _ := json.Marshal(page)
			if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), repo) {
				t.Fatal("private native configuration leaked")
			}
		})
	}
}
