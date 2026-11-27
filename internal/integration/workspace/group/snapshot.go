package workspacegroup

import "encoding/json"

// RegistrationSnapshot detaches a complete workspace in the same canonical
// representation used by the native registry. It performs no filesystem I/O
// and grants no admission; callers must still compare the live registration.
func RegistrationSnapshot(workspace Workspace) (Workspace, error) {
	data, err := json.Marshal(workspace)
	if err != nil {
		return Workspace{}, err
	}
	var detached Workspace
	if err := json.Unmarshal(data, &detached); err != nil {
		return Workspace{}, err
	}
	registry := Registry{Version: Version, Workspaces: []Workspace{detached}}
	if err := normalizeAndValidate(&registry); err != nil {
		return Workspace{}, err
	}
	return registry.Workspaces[0], nil
}
