package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"

	"radar/internal/integration/terraform/settings"
)

func (s session) workspaceNames(ctx context.Context, organization string) ([]string, error) {
	path := "/organizations/" + url.PathEscape(organization) + "/workspaces"
	query := url.Values{"page[size]": {"100"}}
	names := []string{}
	seenNames, seenIDs := map[string]bool{}, map[string]bool{}
	for page := 1; ; {
		query.Set("page[number]", strconv.Itoa(page))
		doc, err := s.client.get(ctx, path+"?"+query.Encode())
		if err != nil {
			return nil, err
		}
		var records []resource
		if json.Unmarshal(doc.Data, &records) != nil || records == nil {
			return nil, fmt.Errorf("Terraform workspace list is malformed")
		}
		for _, space := range records {
			var attrs struct {
				Name string `json:"name"`
			}
			if space.Type != "workspaces" || !validID(space.ID, "ws-") || json.Unmarshal(space.Attributes, &attrs) != nil || !settings.ValidSelector(attrs.Name) || seenNames[attrs.Name] || seenIDs[space.ID] {
				return nil, fmt.Errorf("Terraform workspace list contains an invalid or duplicate workspace")
			}
			if org := space.related("organization"); org.ID != "" && org.ID != organization {
				return nil, fmt.Errorf("Terraform workspace organization does not match")
			}
			seenNames[attrs.Name], seenIDs[space.ID] = true, true
			names = append(names, attrs.Name)
		}
		next, err := nextPage(doc, s.host, path, page)
		if err != nil {
			return nil, err
		}
		if next == 0 {
			sort.Strings(names)
			return names, nil
		}
		if len(records) == 0 {
			return nil, fmt.Errorf("Terraform returned an empty nonfinal workspace page")
		}
		page = next
	}
}
