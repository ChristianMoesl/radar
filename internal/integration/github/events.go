package github

import (
	"encoding/json"
	"sort"
	"strings"

	"radar/internal/integration"
	"radar/internal/integration/github/identity"
)

// CollectionItems exports API facts before task filtering, linking or muting.
// In particular, disappearing from an open search is never a closure event.
func (Source) CollectionItems(observations []integration.Observation) []integration.CollectionItem {
	items := map[string]integration.CollectionItem{}
	for _, observation := range observations {
		ref := observation.Ref
		repo, number, ok := parsePullRequestSourceRefID(ref.ID)
		state := ref.Metadata["pr_state"]
		if !ok || ref.Metadata["head_oid"] == "" || ref.Metadata["pr_node_id"] == "" || (state != "OPEN" && state != "CLOSED" && state != "MERGED") {
			continue
		}
		key := identity.PullRequestKey(strings.ToLower(repo), number)
		// Fresh discovery wins over retained done facts.
		if _, exists := items[key]; exists {
			continue
		}
		payload, _ := json.Marshal(map[string]any{
			"type": "github.pull_request", "nodeId": ref.Metadata["pr_node_id"],
			"repository": strings.ToLower(repo), "number": number, "url": ref.URL,
			"title": ref.Title, "author": ref.Metadata["author"], "authorType": ref.Metadata["author_type"],
			"state": state, "draft": ref.Metadata["pr_draft"] == "true", "headRef": ref.Branch, "headOid": ref.Metadata["head_oid"],
		})
		items[key] = integration.CollectionItem{Key: key, Payload: payload}
	}
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]integration.CollectionItem, 0, len(keys))
	for _, key := range keys {
		result = append(result, items[key])
	}
	return result
}
