package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/terraform/settings"
	"radar/internal/protocol"
)

const (
	// This endpoint allows 30 requests/minute. Stay below it per two-minute
	// refresh, even across workspaces. An incomplete scan is never authoritative.
	listPageBudget = 24
	operations     = "plan_only,plan_and_apply,save_plan,refresh_only,destroy,empty_apply,action_only"
)

func (s Source) collect(ctx context.Context, session session, cfg settings.Config, req integration.CollectRequest) integration.CollectResult {
	result := integration.CollectResult{Complete: true, SourceStatus: &protocol.SourceStatus{Name: "terraform", Status: "ok"}}
	prior := previousRefs(req.Previous)
	fresh := map[string]integration.Observation{}
	seenRuns := map[string]bool{}
	pages := 0
	spaceIDs := make([]string, 0, len(session.spaces))
	for id := range session.spaces {
		spaceIDs = append(spaceIDs, id)
	}
	sort.Strings(spaceIDs)
	// Active work across every workspace comes before historical catch-up.
	for _, group := range []string{"non_final", "final"} {
		for _, spaceID := range spaceIDs {
			path := "/workspaces/" + spaceID + "/runs"
			query := url.Values{"filter[operation]": {operations}, "filter[status_group]": {group}, "page[size]": {"100"}, "include": {"plan,configuration_version.ingress_attributes"}}
			// Workspace listings without a timeframe filter include all years.
			// Scan to an explicit pagination end; ordering is not assumed.
			page := 1
			for {
				if pages >= listPageBudget {
					addFailure(&result, "Terraform run scan reached its request budget; narrow the workspace scope")
					break
				}
				pages++
				query.Set("page[number]", strconv.Itoa(page))
				doc, err := session.client.get(ctx, path+"?"+query.Encode())
				if err != nil {
					addFailure(&result, err.Error())
					break
				}
				var records []resource
				if json.Unmarshal(doc.Data, &records) != nil || records == nil {
					addFailure(&result, "Terraform run list is malformed")
					break
				}
				for _, r := range records {
					if seenRuns[r.ID] {
						continue
					}
					seenRuns[r.ID] = true
					if group == "final" && prior[runIdentity(session.host, r.ID)].ID == "" && !recentRun(r, time.Now()) {
						continue
					}
					observation, owned, err := s.observe(ctx, session, cfg, r, doc.Included, req)
					if err != nil {
						addFailure(&result, err.Error())
						continue
					}
					if !owned {
						continue
					}
					if observation.Ref.Signal == "done" && prior[observation.Ref.ID].ID == "" && !recentRun(r, time.Now()) {
						continue
					}
					fresh[observation.Ref.ID] = observation
				}
				next, err := nextPage(doc, session.host, path, page)
				if err != nil {
					addFailure(&result, err.Error())
					break
				}
				if next == 0 {
					break
				}
				if len(records) == 0 {
					addFailure(&result, "Terraform returned an empty nonfinal page")
					break
				}
				page = next
			}
		}
	}
	// Re-read missing known runs regardless of discovery age or completion of
	// the paginated scan. Confirmed terminal facts are immutable and reusable.
	ids := make([]string, 0, len(prior))
	for id := range prior {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := fresh[id]; ok {
			continue
		}
		ref := prior[id]
		host, runID, valid := parseIdentity(id)
		if !valid || host != session.host || ref.Metadata["creator_id"] != session.userID || session.spaces[ref.Metadata["workspace_id"]] == "" {
			addFailure(&result, "tracked Terraform run does not match the configured account or workspace scope")
			fresh[id] = integration.Observation{Ref: ref, Signal: integration.WorkSignal(ref.Signal), Reason: ref.Status}
			continue
		}
		if ref.Signal == "done" {
			fresh[id] = integration.Observation{Ref: ref, Signal: integration.SignalDone, Reason: ref.Status}
			continue
		}
		observation, err := s.readRun(ctx, session, cfg, runID, req)
		if err != nil {
			addFailure(&result, err.Error())
			observation = integration.Observation{Ref: ref, Signal: integration.WorkSignal(ref.Signal), Reason: ref.Status}
		}
		fresh[id] = observation
	}
	ids = ids[:0]
	for id := range fresh {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		result.Observations = append(result.Observations, fresh[id])
	}
	if result.Complete {
		result.SourceStatus.Detail = statusDetail(len(result.Observations))
	}
	return result
}

func nextPage(doc document, host, path string, current int) (int, error) {
	if raw, ok := doc.Links["next"]; ok {
		if string(raw) == "null" {
			return 0, nil
		}
		var value string
		if json.Unmarshal(raw, &value) != nil || value == "" {
			return 0, fmt.Errorf("invalid Terraform next-page link")
		}
		u, err := url.Parse(value)
		if err != nil || u.User != nil || u.Fragment != "" || u.Path != "/api/v2"+path || (u.Host != "" && (!strings.EqualFold(u.Host, host) || u.Scheme != "https")) {
			return 0, fmt.Errorf("Terraform pagination changed service or endpoint")
		}
		next, err := strconv.Atoi(u.Query().Get("page[number]"))
		if err != nil || next != current+1 {
			return 0, fmt.Errorf("Terraform pagination is incomplete or cyclic")
		}
		return next, nil
	}
	if raw, ok := doc.Meta.Pagination["next-page"]; ok {
		if string(raw) == "null" {
			return 0, nil
		}
		var next int
		if json.Unmarshal(raw, &next) == nil && next == current+1 {
			return next, nil
		}
	}
	return 0, fmt.Errorf("Terraform pagination did not prove the end of the run list")
}

func (s Source) readRun(ctx context.Context, session session, cfg settings.Config, id string, req integration.CollectRequest) (integration.Observation, error) {
	doc, err := session.client.get(ctx, "/runs/"+id+"?include=plan,configuration_version.ingress_attributes")
	if err != nil {
		return integration.Observation{}, err
	}
	run, err := doc.one("runs")
	if err != nil || run.ID != id {
		return integration.Observation{}, fmt.Errorf("Terraform run identity changed")
	}
	observation, owned, err := s.observe(ctx, session, cfg, run, doc.Included, req)
	if err != nil {
		return integration.Observation{}, err
	}
	if !owned {
		return integration.Observation{}, fmt.Errorf("Terraform run is not attributed to the configured personal user")
	}
	return observation, nil
}

func recentRun(run resource, now time.Time) bool {
	var attrs runAttributes
	if json.Unmarshal(run.Attributes, &attrs) != nil {
		return false
	}
	latest, _ := time.Parse(time.RFC3339, attrs.CreatedAt)
	for _, value := range attrs.Timestamps {
		if at, err := time.Parse(time.RFC3339, value); err == nil && at.After(latest) {
			latest = at
		}
	}
	return !latest.IsZero() && !latest.Before(now.Add(-72*time.Hour))
}
