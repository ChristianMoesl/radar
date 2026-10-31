package protocol

import (
	"encoding/json"
	"fmt"
	"time"
)

const AgentProtocolVersion = 1
const AgentMaxFrameBytes = 1024 * 1024

type AgentRPCError struct {
	Code    int
	Message string
}

func (e *AgentRPCError) Error() string { return fmt.Sprintf("agent RPC %d: %s", e.Code, e.Message) }

type AgentRequest struct {
	Operation  string          `json:"operation"`
	ID         string          `json:"id,omitempty"`
	Entrypoint string          `json:"entrypoint,omitempty"`
	Workspace  string          `json:"workspace,omitempty"`
	Collection string          `json:"collection,omitempty"`
	Config     json.RawMessage `json:"config,omitempty"`
	Params     json.RawMessage `json:"params,omitempty"`
}

type AgentAction struct {
	ID      string          `json:"id"`
	Label   string          `json:"label"`
	Text    bool            `json:"text,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

type AgentConversation struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}
type AgentWorkspace struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}
type AgentPresentation struct {
	Title         string              `json:"title"`
	Summary       string              `json:"summary"`
	Attention     bool                `json:"attention"`
	Revision      int                 `json:"revision"`
	Message       bool                `json:"message"`
	Actions       []AgentAction       `json:"actions"`
	Conversations []AgentConversation `json:"conversations"`
	Workspaces    []AgentWorkspace    `json:"workspaces"`
}

type AgentSnapshot struct {
	Protocol          int               `json:"protocol"`
	InstanceID        string            `json:"instanceId"`
	DefinitionVersion int               `json:"definitionVersion"`
	PID               int               `json:"pid"`
	Presentation      AgentPresentation `json:"presentation"`
	Pending           int               `json:"pending"`
	Problem           string            `json:"problem,omitempty"`
	Tasks             json.RawMessage   `json:"tasks"`
	Recent            json.RawMessage   `json:"recent"`
}

type AgentStatus struct {
	ID                string                `json:"id"`
	Entrypoint        string                `json:"entrypoint"`
	DataDir           string                `json:"data_dir"`
	Workspace         AgentWorkspaceBinding `json:"workspace"`
	Running           bool                  `json:"running"`
	Desired           bool                  `json:"desired"`
	Error             string                `json:"error,omitempty"`
	Collection        string                `json:"collection,omitempty"`
	CollectionPending int                   `json:"collection_pending,omitempty"`
	CollectionError   string                `json:"collection_error,omitempty"`
	Schedules         []AgentScheduleStatus `json:"schedules,omitempty"`
	ScheduleError     string                `json:"schedule_error,omitempty"`
	Snapshot          *AgentSnapshot        `json:"snapshot,omitempty"`
}

type AgentScheduleConfig struct {
	Name     string          `json:"name"`
	Time     string          `json:"time"`
	Weekdays []string        `json:"weekdays"`
	Timezone string          `json:"timezone"`
	Payload  json.RawMessage `json:"payload"`
}

// Admission confirms a native event task, not execution or business success.
type AgentScheduleAdmission struct {
	ScheduledAt time.Time `json:"scheduled_at"`
	AcceptedAt  time.Time `json:"accepted_at"`
	TaskID      int       `json:"task_id"`
	EventID     string    `json:"event_id"`
	CatchUp     bool      `json:"catch_up"`
}

type AgentScheduleStatus struct {
	AgentScheduleConfig
	ID            string                  `json:"id"`
	Enabled       bool                    `json:"enabled"`
	NextDue       time.Time               `json:"next_due,omitzero"`
	PendingAt     time.Time               `json:"pending_at,omitzero"`
	LastAdmission *AgentScheduleAdmission `json:"last_admission,omitempty"`
	LastMissedAt  time.Time               `json:"last_missed_at,omitzero"`
	Error         string                  `json:"error,omitempty"`
}

// Binding is infrastructure policy, independent of the definition's domain.
type AgentWorkspaceBinding struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	Sandbox string `json:"sandbox,omitempty"`
}
