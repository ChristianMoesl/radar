package terraform

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"time"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/terraform/settings"
	"radar/internal/protocol"
	"radar/internal/setup"
)

type Source struct {
	output    outputFunc
	resolvers []integration.DevelopmentLinkResolver
}

func NewSource(resolvers ...integration.DevelopmentLinkResolver) Source {
	return Source{resolvers: resolvers}
}

func (Source) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "terraform", Label: "Terraform", DisplayOrder: 1}
}

func configured(cfg settings.Config) string {
	if cfg.Organization == "" || len(cfg.Workspaces) == 0 {
		return "select terraform.organization and terraform.workspaces with radar setup"
	}
	return ""
}

func (s Source) Status(ctx context.Context, _ *slog.Logger) integration.StatusResult {
	cfg, err := config.Load()
	if err != nil {
		return integration.StatusResult{Status: protocol.SourceStatus{Name: "terraform", Status: "error", Detail: "could not load config"}}
	}
	status := integration.OptionalStatus("terraform", cfg.Terraform.Enabled, configured(cfg.Terraform))
	if !status.CanRun {
		return status
	}
	if s.output == nil {
		if _, err := exec.LookPath("tfctl"); err != nil {
			return integration.OptionalStatus("terraform", cfg.Terraform.Enabled, "tfctl not found; run radar setup")
		}
	}
	version, err := (client{profile: cfg.Terraform.Profile, output: s.output}).version(ctx)
	if err != nil || !setup.VersionAtLeast(version, minimumVersion) {
		status.Status.Status, status.Status.Detail, status.CanRun = "error", "tfctl 0.4.0+ required; run radar setup", false
	}
	return status
}

func (s Source) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	cfg, err := config.Load()
	if err != nil {
		return failedResult(req.Previous, "could not load config")
	}
	if status := integration.OptionalStatus("terraform", cfg.Terraform.Enabled, configured(cfg.Terraform)); !status.CanRun {
		return integration.CollectResult{SourceStatus: &status.Status}
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	session, err := (client{profile: cfg.Terraform.Profile, output: s.output}).connect(ctx, cfg.Terraform)
	if err != nil {
		return failedResult(req.Previous, err.Error())
	}
	return s.collect(ctx, session, cfg.Terraform, req)
}

func previousRefs(tasks []protocol.Task) map[string]protocol.SourceRef {
	refs := map[string]protocol.SourceRef{}
	for _, task := range tasks {
		for _, ref := range task.SourceRefs {
			if ref.Source == "terraform" && ref.Kind == "run" {
				refs[ref.ID] = ref
			}
		}
	}
	return refs
}

func failedResult(previous []protocol.Task, detail string) integration.CollectResult {
	result := integration.CollectResult{SourceStatus: &protocol.SourceStatus{Name: "terraform", Status: "error", Detail: detail}}
	refs := previousRefs(previous)
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ref := refs[id]
		result.Observations = append(result.Observations, integration.Observation{Ref: ref, Signal: integration.WorkSignal(ref.Signal), Reason: ref.Status})
	}
	return result
}

func addFailure(result *integration.CollectResult, detail string) {
	result.Complete = false
	if result.SourceStatus == nil || result.SourceStatus.Status == "ok" {
		result.SourceStatus = &protocol.SourceStatus{Name: "terraform", Status: "partial", Detail: detail}
	}
}

// Collection directly re-reads previously tracked active IDs. This capability
// intentionally does not infer completion from the disappearance of a list row.
func (Source) Reconcile(context.Context, integration.ReconcileRequest) []integration.Observation {
	return nil
}

func runIdentity(host, id string) string { return "terraform:run:" + host + ":" + id }

func parseIdentity(id string) (string, string, bool) {
	value, ok := strings.CutPrefix(id, "terraform:run:")
	i := strings.LastIndex(value, ":")
	if !ok || i < 1 {
		return "", "", false
	}
	host, err := normalizeHost(value[:i])
	run := value[i+1:]
	return host, run, err == nil && host == value[:i] && validID(run, "run-") && runIdentity(host, run) == id
}

func statusDetail(count int) string { return fmt.Sprintf("%d personal runs", count) }
