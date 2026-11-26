package cli

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"svc/internal/appcore"
)

// `svc import` is thin: pick a source, ask, take the lock, call the service the
// GUI calls, report the sentence the GUI reports. Its tests are split by how
// much of the machine they need.
//
//   - the argument rules, the wording choice and the no-picker guard are pure
//     and run on all three CI platforms;
//   - everything that reaches the service bootstraps at LevelWrite, which on
//     Windows merges the shims directory into HKCU\Environment, so those skip
//     there for the reason skipOnWindowsRegistryWrites gives;
//   - a SUCCESSFUL import additionally has to run the SDK's own verify binary,
//     because that is how the version is detected, and criticalFilesFor(Golang)
//     asks for go.exe and gofmt.exe on Windows. No script renamed to .exe is a
//     PE image, so the happy path is Unix-only and says so.
//
// What the happy path cannot cover on Windows is the importer's own integrity
// layer, which internal/importer tests directly; the command's logic -- source
// selection, the prompt, the lock, the report -- is the part those platforms
// still share.

// fakeGoVersion is the version the fake Go in these tests claims. It comes out
// of the binary's own `go version` output rather than out of an argument, which
// is the whole reason an import can be trusted to name the directory it made.
const fakeGoVersion = "1.22.0"

// skipImportHappyPathOnWindows names why the successful-import tests are
// Unix-only. Both halves are needed: LevelWrite touches the real registry, and
// the verify step needs a runnable go.exe.
func skipImportHappyPathOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a successful import both bootstraps at LevelWrite (which edits HKCU\\Environment) and runs the SDK's verify binary, and criticalFilesFor(Golang) demands real go.exe/gofmt.exe images on Windows")
	}
}

// goBinaryName is the file name the importer looks for when it wants to run
// Go's verify command. FindExecutable appends the platform extensions itself,
// so the file on disk has to carry the one Windows requires.
func goBinaryName() string {
	if runtime.GOOS == "windows" {
		return "go.exe"
	}
	return "go"
}

// seedFlatGoSdk lays down the FLAT directory an import expects: bin/ directly
// under the root. pathmgr.AlignImportLayout wraps it into go/bin once it has
// been copied, and pathmgr.DetectSdkRoot requires the flat shape to recognise
// the directory as a Go SDK at all -- seeding the post-alignment go/bin layout
// here would make DetectSdkRoot return "" and the import would fail before it
// reached anything worth testing.
//
// script is the body of the `go` file. A runnable one makes the version
// detection succeed; a stub makes it fail, which is a different test.
func seedFlatGoSdk(t *testing.T, dir, script string) {
	t.Helper()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", binDir, err)
	}
	if err := os.WriteFile(filepath.Join(binDir, goBinaryName()), []byte(script), 0755); err != nil {
		t.Fatalf("write the fake go: %v", err)
	}
	// gofmt is never executed -- it only has to exist for the critical-files
	// check, which is what makes the pair complete.
	gofmt := "gofmt"
	if runtime.GOOS == "windows" {
		gofmt = "gofmt.exe"
	}
	if err := os.WriteFile(filepath.Join(binDir, gofmt), []byte("stub\n"), 0755); err != nil {
		t.Fatalf("write the fake gofmt: %v", err)
	}
}

// seedStubGoSdk is seedFlatGoSdk with a `go` that is a file and not a program.
// Running it fails, so the version detection fails, so the import stops at the
// first of its three integrity layers.
func seedStubGoSdk(t *testing.T, dir string) {
	t.Helper()
	seedFlatGoSdk(t, dir, "not an executable\n")
}

// seedRunnableGoSdk is seedFlatGoSdk with a `go` that answers `go version` the
// way the real one does, which is what a successful import needs.
func seedRunnableGoSdk(t *testing.T, dir, version string) {
	t.Helper()
	seedFlatGoSdk(t, dir,
		"#!/bin/sh\necho \"go version go"+version+" "+runtime.GOOS+"/"+runtime.GOARCH+"\"\n")
}

// --- the argument rules -----------------------------------------------------

