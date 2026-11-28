package cli

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The guards here read the shipped frontend catalogs and this package's own
// sources rather than copies of either. A3 of the CLI contract says the
// terminal words things exactly like the window does, which only holds while
// both read ONE catalog -- and while every key this package asks for is a key
// the GUI still has. The failure both directions catch is invisible at runtime:
// i18n.Catalog.T returns the key itself when it cannot resolve, so a renamed
// GUI key prints "detail.installed" in a column and nothing fails.

// catalogGroup is the group this package owns outright. Keys outside it are
// shared with the GUI, which is the point of reusing the catalog rather than
// shipping a CLI-only one.
const catalogGroup = "cli"

// sourceFiles lists this package's non-test Go sources. The working directory
// is the package directory, which is what `go test` uses.
func sourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	var files []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, name)
	}
	if len(files) == 0 {
		t.Fatal("no Go sources found in the package directory")
	}
	sort.Strings(files)
	return files
}

// catalogKeyRE finds a `....T("group.key"` call. Parsing the source with
// go/parser instead keeps this honest about what is a key and what is prose:
// only a string literal in the first argument position counts, so a key built
// by concatenation is not silently accepted here and left unresolved at
// runtime.
func translatedKeys(t *testing.T) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	keys := map[string][]string{}
	for _, name := range sourceFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			// Printer.T is the pass-through to i18n.Catalog.T; its key arrives
			// as a parameter, which is the one place that is legitimate rather
			// than dynamic.
			if decl, ok := n.(*ast.FuncDecl); ok && decl.Name.Name == "T" {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "T" || len(call.Args) == 0 {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				// Position is taken off the call, not the literal: the literal
				// is exactly what was not there.
				t.Errorf("%s:%d builds a catalog key dynamically; the guard cannot see it and neither can a rename",
					name, fset.Position(call.Pos()).Line)
				return true
			}
			key, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatalf("%s: unquote %s: %v", name, literal.Value, err)
			}
			keys[key] = append(keys[key], name)
			return true
		})
	}
	if len(keys) == 0 {
		t.Fatal("no catalog keys found in this package; translatedKeys has rotted")
	}
	return keys
}

// catalogGroupKeys returns the dotted keys of one top-level group of a shipped
// catalog, sorted.
func catalogGroupKeys(t *testing.T, lang, group string) []string {
	t.Helper()
	raw := repoFile(t, filepath.Join("frontend/src/i18n", lang+".json"))
	var tree map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatalf("parse the %s catalog: %v", lang, err)
	}
	body, ok := tree[group]
	if !ok {
		t.Fatalf("the %s catalog has no %q group", lang, group)
	}
	var leaves map[string]string
	if err := json.Unmarshal(body, &leaves); err != nil {
		t.Fatalf("the %q group of the %s catalog is not a flat string map: %v", group, lang, err)
	}
	keys := make([]string, 0, len(leaves))
	for key, value := range leaves {
		if strings.TrimSpace(value) == "" {
			t.Errorf("%s: %s.%s is empty", lang, group, key)
		}
		keys = append(keys, group+"."+key)
	}
	sort.Strings(keys)
	return keys
}

// TestTranslatedKeysResolveInTheShippedCatalogs is the guard that turns "a
// renamed GUI key shows up as an ugly literal" into a failing test. Every key
// this package asks for must exist, in both languages, and must render to
// something that is not the key.
func TestTranslatedKeysResolveInTheShippedCatalogs(t *testing.T) {
	for _, lang := range []string{"zh", "en"} {
		catalog := loadShippedCatalog(t, lang)
		for key, files := range translatedKeys(t) {
			if !catalog.Exists(key) {
				t.Errorf("%s: %q is asked for by %v but the shipped catalog has no such key", lang, key, files)
				continue
			}
			rendered := catalog.T(key, nil)
			if rendered == key {
				t.Errorf("%s: %q renders as the key itself", lang, key)
			}
			if strings.TrimSpace(rendered) == "" {
				t.Errorf("%s: %q renders as an empty string", lang, key)
			}
		}
	}
}

// TestCliCatalogGroupHasNoDeadKeys is the other direction. The `cli` group is
// this package's own, so a key nobody reads is a key that was added for a
// command that never landed, or one left behind by a rename -- and either way
// it is a translation a maintainer now has to keep in sync for nothing.
func TestCliCatalogGroupHasNoDeadKeys(t *testing.T) {
	used := translatedKeys(t)
	for _, lang := range []string{"zh", "en"} {
		for _, key := range catalogGroupKeys(t, lang, catalogGroup) {
			if _, ok := used[key]; !ok {
				t.Errorf("%s: %q is in the catalog but no command reads it", lang, key)
			}
		}
	}
}

