package integration

type WorkspaceContext struct {
	CurrentPath        string                       `json:"current_path"`
	WorkspaceRoot      string                       `json:"workspace_root"`
	WorkspaceID        string                       `json:"workspace_id"`
	WorkspaceName      string                       `json:"workspace_name"`
	WorkspacePath      string                       `json:"workspace_path"`
	Registered         bool                         `json:"registered"`
	EnrollmentRequired bool                         `json:"enrollment_required"`
	SessionName        string                       `json:"session_name,omitempty"`
	Revision           string                       `json:"revision"`
	Capabilities       WorkspaceContextCapabilities `json:"capabilities"`
	Desired            DesiredWorkspaceDescription  `json:"desired"`
	Note               *WorkspaceContextNote        `json:"note,omitempty"`
	Sandbox            *WorkspaceContextSandbox     `json:"sandbox,omitempty"`
	Members            []WorkspaceContextMember     `json:"members"`
	Repositories       []WorkspaceContextRepository `json:"repositories"`
}

type WorkspaceContextCapabilities struct {
	Worktrees        bool `json:"worktrees"`
	Sandbox          bool `json:"sandbox"`
	AdditionalMounts bool `json:"additional_mounts"`
	PortForwarding   bool `json:"port_forwarding"`
}

type WorkspaceContextMember struct {
	Repository       string   `json:"repository"`
	Path             string   `json:"path"`
	Branch           string   `json:"branch"`
	Dirty            bool     `json:"dirty"`
	InstructionFiles []string `json:"instruction_files"`
	SkillPaths       []string `json:"skill_paths"`
}

type WorkspaceContextNote struct {
	Path          string `json:"path"`
	WorkspacePath string `json:"workspace_path"`
	LinkingKey    string `json:"linking_key"`
}

type WorkspaceContextSandbox struct {
	SharedDirectory      string        `json:"shared_directory,omitempty"`
	SharedDirectoryReady bool          `json:"shared_directory_ready"`
	Name                 string        `json:"name"`
	Agent                string        `json:"agent"`
	KitPath              string        `json:"kit_path,omitempty"`
	Mounts               []string      `json:"mounts"`
	Ports                []SandboxPort `json:"ports"`
}

type WorkspaceContextRepository struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	AlreadyMember bool   `json:"already_member"`
}

type RepositoryRefs struct {
	Repository    string             `json:"repository"`
	DefaultBranch string             `json:"default_branch,omitempty"`
	BaseRefs      []string           `json:"base_refs"`
	Branches      []RepositoryBranch `json:"branches"`
	Warning       string             `json:"warning,omitempty"`
}

type RepositoryBranch struct {
	Name            string   `json:"name"`
	Local           bool     `json:"local"`
	Origin          bool     `json:"origin"`
	CheckedOutPaths []string `json:"checked_out_paths,omitempty"`
}