// TestImportArgumentRules pins the one thing that makes `import` different
// from every other mutating command: it has two mutually exclusive sources, so
// the arity is a range and the choice is checked in RunE. Both mistakes are
// usage failures, because nothing was attempted.
func TestImportArgumentRules(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no arguments at all", []string{"import"}, "accepts between 1 and 2 arg(s), received 0"},
		{"an SDK and no source", []string{"import", goSdk}, errImportNoSource.Error()},
		{"three arguments", []string{"import", goSdk, "a", "b"}, "accepts between 1 and 2 arg(s), received 3"},
		{"a path and --from-path", []string{"import", goSdk, "somewhere", "--from-path"}, errImportTwoSources.Error()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, s := pipedRun(t, nil, c.args...)
			if code != ExitUsage {
				t.Fatalf("svc %s = exit %d, want %d; stderr:\n%s",
					strings.Join(c.args, " "), code, ExitUsage, s.stderr)
			}
			if !strings.Contains(s.stderr.String(), c.want) {
				t.Errorf("stderr = %q, want it to name %q", s.stderr, c.want)
			}
			// A usage failure is not a result, and the usage text belongs on the
			// diagnostic stream with the message.
			if s.stdout.Len() != 0 {
				t.Errorf("a usage failure wrote to the result stream:\n%s", s.stdout)
			}
			if !strings.Contains(s.stderr.String(), "svc import <sdk> [<path>]") {
				t.Errorf("the usage printed is not the import command's own:\n%s", s.stderr)
			}
		})
	}
}

// TestImportWordingComesFromTheGuiButtonItPorts pins the two sets of sentences
// to the two GUI handlers they port. A CLI that grew its own wording for
// "Import Go into SVC?" would pass every other test in this file and still
// disagree with the window.
func TestImportWordingComesFromTheGuiButtonItPorts(t *testing.T) {
	const source = "/downloads/go1.22.0"
	cases := []struct {
		name         string
		fromPath     bool
		wantQuestion string
		wantBody     string
		wantSuccess  string
	}{
		{
			name:         "an explicit path is PathModal's handleImport",
			fromPath:     false,
			wantQuestion: catalogWord(t, "zh", "path.importConfirm", map[string]string{"sdk": goDisplayName()}),
			wantBody:     catalogWord(t, "zh", "path.importConfirmDesc", map[string]string{"path": source}),
			wantSuccess:  catalogWord(t, "zh", "path.importSuccess", nil),
		},
		{
			name:         "--from-path is DetailPanel's handleImportPath",
			fromPath:     true,
			wantQuestion: catalogWord(t, "zh", "sidebar.importConfirm", map[string]string{"sdk": goDisplayName()}),
			wantBody:     catalogWord(t, "zh", "sidebar.importConfirmDesc", nil),
			wantSuccess:  catalogWord(t, "zh", "sidebar.importSuccess", map[string]string{"sdk": goDisplayName()}),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStreams()
			r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
			if err := r.resolveOptions(); err != nil {
				t.Fatalf("resolveOptions: %v", err)
			}
			question, explanation, success := r.importWording(goDisplayName(), source, c.fromPath)
			if question != c.wantQuestion {
				t.Errorf("question = %q, want %q", question, c.wantQuestion)
			}
			if explanation != c.wantBody {
				t.Errorf("explanation = %q, want %q", explanation, c.wantBody)
			}
			if success != c.wantSuccess {
				t.Errorf("success = %q, want %q", success, c.wantSuccess)
			}
		})
	}
}

// TestImportWordingQuotesThePathItIsAboutToCopy is the reason the two wordings
// cannot be collapsed into one: only the path flavour can tell the user which
// directory is going to be copied.
func TestImportWordingQuotesThePathItIsAboutToCopy(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}
	const source = "/downloads/somewhere with spaces"
	_, explanation, _ := r.importWording(goDisplayName(), source, false)
	if !strings.Contains(explanation, source) {
		t.Errorf("the explanation %q does not name the source %q", explanation, source)
	}
}

