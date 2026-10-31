package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/protocol"
)

type Source struct{ manager *agentservice.Manager }

func NewSource(workspace integration.WorkspaceExecutor) *Source {
	return &Source{manager: agentservice.New(workspace.AgentWorkspace)}
}
func (*Source) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "agent", Label: "Agents", DisplayOrder: 10}
}
func (*Source) Local() bool                               { return true }
func (s *Source) Run(ctx context.Context, changed func()) { s.manager.Run(ctx, changed) }
func (s *Source) ManageAgent(ctx context.Context, req protocol.AgentRequest) (json.RawMessage, error) {
	return s.manager.Handle(ctx, req)
}

func (s *Source) ObserveCollection(ctx context.Context, source string, items []integration.CollectionItem) error {
	return s.manager.ObserveCollection(ctx, source, items)
}

func (s *Source) Status(ctx context.Context, _ *slog.Logger) integration.StatusResult {
	data, err := s.manager.Handle(ctx, protocol.AgentRequest{Operation: "list"})
	status := protocol.SourceStatus{Name: "agent", Status: "ok"}
	if err != nil {
		status.Status = "error"
		status.Detail = err.Error()
		return integration.StatusResult{Status: status}
	}
	var agents []protocol.AgentStatus
	if err := json.Unmarshal(data, &agents); err != nil {
		status.Status = "error"
		status.Detail = err.Error()
		return integration.StatusResult{Status: status}
	}
	if len(agents) == 0 {
		status.Status = "disabled"
		status.Detail = "no registered agents"
	}
	return integration.StatusResult{Status: status, CanRun: len(agents) > 0}
}

func (s *Source) Collect(ctx context.Context, _ integration.CollectRequest) integration.CollectResult {
	data, err := s.manager.Handle(ctx, protocol.AgentRequest{Operation: "list"})
	status := protocol.SourceStatus{Name: "agent", Status: "ok"}
	if err != nil {
		status.Status = "error"
		status.Detail = err.Error()
		return integration.CollectResult{SourceStatus: &status}
	}
	var agents []protocol.AgentStatus
	if err := json.Unmarshal(data, &agents); err != nil {
		status.Status = "error"
		status.Detail = err.Error()
		return integration.CollectResult{SourceStatus: &status}
	}
	result := integration.CollectResult{Complete: true, SourceStatus: &status}
	for _, agent := range agents {
		title, summary := agent.ID, "Stopped"
		signal := integration.SignalLowPriority
		activity := protocol.ActivityIdle
		if agent.Running {
			summary = "Idle"
		}
		if agent.Snapshot != nil {
			p := agent.Snapshot.Presentation
			if p.Title != "" {
				title = p.Title
			}
			if p.Summary != "" {
				summary = p.Summary
			}
			if p.Attention {
				signal = integration.SignalAttention
			}
			if agent.Running && agent.Snapshot.Pending > 0 {
				activity = protocol.ActivityBusy
				if !p.Attention {
					signal = integration.SignalInProgress
				}
			}
			if agent.Snapshot.Problem != "" {
				signal = integration.SignalAttention
				summary = agent.Snapshot.Problem
			}
		}
		if agent.Error != "" {
			signal = integration.SignalAttention
			summary = agent.Error
		}
		if agent.CollectionPending > 0 {
			summary += fmt.Sprintf("\n%s collection: %d pending deliveries", agent.Collection, agent.CollectionPending)
		}
		if agent.CollectionError != "" {
			signal = integration.SignalAttention
			summary += "\n" + agent.CollectionError
		}
		if len(agent.Schedules) > 0 {
			enabled := 0
			for _, s := range agent.Schedules {
				if s.Enabled {
					enabled++
				}
				if s.Error != "" {
					signal = integration.SignalAttention
					summary += "\n" + s.Name + ": " + s.Error
				}
			}
			summary += fmt.Sprintf("\nSchedules: %d enabled, %d paused", enabled, len(agent.Schedules)-enabled)
		}
		if agent.ScheduleError != "" {
			signal = integration.SignalAttention
			summary += "\n" + agent.ScheduleError
		}
		ref := protocol.SourceRef{
			AgentID: agent.ID,
			ID:      "agent:instance:" + agent.ID, EntityID: "agent:instance:" + agent.ID, Source: "agent", Kind: "agent",
			Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleResource, Authority: protocol.SourceRefAuthorityNone,
			CanonicalKey: "agent:instance:" + agent.ID, Title: title, Status: summary, Activity: activity,
			Metadata: map[string]string{"agent_id": agent.ID, "data_dir": agent.DataDir, "entrypoint": agent.Entrypoint, "workspace_path": agent.Workspace.Path, "execution": agent.Workspace.Mode},
		}
		result.Observations = append(result.Observations, integration.Observation{Ref: ref, Signal: signal, Reason: summary, TaskKind: "agent"})
	}
	return result
}
