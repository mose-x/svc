package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The event names are a three-way contract: the service layer emits them, the
// GUI subscribes to them, and this package routes them. All three spell the
// same four strings, in three languages, in files that never import each other.
// The guards below read the shipped sources and fail when one side moves.

// cliEventNames is every name this package declares a constant for.
func cliEventNames() []string {
	return []string{EventInstallProgress, EventScanProgress, EventVersionsRefreshed, EventUpdateProgress}
}

// emitRE finds a literal event name at an EventsEmit call site. The interface
// declaration in internal/wailsrt and the clirt implementation both name the
// parameter rather than a string, so they never match.
var emitRE = regexp.MustCompile(`EventsEmit\(\s*"([^"]+)"`)

// listenRE finds a literal event name at an EventsOn call site. \s spans the
// newline in the one multi-line subscription (DetailPanel's background
// refresh), and the `EventsOn: (` type declaration in vite-env.d.ts has no
// string after the paren so it does not match either.
var listenRE = regexp.MustCompile(`EventsOn\(\s*['"]([^'"]+)['"]`)

// collectLiterals walks root and returns every literal matched by re in the
// files whose name passes want, paired with the file it came from so a failure
// can point at it.
func collectLiterals(t *testing.T, root string, re *regexp.Regexp, want func(name string) bool) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(filepath.Join(repoRootRel, root), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "node_modules" || entry.Name() == "dist" {
				return fs.SkipDir
			}
			return nil
		}
		if !want(entry.Name()) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range re.FindAllStringSubmatch(string(raw), -1) {
			found = append(found, match[1])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return slices.Compact(slices.Sorted(slices.Values(found)))
}

func goSource(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

func frontendSource(name string) bool {
	return strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".tsx")
}

// emittedEventNames is the set the service layer actually emits. internal/cli
// contributes nothing here on its own: it declares the constants and routes
// them, and clirt's EventsEmit names its parameter rather than a literal.
func emittedEventNames(t *testing.T) []string {
	t.Helper()
	return collectLiterals(t, "internal", emitRE, func(name string) bool { return goSource(name) })
}

// listenedEventNames is the set the GUI subscribes to.
func listenedEventNames(t *testing.T) []string {
	t.Helper()
	return collectLiterals(t, "frontend/src", listenRE, frontendSource)
}

// TestEventNamesAreEmittedByTheServiceLayer is the guard that matters most: an
// event the installer emits and this package has never heard of is dropped, and
// the terminal goes quiet while the window shows a progress bar. That is a
// silent regression, so it needs a test rather than a reviewer's memory.
func TestEventNamesAreEmittedByTheServiceLayer(t *testing.T) {
	ours := cliEventNames()
	emitted := emittedEventNames(t)

	if len(emitted) == 0 {
		t.Fatal("emitRE found no EventsEmit call sites; the expression has rotted")
	}
	for _, name := range emitted {
		if !slices.Contains(ours, name) {
			t.Errorf("the service layer emits %q but this package has no constant for it; RouteEvent would drop it", name)
		}
	}
	// And the converse: a constant nothing emits is dead code that would keep
	// a future reader guessing which spelling is correct.
	for _, name := range ours {
		if !slices.Contains(emitted, name) {
			t.Errorf("constant %q is never emitted by the service layer", name)
		}
	}
}

// TestEventNamesAreListenedToByTheFrontend keeps the GUI in the same loop. A3
// of the CLI contract is that both shells see the same events; if the window
// stops subscribing to a name, that name is no longer a shared contract and the
// CLI constant should go with it.
func TestEventNamesAreListenedToByTheFrontend(t *testing.T) {
	ours := cliEventNames()
	listened := listenedEventNames(t)

	if len(listened) == 0 {
		t.Fatal("listenRE found no EventsOn call sites; the expression has rotted")
	}
	for _, name := range listened {
		if !slices.Contains(ours, name) {
			t.Errorf("the GUI subscribes to %q but this package has no constant for it", name)
		}
	}
	for _, name := range ours {
		if !slices.Contains(listened, name) {
			t.Errorf("constant %q has no EventsOn subscriber in frontend/src", name)
		}
	}
}