// TestNoCliCommandReachesForAFileDialog is the source-level half of G5. The
// runtime half -- clirt refusing both pickers -- is pinned in clirt's own tests;
// this one catches the mistake that would never reach it, a command that calls
// SelectLocalFile directly instead of taking the path as an argument.
//
// The scan is over the syntax tree rather than over the text, because the picker
// names also appear in prose: cmd_import.go explains why it takes a path
// argument instead of opening one. A guard that failed on its own explanation
// is a guard nobody can keep green, and a green guard that nobody reads is
// worth nothing.
func TestNoCliCommandReachesForAFileDialog(t *testing.T) {
	forbidden := map[string]bool{
		"SelectLocalFile":     true,
		"SelectLocalDir":      true,
		"OpenFileDialog":      true,
		"OpenDirectoryDialog": true,
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, name := range sourceFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !forbidden[selector.Sel.Name] {
				return true
			}
			t.Errorf("%s:%d calls %s: a terminal has no picker, and reaching for one from a headless command is the popup the CLI contract forbids",
				name, fset.Position(call.Pos()).Line, selector.Sel.Name)
			return true
		})
	}
	// A scan that saw nothing would pass forever.
	if scanned == 0 {
		t.Fatal("no sources were scanned; the guard has rotted")
	}
}

// --- the paths that reach the service ---------------------------------------

func TestImportRefusesAMissingPathBeforeAsking(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	home := t.TempDir()
	missing := filepath.Join(home, "downloads", "go1.22.0.tar.gz")

	// At a terminal and without --yes, the refusal has to arrive INSTEAD of the
	// question. Asking a user to approve an import of a path that is not there,
	// and only then failing, is the failure this ordering prevents.
	code, s := interactiveRunHome(t, home, "y\n", "import", goSdk, missing)
	if code != ExitNotFound {
		t.Fatalf("svc import of a missing path = exit %d, want %d; stderr:\n%s", code, ExitNotFound, s.stderr)
	}
	if want := catalogWord(t, "zh", "errors.path-not-exist", map[string]string{"path": missing}); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
	question := catalogWord(t, "zh", "path.importConfirm", map[string]string{"sdk": goDisplayName()})
	if strings.Contains(s.stderr.String(), question) {
		t.Error("the command asked about an import it had already decided to refuse")
	}

	// --yes takes the same route and reports the same thing.
	if code, s := runHome(t, home, "import", goSdk, missing, "--yes"); code != ExitNotFound {
		t.Errorf("svc import --yes of a missing path = exit %d, want %d; stderr:\n%s", code, ExitNotFound, s.stderr)
	}
}

func TestImportRefusesAnUnknownSdk(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	home := t.TempDir()
	code, s := runHome(t, home, "import", "golang", t.TempDir(), "--yes")
	if code != ExitNotFound {
		t.Fatalf("svc import golang = exit %d, want %d; stderr:\n%s", code, ExitNotFound, s.stderr)
	}
	if want := catalogWord(t, "zh", "errors.unknown-sdk-type", map[string]string{"sdk": "golang"}); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
}

// TestImportReportsARefusedSourceFaithfully is the error path that needs no
// runnable binary: the SDK directory is there, its `go` is not a program, so
// the import stops at the first integrity layer. What matters is that the
// service's own refusal reaches the user untranslated-by-nobody and that a
// refused import leaves no half-written version behind.
func TestImportReportsARefusedSourceFaithfully(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	home := t.TempDir()
	source := t.TempDir()
	seedStubGoSdk(t, source)

	code, s := runHome(t, home, "import", goSdk, source, "--yes")
	if code == ExitOK {
		t.Fatalf("an import of a stub SDK succeeded; stderr:\n%s", s.stderr)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("a failed import wrote to the result stream:\n%s", s.stdout)
	}
	if strings.TrimSpace(s.stderr.String()) == "" {
		t.Error("a failed import said nothing")
	}
	entries, err := os.ReadDir(filepath.Join(home, ".svc", goSdk))
	if err != nil {
		t.Fatalf("read the SDK directory: %v", err)
	}
	for _, entry := range entries {
		// copyToTargetAtomically builds its candidate in a .new sibling and
		// removes it on failure; anything left here is a leak.
		if strings.HasPrefix(entry.Name(), fakeGoVersion) {
			t.Errorf("a refused import left %s behind", entry.Name())
		}
	}
}