// TestCliWordingIsSharedWithTheGUI pins the reuse claim itself: the words a
// command prints for something the window also shows must come from a group the
// GUI owns, not from a CLI-only synonym.
//
// Two kinds of key are in here. The first are the state labels `list` and `path`
// render, each with an exact counterpart in the home page. The second are the
// sentences the mutating commands print -- every confirmation question, its
// explanation, and every success or warning toast has a literal counterpart in
// DetailPanel.tsx, PathModal.tsx, SettingsPage.tsx, NpmRegistrySection.tsx or
// GlobalPackagesSection.tsx, so a CLI that grew its own wording for "Uninstall
// Go 1.22.0?" would be caught here rather than by a user noticing the two
// shells disagree.
//
// `about` and the `self` group are the same discipline applied to the About tab
// and to the update modal: the four row labels, the "up to date" toast, the two
// sentences the modal is built from, the button that doubles as `self update`'s
// question, and the rollback question are all reused rather than reworded. The
// one addition is `cli.downloadURL`, a label the modal has no need for because
// it turns the URL into a button -- and a terminal that printed a bare URL under
// nothing would leave a reader guessing what the line was.
//
// The exceptions are deliberate. `clean logs` and `clean inactive` port bindings
// no component calls, so there is no GUI sentence to borrow and their wording
// lives in the `cli` group (see cmd_clean.go). `cli.selfRollbackDesc` is the
// opposite case: the GUI sentence exists, `settings.rollbackConfirmDesc`, and
// cannot be reused because it promises the app will restart -- which is the one
// thing a terminal swap must not do. Borrowing it would have the CLI confirm an
// outcome it then declines to produce, so it gets its own wording and the
// question above it stays shared.
func TestCliWordingIsSharedWithTheGUI(t *testing.T) {
	shared := []string{
		"path.sdkColumn",
		"detail.currentVersion",
		"detail.installed",
		"detail.latest",
		"detail.currentConfig",
		"detail.needsSwitch",
		"detail.externalManagerHint",
		"detail.systemConflictTitle",
		"detail.systemConflictMsg",
		"detail.confirmInstallSdk",
		"detail.confirmInstallDesc",
		"detail.installSuccess",
		"detail.reinstallConfirm",
		"detail.reinstallConfirmDesc",
		"detail.switchConfirm",
		"detail.switchConfirmDesc",
		"detail.switchSuccess",
		"detail.reopenTerminalHint",
		"detail.uninstallConfirm",
		"detail.uninstallConfirmDesc",
		"detail.uninstallActiveConfirm",
		"detail.uninstallActiveConfirmDesc",
		"detail.uninstallSuccess",
		"detail.activeVersionDeleted",
		"detail.pathBinaryTitle",
		"path.importConfirm",
		"path.importConfirmDesc",
		"path.importSuccess",
		"sidebar.importConfirm",
		"sidebar.importConfirmDesc",
		"sidebar.importSuccess",
		"app.configured",
		"app.notConfigured",
		"app.inPathOnly",
		"app.systemManaged",
		"app.externalManager",
		"path.emptyPath",
		"path.noSdkDetected",
		"settings.filename",
		"settings.tmpCache",
		"settings.cleanSuccess",
		"logs.noLogs",
		"logs.size",
		"logs.modified",
		"logs.deleteConfirm",
		"logs.deleteConfirmDesc",
		"logs.deleteSuccess",
		"logs.deleteFail",
		"logs.cleanSelectedConfirm",
		"logs.cleanSelectedDesc",
		"logs.cleanSuccess",
		"logs.cleanFail",
		"settings.noContent",
		"sidebar.loadingSdkDetail",
		"detail.packageManagers",
		"detail.pmInstallSuccess",
		"detail.pmUpdateSuccess",
		"detail.npmRegistry",
		"detail.registryOfficial",
		"detail.registryChinaMirror",
		"detail.registryCustom",
		"detail.registryScopeHint",
		"detail.registrySwitchSuccess",
		"detail.globalPackages",
		"detail.globalPackagesEmpty",
		"detail.gpInstallSuccess",
		"detail.gpUninstallConfirm",
		"detail.gpUninstallConfirmDesc",
		"detail.gpUninstallSuccess",
		"settings.settingsSaved",
		"settings.githubTokenSaved",
		"settings.githubTokenCleared",
		"settings.githubTokenEmpty",
		"settings.githubTokenClear",
		"settings.githubTokenClearDesc",
		"settings.installPathConfirm",
		"settings.installPathConfirmDesc",
		"settings.installPathResetConfirm",
		"settings.installPathResetConfirmDesc",
		"settings.installPathSuccess",
		"endpoint.confirmSave",
		"endpoint.confirmSaveDesc",
		"endpoint.confirmResetOne",
		"endpoint.confirmResetOneDesc",
		"about.version",
		"about.goVersion",
		"about.license",
		"about.repo",
		"about.upToDate",
		"about.newVersion",
		"about.currentVersion",
		"about.downloadAndInstall",
		"settings.rollbackConfirm",
		"settings.proxyCheckBaidu",
		"settings.proxyCheckGoogle",
		"settings.proxyCheckSuccess",
		"settings.proxyCheckFail",
		"settings.proxyUrlRequired",
	}
	used := translatedKeys(t)
	for _, lang := range []string{"zh", "en"} {
		catalog := loadShippedCatalog(t, lang)
		for _, key := range shared {
			if !catalog.Exists(key) {
				t.Errorf("%s: the GUI key %q no longer exists; the CLI now words that state on its own", lang, key)
			}
			if _, ok := used[key]; !ok {
				t.Errorf("%q is no longer read by any command; the CLI and the GUI have stopped sharing that wording", key)
			}
		}
	}
}
