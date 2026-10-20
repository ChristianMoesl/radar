package terraform

import (
	"context"
	"fmt"
	"time"

	"radar/internal/config"
	"radar/internal/integration"
)

func (s Source) ResolveBindings(ctx context.Context, req integration.BindingRequest) integration.CollectResult {
	result := integration.CollectResult{Complete: true}
	if len(req.Bindings) == 0 {
		return result
	}
	cfg, err := config.Load()
	if err != nil {
		addFailure(&result, "could not load config")
		return result
	}
	if status := integration.OptionalStatus("terraform", cfg.Terraform.Enabled, configured(cfg.Terraform)); !status.CanRun {
		return integration.CollectResult{SourceStatus: &status.Status}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	session, err := (client{profile: cfg.Terraform.Profile, output: s.output}).connect(ctx, cfg.Terraform)
	if err != nil {
		addFailure(&result, err.Error())
		return result
	}
	observed := map[string]bool{}
	for _, item := range req.Result.Observations {
		observed[item.Ref.ID] = true
	}
	for _, b := range req.Bindings {
		host, run, valid := parseIdentity(b.ID)
		if b.Source != "terraform" || b.Kind != "run" || b.Key != "" || !b.WorkItem || !valid || host != session.host {
			addFailure(&result, fmt.Sprintf("invalid or mismatched Terraform run binding %q", b.ID))
			continue
		}
		if observed[b.ID] {
			continue
		}
		observation, err := s.readRun(ctx, session, cfg.Terraform, run, integration.CollectRequest{Previous: req.Previous, LinkingMarks: req.LinkingMarks, Logger: req.Logger})
		if err != nil {
			addFailure(&result, err.Error())
			continue
		}
		result.Observations = append(result.Observations, observation)
		observed[b.ID] = true
	}
	return result
}
