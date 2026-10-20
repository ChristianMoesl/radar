package settings

import "testing"

func TestSettingsRejectUnsafeAndDuplicateScope(t *testing.T) {
	for _, cfg := range []Config{
		{Profile: "../other"},
		{Profile: "default", Organization: "acme/other"},
		{Profile: "default", Workspaces: []string{"one", "one"}},
		{Profile: "default", Workspaces: []string{" bad"}},
	} {
		if Validate(cfg) == nil {
			t.Fatal("unsafe scope accepted", cfg)
		}
	}
	if err := Validate(Default()); err != nil {
		t.Fatal(err)
	}
}
