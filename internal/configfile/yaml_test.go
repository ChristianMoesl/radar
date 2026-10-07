package configfile

import (
	"strings"
	"testing"
)

func TestParseRequiresOneValidMapping(t *testing.T) {
	for _, data := range []string{"", "# only a comment\n", "null", "[]", "hello", "key: [", "key: a\nkey: b\n", "future:\n  key: a\n  key: b\n", "a: b\n---\nc: d\n", "a: &loop {next: *loop}"} {
		t.Run(data, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Fatal("invalid document accepted")
			}
		})
	}
	if _, err := Parse([]byte("# configuration\na: b # explanation\n")); err != nil {
		t.Fatal(err)
	}
}

func TestPatchPreservesPresentationAndUnknownFields(t *testing.T) {
	original := "# my configuration\nworkspace:\n  # my location\n  root_dir: '/old' # fast disk\n  future: 9007199254740993\nrepository_dirs:\n  - ~/work # first checkout\n# keep this last\nother: |\n  exact text\n"
	doc, err := Parse([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := Node(map[string]any{"workspace": map[string]any{"root_dir": "/old"}, "repository_dirs": []string{"~/work"}})
	after, _ := Node(map[string]any{"workspace": map[string]any{"root_dir": "/new"}, "repository_dirs": []string{"~/code", "~/extra"}})
	if !Patch(doc, before, after) {
		t.Fatal("patch was not applied")
	}
	data, err := Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# my configuration", "# my location", "root_dir: '/new' # fast disk", "future: 9007199254740993", "- ~/code # first checkout", "- ~/extra", "# keep this last", "other: |\n  exact text"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q:\n%s", want, data)
		}
	}
	if strings.Index(string(data), "workspace:") > strings.Index(string(data), "repository_dirs:") {
		t.Fatal("key order changed")
	}
	if Patch(doc, after, after) {
		t.Fatal("no-op changed document")
	}
}

func TestPatchDoesNotChangeOtherAnchorConsumers(t *testing.T) {
	for _, fixture := range []struct {
		name, data    string
		before, after map[string]any
		wantOther     string
	}{
		{"scalar anchor", "model: &model old\nother: *model\n", map[string]any{"model": "old"}, map[string]any{"model": "new"}, "old"},
		{"alias", "other: &model old\nmodel: *model # chosen model\n", map[string]any{"model": "old"}, map[string]any{"model": "new"}, "old"},
		{"merged mapping", "defaults: &defaults\n  workspace: {root_dir: /old, future: keep}\n<<: *defaults\n", map[string]any{"workspace": map[string]any{"root_dir": "/old"}}, map[string]any{"workspace": map[string]any{"root_dir": "/new"}}, ""},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			doc, err := Parse([]byte(fixture.data))
			if err != nil {
				t.Fatal(err)
			}
			before, _ := Node(fixture.before)
			after, _ := Node(fixture.after)
			Patch(doc, before, after)
			data, err := Encode(doc)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := Decode(data, &result); err != nil {
				t.Fatalf("invalid patched document: %v\n%s", err, data)
			}
			if fixture.wantOther != "" {
				if result["other"] != fixture.wantOther || result["model"] != "new" {
					t.Fatal(string(data))
				}
			} else {
				workspace := result["workspace"].(map[string]any)
				defaults := result["defaults"].(map[string]any)["workspace"].(map[string]any)
				if workspace["root_dir"] != "/new" || workspace["future"] != "keep" || defaults["root_dir"] != "/old" {
					t.Fatal(string(data))
				}
			}
		})
	}
}

func TestAnnotateDoesNotReplaceUserComments(t *testing.T) {
	doc, err := Parse([]byte("# user's explanation\nkey: value\nother: value # inline explanation\nnew: value\n"))
	if err != nil {
		t.Fatal(err)
	}
	Annotate(doc, map[string]string{"key": "generated", "other": "generated", "new": "new guidance"})
	data, err := Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "generated") || strings.Count(string(data), "# new guidance") != 1 {
		t.Fatal(string(data))
	}
}

func TestPatchCanClearAnInheritedOptionalSetting(t *testing.T) {
	for _, override := range []string{"", "api_base_url: https://override.test\n"} {
		doc, err := Parse([]byte("defaults: &defaults {api_base_url: https://inherited.test}\n<<: *defaults\n" + override))
		if err != nil {
			t.Fatal(err)
		}
		before, _ := Node(map[string]string{"api_base_url": "https://inherited.test"})
		after, _ := Node(map[string]string{})
		Patch(doc, before, after)
		data, err := Encode(doc)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			APIBaseURL string `yaml:"api_base_url"`
		}
		if err := Decode(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.APIBaseURL != "" {
			t.Fatal("cleared setting inherited its old value")
		}
	}
}
