package contracttest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestCoreDoesNotImportConcreteIntegrations(t *testing.T) {
	root := repositoryRoot(t)
	concrete := []string{
		"radar/internal/integration/datadog",
		"radar/internal/integration/git",
		"radar/internal/integration/github",
		"radar/internal/integration/jira",
		"radar/internal/integration/obsidian",
		"radar/internal/integration/sbx",
		"radar/internal/integration/tmux",
		"radar/internal/integration/workspace",
	}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			relative, _ := filepath.Rel(root, path)
			if strings.HasPrefix(filepath.ToSlash(relative), "internal/integration/") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		directory := filepath.ToSlash(filepath.Dir(relative))
		if directory == "internal/app" || directory == "internal/config" {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range parsed.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			for _, prefix := range concrete {
				if value == prefix || strings.HasPrefix(value, prefix+"/") {
					t.Errorf("%s imports concrete integration %q; use an internal/integration capability", relative, value)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProviderCommandsStayInsideIntegrationPackages(t *testing.T) {
	root := repositoryRoot(t)
	commands := map[string]bool{"gh": true, "git": true, "tmux": true, "sbx": true}
	for _, directory := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				relative, _ := filepath.Rel(root, path)
				if strings.HasPrefix(filepath.ToSlash(relative), "internal/integration/") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			relative, _ := filepath.Rel(root, path)
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(parsed, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (selector.Sel.Name != "Run" && selector.Sel.Name != "LookPath" && selector.Sel.Name != "Command" && selector.Sel.Name != "CommandContext") {
					return true
				}
				for _, argument := range call.Args {
					literal, ok := argument.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					value, _ := strconv.Unquote(literal.Value)
					if commands[value] {
						t.Errorf("%s invokes provider command %q outside internal/integration", relative, value)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	extension, err := os.ReadFile(filepath.Join(root, "internal", "pi", "extension", "radar.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"tmux", "sbx", "git", "gh"} {
		if strings.Contains(string(extension), "pi.exec(\""+command+"\"") {
			t.Errorf("Pi extension invokes provider command %q directly", command)
		}
	}
}

func TestLegacyProviderPackagesAreAbsent(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{
		"internal/filters", "internal/githubidentity", "internal/sbxauth",
		"internal/tmuxlayout", "internal/workspace", "internal/workspacegroup",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Errorf("legacy provider package %s still exists", path)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve boundary test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", ".."))
}
