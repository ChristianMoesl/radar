package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func repositoryPolicyFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, repo := filepath.Join(base, "workspaces"), filepath.Join(base, "private-repository-ABC-123")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(filepath.Join(root, "fixture")), Name: "fixture", Path: filepath.Join(root, "fixture"), Members: []workspacegroup.Member{{Repository: repo, Path: filepath.Join(root, "fixture", "member"), Branch: "fixture"}}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	member := protocol.DiagnosticMember{Repository: repo, Path: group.Members[0].Path}
	id := protocol.DiagnosticWorkspaceResourceID(group.ID, protocol.DiagnosticWorkspaceResource{Kind: "member", Member: &member})
	return root, repo, group.Path, id
}

func TestRepositoryPolicyNativeValuesAndDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name, data   string
		absent       bool
		values       map[string]string
		declarations map[string]string
	}{
		{name: "absent", absent: true},
		{name: "empty_mapping", data: "{}\n"},
		{name: "explicit_defaults", data: "copy_files: []\nsetup: []\nmodel: ''\nthinking: ''\nsbx: {enabled: false, env_file: '', ready_command: [], additional_mounts: []}\n", values: map[string]string{"sbx.enabled": "disabled"}, declarations: map[string]string{"copy_files": "value", "setup": "value", "model": "value", "thinking": "value", "sbx.enabled": "value", "sbx.env_file": "value", "sbx.ready_command": "value", "sbx.additional_mounts": "value"}},
		{name: "nulls", data: "copy_files: null\nsetup: null\nmodel: null\nthinking: null\nsbx: {enabled: null, env_file: null, ready_command: null, kit: null, additional_mounts: null}\n", declarations: map[string]string{"copy_files": "null", "setup": "null", "model": "null", "thinking": "null", "sbx.enabled": "null", "sbx.env_file": "null", "sbx.ready_command": "null", "sbx.additional_mounts": "null"}},
		{name: "parent_null", data: "sbx: null\n"},
		{name: "aliases_and_merges", data: "defaults: &d {enabled: true, env_file: /never-read-private-env, ready_command: [private-command], kit: {name: private-kit, path: /never-read-private-kit}, additional_mounts: [/never-read-private-mount]}\nsbx: {<<: *d}\ncopy_files: [private-copy]\nsetup: [private-setup]\nmodel: private-model\nthinking: high\n", values: map[string]string{"copy_files": "1", "setup": "1", "model": "true", "thinking": "true", "sbx.enabled": "required", "sbx.env_file": "true", "sbx.ready_command": "true", "sbx.kit.name": "true", "sbx.kit.path": "true", "sbx.additional_mounts": "1"}, declarations: map[string]string{"copy_files": "value", "setup": "value", "model": "value", "thinking": "value", "sbx.enabled": "value", "sbx.env_file": "value", "sbx.ready_command": "value", "sbx.kit.name": "value", "sbx.kit.path": "value", "sbx.additional_mounts": "value"}},
		{name: "merge_override_null", data: "defaults: &d {enabled: true}\nsbx: {<<: *d, enabled: null}\n", declarations: map[string]string{"sbx.enabled": "null"}},
		{name: "whole_alias", data: "defaults: &d {enabled: true}\nsbx: *d\n", values: map[string]string{"sbx.enabled": "required"}, declarations: map[string]string{"sbx.enabled": "value"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, repo, scope, id := repositoryPolicyFixture(t)
			path := filepath.Join(repo, ".radar.yaml")
			if !tc.absent {
				if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
					t.Fatal(err)
				}
				if _, _, err := decodeRepoConfig([]byte(tc.data)); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(workspacegroup.Path(root))
			view, err := diagnosticRepositoryPolicy(context.Background(), root, workspacegroup.ID(scope), id, scope, captureRepositoryConfiguration)
			if err != nil || !view.Available || len(view.Settings) != 10 || view.ID != id || view.WorkspaceID != workspacegroup.ID(scope) || view.Origin != "current_registered_member_repository" {
				t.Fatalf("view=%+v err=%v", view, err)
			}
			expected := "fresh_bounded_repository_configuration"
			if tc.absent {
				expected = "repository_configuration_absent"
			}
			if view.Evidence != expected || view.Reason != "" {
				t.Fatalf("wrong evidence: %+v", view)
			}
			for _, setting := range view.Settings {
				value := "false"
				if setting.Key == "copy_files" || setting.Key == "setup" || setting.Key == "sbx.additional_mounts" {
					value = "0"
				}
				if setting.Key == "sbx.enabled" {
					value = "inherit"
				}
				if v, ok := tc.values[setting.Key]; ok {
					value = v
				}
				declaration := "absent"
				if d, ok := tc.declarations[setting.Key]; ok {
					declaration = d
				}
				origin := "repository_default"
				if declaration == "value" {
					origin = "repository_file"
				}
				if setting.Value != value || setting.Declaration != declaration || setting.Origin != origin {
					t.Fatalf("unexpected setting: %+v expected %s/%s/%s", setting, value, declaration, origin)
				}
			}
			encoded, _ := json.Marshal(view)
			for _, private := range []string{repo, "private-command", "private-kit", "private-copy", "private-setup", "private-model", "never-read"} {
				if strings.Contains(string(encoded), private) {
					t.Fatalf("private value leaked: %s", encoded)
				}
			}
			after, _ := os.ReadFile(workspacegroup.Path(root))
			if string(before) != string(after) {
				t.Fatal("registration mutated")
			}
			if tc.absent {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("absent config created")
				}
			}
		})
	}
}

