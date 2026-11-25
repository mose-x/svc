package detach

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// spawnPackageRoots are the trees the no-flash rule covers. Every spawn that
// reaches a user's screen from here must go through helpers.CreateCmd /
// CreateCmdContext, which is the only place CREATE_NO_WINDOW is set.
var spawnPackageRoots = []string{".", "../cli"}

// forbiddenSpawnCalls are the constructors that bypass helpers.CreateCmd. They
// compile, they pass review, and on Windows they flash a console window in
// front of the user -- which is why the check is mechanical rather than a
// convention.
var forbiddenSpawnCalls = map[string]bool{"Command": true, "CommandContext": true}

// TestNoBareExecCommandInSpawnPackages is the static half of the no-flash rule.
// It parses the AST instead of grepping, so a comment or a doc string that
// mentions exec.Command (including this package's own) does not trip it, and a
// renamed import or a wrapped call cannot hide from it either.
func TestNoBareExecCommandInSpawnPackages(t *testing.T) {
	scanned := 0
	for _, root := range spawnPackageRoots {
		info, err := filepath.Abs(root)
		if err != nil {
			t.Fatalf("abs %s: %v", root, err)
		}
		err = filepath.WalkDir(info, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			scanned++
			checkFile(t, path)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	// A walk that silently matched nothing would pass forever; the two roots
	// hold this file and the i18n package at minimum.
	if scanned < 3 {
		t.Errorf("only scanned %d Go files under %v; the walk is broken", scanned, spawnPackageRoots)
	}
}

func checkFile(t *testing.T, path string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Errorf("parse %s: %v", path, err)
		return
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "exec" || !forbiddenSpawnCalls[sel.Sel.Name] {
			return true
		}
		pos := fset.Position(call.Pos())
		t.Errorf("%s:%d: bare exec.%s; use helpers.CreateCmd / helpers.CreateCmdContext so the child never flashes a console window on Windows",
			pos.Filename, pos.Line, sel.Sel.Name)
		return true
	})
}
