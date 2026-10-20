package terraform

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"radar/internal/integration"
	"radar/internal/openurl"
	"radar/internal/protocol"
)

func actionURL(ref protocol.SourceRef, action string) (string, bool) {
	host, run, ok := parseIdentity(ref.ID)
	if !ok || ref.Source != "terraform" || ref.Kind != "run" {
		return "", false
	}
	value := ref.URL
	if action == "terraform.open_workspace" {
		value = ref.Metadata["workspace_url"]
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host != host || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 && len(parts) != 6 || len(parts) < 4 || parts[0] != "app" || parts[2] != "workspaces" || parts[1] == "" || parts[3] == "" {
		return "", false
	}
	if action == "terraform.open_run" && (len(parts) != 6 || parts[4] != "runs" || parts[5] != run) {
		return "", false
	}
	if action == "terraform.open_workspace" && len(parts) != 4 {
		return "", false
	}
	return value, true
}

func (Source) Actions(_ context.Context, req integration.ActionRequest) []integration.Action {
	var actions []integration.Action
	for _, item := range []struct{ id, label string }{{"terraform.open_run", "Open Terraform run"}, {"terraform.open_workspace", "Open Terraform workspace"}} {
		if value, ok := actionURL(req.Ref, item.id); ok {
			actions = append(actions, integration.Action{ID: item.id, Source: "Terraform", Label: item.label, Detail: value, Ref: req.Ref})
		}
	}
	return actions
}

func (Source) RunAction(ctx context.Context, req integration.RunActionRequest) (integration.ActionResult, error) {
	if req.ActionID != "terraform.open_run" && req.ActionID != "terraform.open_workspace" {
		return integration.ActionResult{}, fmt.Errorf("unknown Terraform action")
	}
	value, ok := actionURL(req.Ref, req.ActionID)
	if !ok {
		return integration.ActionResult{}, fmt.Errorf("invalid Terraform link")
	}
	if err := openurl.Open(ctx, value); err != nil {
		return integration.ActionResult{}, err
	}
	return integration.ActionResult{Message: "Opened Terraform"}, nil
}
