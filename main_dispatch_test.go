package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"slices"
	"testing"
)

// main.go is the compatibility red line. `svc` with no arguments must keep
// launching the window, and the four spellings that predate the CLI must keep
// their original code paths and their original output -- `svc version` in
// particular is parsed by scripts. internal/cli/dispatch_test.go pins the
// decision table itself; the guards here pin that main.go actually obeys it,
// because a Kind the switch forgets falls through to the default and silently
// opens a window instead.
//
// They read main.go as an AST rather than as text so ordinary reformatting
// does not break them, and so the assertions are about structure: which
// function is called, with which argument, under which case.

func parseMain(t *testing.T) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	return fset, file
}

func funcDecl(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("main.go has no function %s", name)
	return nil
}

// render prints an expression back to source, so an assertion can compare
// shapes instead of walking them by hand.
func render(t *testing.T, fset *token.FileSet, node ast.Node) string {
	t.Helper()
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, node); err != nil {
		t.Fatalf("render %T: %v", node, err)
	}
	return buf.String()
}

// calleeName renders a call's function as "pkg.Func", or as "Func" for a call
// into this package's own functions. Anything else -- a method value, a
// func literal -- yields "", which simply never matches.
func calleeName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if pkg, ok := fun.X.(*ast.Ident); ok {
			return pkg.Name + "." + fun.Sel.Name
		}
	case *ast.Ident:
		return fun.Name
	}
	return ""
}

// callsIn collects every call expression below node.
func callsIn(node ast.Node) []*ast.CallExpr {
	var found []*ast.CallExpr
	ast.Inspect(node, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			found = append(found, call)
		}
		return true
	})
	return found
}

// callNamed returns the first call to the named function below node.
func callNamed(node ast.Node, name string) *ast.CallExpr {
	for _, call := range callsIn(node) {
		if calleeName(call) == name {
			return call
		}
	}
	return nil
}

func TestMainDecidesThroughTheCliPackage(t *testing.T) {
	fset, file := parseMain(t)
	main := funcDecl(t, file, "main")

	dispatch := callNamed(main, "cli.Dispatch")
	if dispatch == nil {
		t.Fatal("main does not call cli.Dispatch; the compatibility table in internal/cli is no longer the thing deciding argv")
	}
	// argv minus the program name: Dispatch keys on the first element, so
	// passing os.Args whole would make it read "svc" as the command.
	if len(dispatch.Args) != 1 {
		t.Fatalf("cli.Dispatch got %d arguments, want one", len(dispatch.Args))
	}
	if got := render(t, fset, dispatch.Args[0]); got != "os.Args[1:]" {
		t.Errorf("cli.Dispatch(%s), want cli.Dispatch(os.Args[1:])", got)
	}
	// The GUI launch moved into runGUI when the switch replaced the fallthrough,
	// and it must stay there: a wails.Run reachable from main outside that
	// function is a window opening on a code path nobody asked for.
	if callNamed(main, "wails.Run") != nil {
		t.Error("main calls wails.Run directly; only runGUI may launch the window")
	}
}

func TestMainSwitchCoversEveryDispatchKind(t *testing.T) {
	fset, file := parseMain(t)
	main := funcDecl(t, file, "main")

	var dispatchSwitch *ast.SwitchStmt
	ast.Inspect(main, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		if call, ok := sw.Tag.(*ast.CallExpr); ok && calleeName(call) == "cli.Dispatch" {
			dispatchSwitch = sw
			return false
		}
		return true
	})
	if dispatchSwitch == nil {
		t.Fatal("main has no switch over cli.Dispatch")
	}

	var cases []string
	var defaultBody []ast.Stmt
	for _, clause := range dispatchSwitch.Body.List {
		cc := clause.(*ast.CaseClause)
		if cc.List == nil {
			defaultBody = cc.Body
			continue
		}
		for _, expr := range cc.List {
			cases = append(cases, render(t, fset, expr))
		}
	}

	// Every Kind internal/cli can return except KindGUI, which is the default.
	// A missing case means that Kind falls through and opens the window.
	want := []string{"cli.KindCommand", "cli.KindLegacyHelp", "cli.KindLegacyInit", "cli.KindLegacyVersion"}
	slices.Sort(cases)
	if !slices.Equal(cases, want) {
		t.Errorf("main switches on %v, want exactly %v", cases, want)
	}
	if slices.Contains(cases, "cli.KindGUI") {
		t.Error("cli.KindGUI must be the default clause, not a case")
	}
	if defaultBody == nil {
		t.Fatal("the dispatch switch has no default clause; the GUI would never launch")
	}
	if callNamed(&ast.BlockStmt{List: defaultBody}, "runGUI") == nil {
		t.Error("the default clause does not call runGUI")
	}
}

func TestRunGUIStillLaunchesWails(t *testing.T) {
	_, file := parseMain(t)
	if callNamed(funcDecl(t, file, "runGUI"), "wails.Run") == nil {
		t.Error("runGUI no longer calls wails.Run; the window would not open")
	}
}

// TestTheCommandTreeGetsTheRealCatalogs pins the two embedded values Execute
// cannot work without. cli.Execute fails loudly when Catalogs is empty, but a
// missing AboutJSON degrades quietly into a version line that says nothing, so
// both are asserted here rather than left to review.
func TestTheCommandTreeGetsTheRealCatalogs(t *testing.T) {
	fset, file := parseMain(t)
	main := funcDecl(t, file, "main")

	execute := callNamed(main, "cli.Execute")
	if execute == nil {
		t.Fatal("main does not call cli.Execute; the command tree is unreachable")
	}
	if len(execute.Args) != 1 {
		t.Fatalf("cli.Execute got %d arguments, want the single ExecuteInput", len(execute.Args))
	}
	literal, ok := execute.Args[0].(*ast.CompositeLit)
	if !ok {
		t.Fatalf("cli.Execute is passed %T, want an ExecuteInput literal", execute.Args[0])
	}

	fields := map[string]string{}
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := pair.Key.(*ast.Ident)
		if !ok {
			continue
		}
		fields[key.Name] = render(t, fset, pair.Value)
	}

	for field, want := range map[string]string{
		"Args":      "os.Args[1:]",
		"Catalogs":  "cliCatalogs()",
		"AboutJSON": "aboutJSON",
		"Stdin":     "os.Stdin",
		"Stdout":    "os.Stdout",
		"Stderr":    "os.Stderr",
	} {
		if got := fields[field]; got != want {
			t.Errorf("ExecuteInput.%s = %q, want %q", field, got, want)
		}
	}
}

// TestRunInitCLIReadsSettingsFromTheHomeDir pins a bug fix. settings.json lives
// at the fixed <home>/.svc location regardless of any custom install path --
// the shim runtime reads it from there to discover that path. Building the
// SettingsManager over cfg.SvcDir() made it look in ~/.svc/.svc/ for a default
// install and silently ignore every custom one, so `svc init` set the shims up
// in the wrong place for exactly the users who had moved their install.
func TestRunInitCLIReadsSettingsFromTheHomeDir(t *testing.T) {
	fset, file := parseMain(t)
	fn := funcDecl(t, file, "runInitCLI")

	call := callNamed(fn, "config.NewSettingsManager")
	if call == nil {
		t.Fatal("runInitCLI no longer builds a SettingsManager")
	}
	if len(call.Args) != 1 {
		t.Fatalf("config.NewSettingsManager got %d arguments, want one", len(call.Args))
	}
	if got := render(t, fset, call.Args[0]); got != "cfg.HomeDir()" {
		t.Errorf("config.NewSettingsManager(%s); settings.json is read from the home dir, not the install path", got)
	}
}
