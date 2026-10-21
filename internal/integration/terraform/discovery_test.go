package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func workspaceFixture(id, name string) resource {
	return resource{ID: id, Type: "workspaces", Attributes: raw(map[string]any{"name": name})}
}

func TestWorkspaceDiscoveryPaginatesAndSortsNames(t *testing.T) {
	_, api, cfg := fixtureSource(t)
	c := client{profile: cfg.Profile, output: api.output}
	cfg.Workspaces = nil
	session, err := c.connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	pages := 0
	api.workspaces = func(u *url.URL) (string, error) {
		pages++
		if u.Query().Get("page[size]") != "100" {
			t.Fatal("missing explicit page size", u)
		}
		page, _ := strconv.Atoi(u.Query().Get("page[number]"))
		if page == 1 {
			return encode(map[string]any{
				"data":  []resource{workspaceFixture("ws-z", "zeta")},
				"links": map[string]any{"next": "https://app.terraform.io/api/v2/organizations/acme/workspaces?page%5Bnumber%5D=2"},
			}), nil
		}
		return encode(map[string]any{
			"data": []resource{workspaceFixture("ws-a", "alpha")},
			"meta": map[string]any{"pagination": map[string]any{"next-page": nil}},
		}), nil
	}
	names, err := session.workspaceNames(context.Background(), cfg.Organization)
	if err != nil || pages != 2 || !reflect.DeepEqual(names, []string{"alpha", "zeta"}) {
		t.Fatal("incomplete or unsorted discovery", names, pages, err)
	}
}

func TestWorkspaceDiscoveryRejectsIncompleteOrInvalidLists(t *testing.T) {
	wrongOrg := workspaceFixture("ws-other", "other")
	wrongOrg.Relationships = map[string]json.RawMessage{"organization": rel("elsewhere", "organizations")}
	for _, tc := range []struct {
		name string
		data string
	}{
		{"not a list", `{"data":{}}`},
		{"null list", `{"data":null,"links":{"next":null}}`},
		{"no pagination proof", `{"data":[]}`},
		{"invalid name", encode(map[string]any{"data": []resource{workspaceFixture("ws-one", "../unsafe")}, "links": map[string]any{"next": nil}})},
		{"invalid ID", encode(map[string]any{"data": []resource{workspaceFixture("run-one", "alpha")}, "links": map[string]any{"next": nil}})},
		{"wrong organization", encode(map[string]any{"data": []resource{wrongOrg}, "links": map[string]any{"next": nil}})},
		{"duplicate workspace", encode(map[string]any{"data": []resource{workspaceFixture("ws-one", "alpha"), workspaceFixture("ws-one", "alpha")}, "links": map[string]any{"next": nil}})},
		{"empty nonfinal page", `{"data":[],"links":{"next":"/api/v2/organizations/acme/workspaces?page%5Bnumber%5D=2"}}`},
		{"foreign host", `{"data":[],"links":{"next":"https://other.invalid/api/v2/organizations/acme/workspaces?page%5Bnumber%5D=2"}}`},
		{"foreign endpoint", `{"data":[],"links":{"next":"/api/v2/organizations/elsewhere/workspaces?page%5Bnumber%5D=2"}}`},
		{"cyclic pagination", `{"data":[],"links":{"next":"/api/v2/organizations/acme/workspaces?page%5Bnumber%5D=1"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, api, _ := fixtureSource(t)
			api.workspaces = func(*url.URL) (string, error) { return tc.data, nil }
			s := session{client: client{profile: "default", output: api.output}, host: "app.terraform.io"}
			if names, err := s.workspaceNames(context.Background(), "acme"); err == nil || names != nil {
				t.Fatal("invalid discovery accepted", names, err)
			}
		})
	}
}

func TestWorkspaceDiscoveryDoesNotReturnPartialListOrPrivateDiagnostics(t *testing.T) {
	_, api, _ := fixtureSource(t)
	api.workspaces = func(u *url.URL) (string, error) {
		if u.Query().Get("page[number]") == "1" {
			return encode(map[string]any{
				"data":  []resource{workspaceFixture("ws-one", "alpha")},
				"links": map[string]any{"next": "/api/v2/organizations/acme/workspaces?page%5Bnumber%5D=2"},
			}), nil
		}
		return "private diagnostic", fmt.Errorf("private diagnostic")
	}
	s := session{client: client{profile: "default", output: api.output}, host: "app.terraform.io"}
	names, err := s.workspaceNames(context.Background(), "acme")
	if err == nil || names != nil || strings.Contains(err.Error(), "private") {
		t.Fatal("partial discovery or private error escaped", names, err)
	}
}