func TestImportFromPathAsksTheSidebarQuestion(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	home := t.TempDir()

	// Declining keeps this deterministic: what is being pinned is which source
	// the flag selected, and the question is chosen before the service is
	// called. Accepting it would make the outcome depend on whether the machine
	// running the test happens to have a `go` on its login-shell PATH.
	code, s := interactiveRunHome(t, home, "n\n", "import", goSdk, "--from-path")
	if code != ExitCanceled {
		t.Fatalf("a declined import = exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
	}
	want := catalogWord(t, "zh", "sidebar.importConfirm", map[string]string{"sdk": goDisplayName()})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want it to ask %q", s.stderr, want)
	}
	notWant := catalogWord(t, "zh", "path.importConfirmDesc", map[string]string{"path": ""})
	if strings.Contains(s.stderr.String(), notWant) {
		t.Errorf("--from-path explained itself with the path flavour's wording:\n%s", s.stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".svc", goSdk, fakeGoVersion)); !os.IsNotExist(err) {
		t.Error("a declined import created a version directory")
	}
}

func TestImportDeclinedChangesNothing(t *testing.T) {
	skipImportHappyPathOnWindows(t)
	home := t.TempDir()
	source := t.TempDir()
	seedRunnableGoSdk(t, source, fakeGoVersion)

	code, s := interactiveRunHome(t, home, "n\n", "import", goSdk, source)
	if code != ExitCanceled {
		t.Fatalf("a declined import = exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("a declined import wrote to the result stream:\n%s", s.stdout)
	}
	if _, err := os.Stat(filepath.Join(home, ".svc", goSdk, fakeGoVersion)); !os.IsNotExist(err) {
		t.Error("a declined import created a version directory")
	}
}

// TestImportWithoutYesOffAPipeRefuses is the script-safety half: an import in a
// pipeline with nobody at the keyboard must not guess an answer.
func TestImportWithoutYesOffAPipeRefuses(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	home := t.TempDir()
	source := t.TempDir()
	seedStubGoSdk(t, source)

	code, s := runHome(t, home, "import", goSdk, source)
	if code != ExitUsage {
		t.Fatalf("svc import off a pipe without --yes = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".svc", goSdk, fakeGoVersion)); !os.IsNotExist(err) {
		t.Error("an unconfirmed import created a version directory")
	}
}

// --- the happy path ---------------------------------------------------------

func TestImportCopiesADirectoryIntoTheStoreAndActivatesIt(t *testing.T) {
	skipImportHappyPathOnWindows(t)
	home := t.TempDir()
	source := t.TempDir()
	seedRunnableGoSdk(t, source, fakeGoVersion)

	code, s := runHome(t, home, "import", goSdk, source, "--yes")
	if code != ExitOK {
		t.Fatalf("svc import = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if want := catalogWord(t, "zh", "path.importSuccess", nil); !strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", s.stdout, want)
	}

	// The tree is the post-alignment one: the flat bin/ the source had became
	// the go/bin the fetcher's GetBinDirs declares, which is what makes the
	// shims resolve.
	binDir := filepath.Join(home, ".svc", goSdk, fakeGoVersion, "go", "bin")
	for _, name := range []string{goBinaryName(), "gofmt"} {
		if _, err := os.Stat(filepath.Join(binDir, name)); err != nil {
			t.Errorf("the imported tree has no %s: %v", name, err)
		}
	}
	// An import copies; the user's directory is not consumed by it.
	if _, err := os.Stat(filepath.Join(source, "bin", goBinaryName())); err != nil {
		t.Errorf("the import took its source with it: %v", err)
	}

	// And the version the SDK reported about itself is the active one, read
	// back through the command a script would use.
	code, s = runHome(t, home, "current", goSdk)
	if code != ExitOK {
		t.Fatalf("svc current %s = exit %d; stderr:\n%s", goSdk, code, s.stderr)
	}
	if got := strings.TrimSpace(s.stdout.String()); got != fakeGoVersion {
		t.Errorf("svc current %s printed %q, want %q", goSdk, got, fakeGoVersion)
	}
}

func TestImportReportsTheDetectedVersionInJSON(t *testing.T) {
	skipImportHappyPathOnWindows(t)
	home := t.TempDir()
	source := t.TempDir()
	seedRunnableGoSdk(t, source, fakeGoVersion)

	code, s := runHome(t, home, "--json", "import", goSdk, source, "--yes")
	if code != ExitOK {
		t.Fatalf("svc --json import = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result MutationResult
	decodeJSON(t, s, &result)
	want := MutationResult{
		Action:  actionImport,
		SdkType: goSdk,
		Version: fakeGoVersion,
		Active:  true,
	}
	if result != want {
		t.Errorf("the document is %+v, want %+v", result, want)
	}
}

// TestImportResultShape pins the tokens an import adds to the --json contract.
func TestImportResultShape(t *testing.T) {
	if actionImport != "import" {
		t.Errorf("actionImport = %q, want %q -- it is a --json contract", actionImport, "import")
	}
	result := MutationResult{Action: actionImport, SdkType: goSdk, Version: fakeGoVersion, Active: true}
	if got, want := jsonKeysOf(t, result), []string{"action", "active", "sdkType", "version"}; !slices.Equal(got, want) {
		t.Errorf("MutationResult marshals to %v, want %v", got, want)
	}
}

// guiImport runs the import the way the GUI does: bootstrap at LevelWrite, then
// call the binding. DetailPanel.tsx handleImportLocal does exactly this once
// its picker has produced a path, so the picker's output is the argument here
// -- which is precisely the substitution the CLI makes.
func guiImport(t *testing.T, home, source string) {
	t.Helper()
	cfg, err := newTestConfig(home)
	if err != nil {
		t.Fatalf("config.NewConfig: %v", err)
	}
	ctx := context.Background()
	core, err := appcore.NewHome(cfg, nil).Bootstrap(ctx, dialogRefusingRuntime{ctx: ctx}, appcore.LevelWrite)
	if err != nil {
		t.Fatalf("the GUI's bootstrap: %v", err)
	}
	if err := core.Importer.ImportLocalSdk(goSdk, source); err != nil {
		t.Fatalf("the GUI's ImportLocalSdk: %v", err)
	}
}

// TestImportProducesTheSameStateAsTheGui is the parity test for the import
// half of §8: two identical homes, one imported through the GUI's own route and
// one through `svc import`, compared byte for byte over the same file set the
// switch parity test uses.
//
// Both runs import the SAME source directory. That is not laziness: the source
// path is copied into the state an import leaves behind, and two different
// temporary sources would make every one of those files differ for a reason
// that has nothing to do with which shell did the work.
func TestImportProducesTheSameStateAsTheGui(t *testing.T) {
	skipImportHappyPathOnWindows(t)
	source := t.TempDir()
	seedRunnableGoSdk(t, source, fakeGoVersion)

	guiHome := t.TempDir()
	// homeAt is called for its environment -- HOME, USERPROFILE, an empty PATH
	// and no SHELL -- which is the same world the CLI half runs in. The OpenHome
	// it returns is discarded: guiImport builds its config from the home
	// directly, the way the GUI's own startup does.
	_ = homeAt(t, guiHome)
	guiImport(t, guiHome, source)
	want := snapshotState(t, guiHome)

	cliHome := t.TempDir()
	code, s := runHome(t, cliHome, "import", goSdk, source, "--yes")
	if code != ExitOK {
		t.Fatalf("svc import = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	got := snapshotState(t, cliHome)

	for _, rel := range stateFiles {
		if want[rel] == stateMissing {
			t.Fatalf("the GUI path did not write %s, so the comparison of it proves nothing", rel)
		}
		if got[rel] == stateMissing {
			t.Errorf("the CLI path did not write %s but the GUI path did:\n%s", rel, want[rel])
			continue
		}
		if got[rel] != want[rel] {
			t.Errorf("%s differs between the two shells\n--- GUI ---\n%s\n--- CLI ---\n%s", rel, want[rel], got[rel])
		}
	}
}
