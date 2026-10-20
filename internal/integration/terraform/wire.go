package terraform

import (
	"encoding/json"
	"fmt"
	"strings"

	"radar/internal/integration/terraform/settings"
)

type linkage struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type resource struct {
	ID            string                     `json:"id"`
	Type          string                     `json:"type"`
	Attributes    json.RawMessage            `json:"attributes"`
	Relationships map[string]json.RawMessage `json:"relationships"`
}

func (r resource) related(name string) linkage {
	var relation struct {
		Data linkage `json:"data"`
	}
	_ = json.Unmarshal(r.Relationships[name], &relation)
	return relation.Data
}

type document struct {
	Data     json.RawMessage            `json:"data"`
	Included []resource                 `json:"included"`
	Errors   []json.RawMessage          `json:"errors"`
	Links    map[string]json.RawMessage `json:"links"`
	Meta     struct {
		Pagination map[string]json.RawMessage `json:"pagination"`
	} `json:"meta"`
}

func (d document) one(kind string) (resource, error) {
	var r resource
	if json.Unmarshal(d.Data, &r) != nil || r.Type != kind || r.ID == "" || len(r.Attributes) == 0 || string(r.Attributes) == "null" {
		return resource{}, fmt.Errorf("invalid Terraform %s response", kind)
	}
	return r, nil
}

func validID(id, prefix string) bool {
	return strings.HasPrefix(id, prefix) && len(id) > len(prefix) && settings.ValidSelector(id)
}

type runAttributes struct {
	Status     string            `json:"status"`
	Message    string            `json:"message"`
	CreatedAt  string            `json:"created-at"`
	PlanOnly   *bool             `json:"plan-only"`
	AutoApply  *bool             `json:"auto-apply"`
	Timestamps map[string]string `json:"status-timestamps"`
	Actions    struct {
		Confirmable bool `json:"is-confirmable"`
	} `json:"actions"`
}

type planAttributes struct {
	Additions    *int `json:"resource-additions"`
	Changes      *int `json:"resource-changes"`
	Destructions *int `json:"resource-destructions"`
}

type ingressAttributes struct {
	Branch        string `json:"branch"`
	Repository    string `json:"identifier"`
	CloneURL      string `json:"clone-url"`
	PRURL         string `json:"pull-request-url"`
	PRNumber      int    `json:"pull-request-number"`
	IsPR          bool   `json:"is-pull-request"`
	DefaultBranch *bool  `json:"on-default-branch"`
}