func TestRepositoryPolicyUnsafeFilesAndInvalidNativeConfiguration(t *testing.T) {
	for _, kind := range []string{"repository_missing", "repository_symlink", "ancestor_symlink", "config_symlink", "dangling_symlink", "fifo", "directory", "oversized", "empty", "malformed", "duplicate", "multiple_documents", "invalid_copy", "invalid_env", "invalid_ready", "invalid_thinking", "invalid_kit", "invalid_unknown_duplicate"} {
		t.Run(kind, func(t *testing.T) {
			root, repo, scope, id := repositoryPolicyFixture(t)
			path := filepath.Join(repo, ".radar.yaml")
			var err error
			switch kind {
			case "repository_missing":
				err = os.Remove(repo)
			case "repository_symlink":
				err = os.Rename(repo, repo+"-moved")
				if err == nil {
					err = os.Symlink(repo+"-moved", repo)
				}
			case "ancestor_symlink":
				parent := filepath.Dir(repo)
				moved := parent + "-moved"
				// Use a child alias rather than rename the test's cleanup root.
				real := filepath.Join(parent, "real")
				err = os.Mkdir(real, 0700)
				if err == nil {
					err = os.Rename(repo, filepath.Join(real, filepath.Base(repo)))
				}
				if err == nil {
					err = os.Symlink(real, moved)
				}
				if err == nil {
					registry, _ := workspacegroup.Load(root)
					registry.Workspaces[0].Members[0].Repository = filepath.Join(moved, filepath.Base(repo))
					err = workspacegroup.Save(root, registry)
					id = protocol.DiagnosticWorkspaceResourceID(workspacegroup.ID(scope), protocol.DiagnosticWorkspaceResource{Kind: "member", Member: &protocol.DiagnosticMember{Repository: registry.Workspaces[0].Members[0].Repository, Path: registry.Workspaces[0].Members[0].Path}})
					t.Cleanup(func() { os.Remove(moved) })
				}
			case "config_symlink":
				err = os.WriteFile(path+"-private", []byte("model: private-model\n"), 0600)
				if err == nil {
					err = os.Symlink(path+"-private", path)
				}
			case "dangling_symlink":
				err = os.Symlink(path+"-missing", path)
			case "fifo":
				err = unix.Mkfifo(path, 0600)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "oversized":
				err = os.WriteFile(path, []byte(strings.Repeat("x", diagnosticRepositoryLimit+1)), 0600)
			default:
				data := map[string]string{"empty": "", "malformed": "model: [private-bad", "duplicate": "model: a\nmodel: b", "multiple_documents": "{}\n---\n{}", "invalid_copy": "copy_files: [../private-copy]", "invalid_env": "sbx: {env_file: private-relative}", "invalid_ready": "sbx: {ready_command: ['']}", "invalid_thinking": "thinking: private-invalid", "invalid_kit": "sbx: {kit: {path: /private-kit}}", "invalid_unknown_duplicate": "unknown: {secret: one, secret: two}"}[kind]
				err = os.WriteFile(path, []byte(data), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			view, err := diagnosticRepositoryPolicy(context.Background(), root, workspacegroup.ID(scope), id, scope, captureRepositoryConfiguration)
			if err != nil || view.Available || view.Evidence != "unavailable" || view.Reason == "" || len(view.Settings) != 0 {
				t.Fatalf("unsafe evidence: %+v %v", view, err)
			}
			encoded, _ := json.Marshal(view)
			if strings.Contains(string(encoded), "private-") {
				t.Fatalf("private error leaked: %s", encoded)
			}
		})
	}
}

func TestRepositoryPolicyExactScopeDriftAndNoFallback(t *testing.T) {
	root, repo, scope, id := repositoryPolicyFixture(t)
	if err := os.WriteFile(filepath.Join(repo, ".radar.yaml"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ parent, member, scope string }{{"other", id, scope}, {workspacegroup.ID(scope), id, scope + "-other"}, {workspacegroup.ID(scope), "workspace-resource:" + strings.Repeat("a", 64), scope}} {
		calls := 0
		capture := func(string) (*repositoryConfigurationCapture, string) { calls++; return nil, "repository_unavailable" }
		if _, err := diagnosticRepositoryPolicy(context.Background(), root, tc.parent, tc.member, tc.scope, capture); err == nil || calls != 0 {
			t.Fatal("out-of-scope file access")
		}
	}
	for _, drift := range []string{"replace_file", "write_file", "remove_file", "replace_repository", "registration", "absent_to_present"} {
		t.Run(drift, func(t *testing.T) {
			root, repo, scope, id := repositoryPolicyFixture(t)
			path := filepath.Join(repo, ".radar.yaml")
			if drift != "absent_to_present" {
				if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			capture := func(repository string) (*repositoryConfigurationCapture, string) {
				c, reason := captureRepositoryConfiguration(repository)
				if c == nil {
					t.Fatalf("capture failed: %s", reason)
				}
				var err error
				switch drift {
				case "replace_file":
					err = os.Rename(path, path+"-old")
					if err == nil {
						err = os.WriteFile(path, []byte("{}\n"), 0600)
					}
				case "write_file":
					err = os.WriteFile(path, []byte("model: private-changed\n"), 0600)
				case "remove_file":
					err = os.Remove(path)
				case "replace_repository":
					err = os.Rename(repo, repo+"-old")
					if err == nil {
						err = os.Mkdir(repo, 0700)
					}
				case "registration":
					reg, _ := workspacegroup.Load(root)
					reg.Workspaces[0].Name = "changed"
					err = workspacegroup.Save(root, reg)
				case "absent_to_present":
					err = os.WriteFile(path, []byte("{}\n"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				return c, reason
			}
			view, err := diagnosticRepositoryPolicy(context.Background(), root, workspacegroup.ID(scope), id, scope, capture)
			if err != nil || view.Available || len(view.Settings) != 0 {
				t.Fatalf("drift accepted: %+v %v", view, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	view, err := diagnosticRepositoryPolicy(ctx, root, workspacegroup.ID(scope), id, scope, func(string) (*repositoryConfigurationCapture, string) { calls++; return nil, "repository_unavailable" })
	if err != nil || view.Available || view.Reason != "cancelled_or_timed_out" || calls != 0 {
		t.Fatal("cancelled probe read configuration")
	}
}

func TestRepositoryPolicyNeverReadsReferencedFiles(t *testing.T) {
	root, repo, scope, id := repositoryPolicyFixture(t)
	reference := filepath.Join(repo, "private-reference")
	if err := unix.Mkfifo(reference, 0600); err != nil {
		t.Fatal(err)
	}
	data := "copy_files: [private-reference]\nsetup: [private-reference]\nsbx:\n  env_file: " + reference + "\n  kit: {name: private-kit, path: " + reference + "}\n  ready_command: [" + reference + "]\n  additional_mounts: [" + reference + "]\n"
	if err := os.WriteFile(filepath.Join(repo, ".radar.yaml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	view, err := diagnosticRepositoryPolicy(context.Background(), root, workspacegroup.ID(scope), id, scope, captureRepositoryConfiguration)
	if err != nil || !view.Available {
		t.Fatalf("referenced files inspected: %+v %v", view, err)
	}
}

func TestRepositoryPolicyInvalidOrAmbiguousRegistrationNeverReadsFiles(t *testing.T) {
	for _, kind := range []string{"missing", "invalid", "duplicate_parent", "duplicate_member"} {
		t.Run(kind, func(t *testing.T) {
			root, _, scope, id := repositoryPolicyFixture(t)
			registry, err := workspacegroup.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing":
				err = os.Remove(workspacegroup.Path(root))
			case "invalid":
				err = os.WriteFile(workspacegroup.Path(root), []byte("private-invalid-registry"), 0600)
			default:
				if kind == "duplicate_parent" {
					registry.Workspaces = append(registry.Workspaces, registry.Workspaces[0])
				} else {
					registry.Workspaces[0].Members = append(registry.Workspaces[0].Members, registry.Workspaces[0].Members[0])
				}
				data, _ := json.Marshal(registry)
				err = os.WriteFile(workspacegroup.Path(root), data, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			_, err = diagnosticRepositoryPolicy(context.Background(), root, workspacegroup.ID(scope), id, scope, func(string) (*repositoryConfigurationCapture, string) { calls++; return nil, "repository_unavailable" })
			if err == nil || calls != 0 || strings.Contains(err.Error(), "private-") {
				t.Fatalf("unsafe registration: calls=%d err=%v", calls, err)
			}
		})
	}
}
