package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"svc/internal/config"
	"svc/internal/pkgmgr"
	"svc/internal/sdk"
)

// `svc pkg` is a thin layer over internal/pkgmgr, and pkgmgr talks to real
// package managers. These tests therefore bring their own: one fake npm per
// test, written into the bin directory of a seeded Node.js version, which is
// the only directory pkgmgr's scoped PATH contains. Nothing here depends on
// what the machine running the test has installed -- which is also what makes
// the happy paths testable at all, on all three CI platforms.
//
// Two properties of the group make that possible. It bootstraps at LevelRead,
// so no test writes the Windows registry PATH (the reason P4a's mutating tests
// skip there). And its mutations are commands run against a directory rather
// than edits of svc's own files, so a fake binary that records its arguments
// is a complete stand-in for the real thing.
//
// What the fakes cannot cover is the branching inside pkgmgr -- the corepack
// fallback, the pnpm pinning per Node major, the version-output parsers. That
// is internal/pkgmgr's own test suite's job, and these tests deliberately pick
// the straight-line managers (cnpm, nrm) so they exercise the command layer
// rather than re-testing the service through it.

const (
	// nodeVersion is the version the seeded Node.js claims to be.
	nodeVersion = "22.11.0"
	// npmVersion is what the fake npm answers to --version.
	npmVersion = "10.9.0"
	// globalPackagesJSON is the fake npm's answer to `ls -g --depth=0 --json`,
	// in the shape parseGlobalPackages reads: a dependencies map whose entries
	// carry versions. Unsorted on purpose -- the service sorts by name, and a
	// test that fed it a sorted document would not notice if that stopped.
	globalPackagesJSON = `{"dependencies":{"typescript":{"version":"5.6.3"},"npm":{"version":"10.9.0"}}}`
)

// writeFakeTool writes one fake executable into dir: a .cmd batch script on
// Windows, where resolveInPath probes .cmd before the extensionless name, and
// a #!/bin/sh script everywhere else. internal/pkgmgr has a helper of the same
// name and shape for the same reason.
func writeFakeTool(t *testing.T, dir, name, winBody, unixBody string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, name+".cmd")
		if err := os.WriteFile(path, []byte(winBody), 0644); err != nil {
			t.Fatalf("write the fake %s: %v", name, err)
		}
		return
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(unixBody), 0755); err != nil {
		t.Fatalf("write the fake %s: %v", name, err)
	}
}

// writeFakeNpm writes an npm that answers every verb these commands use and
// records the ones that change something into marker, argument for argument.
//
// The recorded text is the whole command line, verb included, so an assertion
// on it pins what ran and not merely that something did. `config get registry`
// is the one verb that answers instead of recording: it is how the registry
// command reads, and its answer carries the trailing slash npm's own default
// prints, so the trimming GetNpmRegistry promises is under test too.
func writeFakeNpm(t *testing.T, dir, marker string) {
	t.Helper()
	unix := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"--version) echo \"" + npmVersion + "\" ;;\n" +
		"config) if [ \"$2\" = \"get\" ]; then echo \"" + pkgmgr.RegistryOfficial + "/\";" +
		" else printf '%s' \"$*\" > \"" + marker + "\"; fi ;;\n" +
		"ls) echo '" + globalPackagesJSON + "' ;;\n" +
		"install|uninstall) printf '%s' \"$*\" > \"" + marker + "\" ;;\n" +
		"esac\n" +
		"exit 0\n"
	// goto rather than parenthesised blocks: a `>` redirect or an echoed brace
	// inside an `if (...)` block is where batch scripts stop meaning what they
	// say, and the whole point of the fake is that its behaviour is not a
	// question.
	win := "@echo off\r\n" +
		"if \"%~1\"==\"--version\" goto version\r\n" +
		"if \"%~1\"==\"config\" goto config\r\n" +
		"if \"%~1\"==\"ls\" goto ls\r\n" +
		"if \"%~1\"==\"install\" goto record\r\n" +
		"if \"%~1\"==\"uninstall\" goto record\r\n" +
		"exit /b 0\r\n" +
		":version\r\necho " + npmVersion + "\r\nexit /b 0\r\n" +
		":config\r\nif \"%~2\"==\"get\" goto registry\r\ngoto record\r\n" +
		":registry\r\necho " + pkgmgr.RegistryOfficial + "/\r\nexit /b 0\r\n" +
		":ls\r\necho " + globalPackagesJSON + "\r\nexit /b 0\r\n" +
		":record\r\n>\"" + marker + "\" echo %*\r\nexit /b 0\r\n"
	writeFakeTool(t, dir, "npm", win, unix)
}

// markerFile is a path in a directory of its own, so a test can tell "the fake
// npm was never run" from "it was run and wrote nothing".
func markerFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "npm-ran")
}

// readMarker returns what the fake npm recorded, trimmed: `echo` on Windows
// ends the line with CRLF and keeps the space before a redirect, and neither
// is part of the command line being asserted on.
func readMarker(t *testing.T, marker string) string {
	t.Helper()
	raw, err := os.ReadFile(marker)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read the marker %s: %v", marker, err)
	}
	return strings.TrimSpace(string(raw))
}

// seedNodeVersion lays down the directory tree an extracted Node.js release
// has and makes it the active version, returning the bin directory the fake
// executables go into.
//
// The extra bin/ level is not decoration on Unix: NodejsFetcher.GetBinDirs
// reports "bin" because the official archive extracts into one, while on
// Windows node.exe sits in the version directory itself. Seeding the wrong
// layout leaves pkgmgr's scoped PATH pointing at an empty directory, and every
// test that follows it would be measuring nothing.
//
// The active version is written into config.json directly rather than through
// `svc switch`, which bootstraps at LevelWrite and would merge the shims
// directory into the real HKCU\Environment on Windows.
func seedNodeVersion(t *testing.T, home string) string {
	t.Helper()
	binDir := filepath.Join(home, ".svc", string(sdk.NodeJS), nodeVersion)
	if !config.IsWindows() {
		binDir = filepath.Join(binDir, "bin")
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", binDir, err)
	}
	setActiveVersion(t, home, string(sdk.NodeJS), nodeVersion)
	return binDir
}

// setActiveVersion points one SDK at one of its installed versions in
// config.json, the file both shells read. The SVC directory is created first
// because a test may need an active version of an SDK it has no files for --
// composer's refusal, for instance, depends on PHP being active and on nothing
// else.
func setActiveVersion(t *testing.T, home, sdkType, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".svc"), 0755); err != nil {
		t.Fatalf("mkdir the SVC dir: %v", err)
	}
	cfg, err := newTestConfig(home)
	if err != nil {
		t.Fatalf("open the config in %s: %v", home, err)
	}
	if err := cfg.SetActiveVersion(sdkType, version); err != nil {
		t.Fatalf("set the active %s version: %v", sdkType, err)
	}
}

// seededNodeHome is the home most of these tests want: one Node.js version,
// active, with a fake npm in it. It returns the home, the marker the fake
// writes to, and the invocation function.
func seededNodeHome(t *testing.T) (string, string, func(args ...string) (int, *streams)) {
	t.Helper()
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)
	return home, marker, func(args ...string) (int, *streams) {
		return runHome(t, home, args...)
	}
}

// --- the envelope -----------------------------------------------------------

func TestPackageResultShape(t *testing.T) {
	result := PackageResult{Action: actionInstall, Target: pkgTargetManager, Name: "yarn"}
	if got, want := jsonKeysOf(t, result), []string{"action", "name", "target"}; !slices.Equal(got, want) {
		t.Errorf("PackageResult marshals to %v, want %v", got, want)
	}
	// The tokens are the contract a script reads; the literals are pinned so a
	// rename of the constant is a decision rather than a typo.
	if actionInstall != "install" || actionUpdate != "update" || actionUninstall != "uninstall" {
		t.Errorf("the action tokens are %q/%q/%q", actionInstall, actionUpdate, actionUninstall)
	}
	if pkgTargetManager != "package-manager" || pkgTargetGlobal != "global-package" {
		t.Errorf("the target tokens are %q/%q", pkgTargetManager, pkgTargetGlobal)
	}
}

func TestEmptyManagersIsAnArrayNotNull(t *testing.T) {
	encoded, err := json.Marshal(emptyManagers(nil))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != "[]" {
		t.Errorf("emptyManagers(nil) marshals to %s, want []", encoded)
	}
	if got := emptyManagers([]sdk.PackageManagerInfo{{Name: "npm"}}); len(got) != 1 {
		t.Errorf("emptyManagers dropped the managers it was given: %v", got)
	}
}

// --- pkg list ---------------------------------------------------------------

func TestPkgListProbesTheActiveVersion(t *testing.T) {
	_, _, run := seededNodeHome(t)
	code, s := run("pkg", "list", "nodejs")
	if code != ExitOK {
		t.Fatalf("svc pkg list = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}

	lines := outLines(s)
	// The fake npm is the only manager present, so it is the only row that
	// carries a version; the other five are cards svc knows about and could
	// not find. Both halves of that are the point of the command.
	npm := tableRow(lines, "npm")
	if npm == "" {
		t.Fatalf("stdout has no npm row:\n%s", s.stdout)
	}
	if !strings.Contains(npm, npmVersion) || !strings.Contains(npm, catalogWord(t, "zh", "detail.installed", nil)) {
		t.Errorf("the npm row = %q, want the version and the installed word", npm)
	}
	yarn := tableRow(lines, "yarn")
	if !strings.Contains(yarn, placeholderNone) || !strings.Contains(yarn, catalogWord(t, "zh", "cli.stateNotInstalled", nil)) {
		t.Errorf("the yarn row = %q, want a placeholder and the not-installed word", yarn)
	}
	// Six cards for Node.js, and a header: the list is the service's, not a
	// subset the command decided on.
	if len(lines) != 7 {
		t.Errorf("stdout has %d lines, want a header and six managers:\n%s", len(lines), s.stdout)
	}
}

func TestPkgListInJSONIsTheServiceValueVerbatim(t *testing.T) {
	_, _, run := seededNodeHome(t)
	code, s := run("pkg", "list", "nodejs", "--json")
	if code != ExitOK {
		t.Fatalf("svc pkg list --json = exit %d; stderr:\n%s", code, s.stderr)
	}
	var managers []sdk.PackageManagerInfo
	decodeJSON(t, s, &managers)
	if len(managers) != 6 {
		t.Fatalf("the document has %d managers, want 6: %v", len(managers), managers)
	}
	if got, want := jsonKeysOf(t, managers[0]), []string{"installed", "name", "parentSdk", "version"}; !slices.Equal(got, want) {
		t.Errorf("a manager marshals to %v, want the service struct's %v", got, want)
	}
	for _, manager := range managers {
		if manager.ParentSdk != sdk.NodeJS {
			t.Errorf("%s reports %q as its parent SDK", manager.Name, manager.ParentSdk)
		}
		if manager.Name == "npm" && (!manager.Installed || manager.Version != npmVersion) {
			t.Errorf("npm = %+v, want it installed at %s", manager, npmVersion)
		}
		if manager.Name == "pnpm" && manager.Installed {
			t.Errorf("pnpm = %+v, want it absent: no fake was written for it", manager)
		}
	}
}

func TestPkgListWithoutAnActiveVersionReportsNothingAndSaysWhy(t *testing.T) {
	t.Run("a pipe gets an empty array and no prose", func(t *testing.T) {
		code, s := pipedRun(t, nil, "pkg", "list", "nodejs", "--json")
		if code != ExitOK {
			t.Fatalf("svc pkg list = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		var managers []sdk.PackageManagerInfo
		decodeJSON(t, s, &managers)
		if len(managers) != 0 {
			t.Errorf("the document has %d managers, want none: %v", len(managers), managers)
		}
		if s.stderr.Len() != 0 {
			t.Errorf("a piped list wrote %q to stderr", s.stderr)
		}
	})

	t.Run("a terminal is told why the list is empty", func(t *testing.T) {
		code, s := terminalRun(t, nil, "pkg", "list", "nodejs")
		if code != ExitOK {
			t.Fatalf("svc pkg list = exit %d, want %d", code, ExitOK)
		}
		want := catalogWord(t, "zh", "cli.noPackageManagers",
			map[string]string{"sdk": sdk.SdkDisplayName(sdk.NodeJS)})
		if got := outLines(s); !slices.Equal(got, []string{want}) {
			t.Errorf("stdout = %q, want only %q", got, want)
		}
		// The reason is a note: an SDK with no active version has nothing to
		// probe, which is a different fact from "this SDK has no managers".
		reason := catalogWord(t, "zh", "cli.noActiveVersion",
			map[string]string{"sdk": sdk.SdkDisplayName(sdk.NodeJS)})
		if !strings.Contains(s.stderr.String(), reason) {
			t.Errorf("stderr = %q, want it to name the missing active version", s.stderr)
		}
	})
}

func TestPkgListOfAnSdkWithNoManagersIsNotAMissingVersion(t *testing.T) {
	home := t.TempDir()
	seedGoVersion(home, "1.22.0")
	setActiveVersion(t, home, goSdk, "1.22.0")

	code, s := interactiveRunHome(t, home, "", "pkg", "list", goSdk)
	if code != ExitOK {
		t.Fatalf("svc pkg list go = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	want := catalogWord(t, "zh", "cli.noPackageManagers", map[string]string{"sdk": goDisplayName()})
	if got := outLines(s); !slices.Equal(got, []string{want}) {
		t.Errorf("stdout = %q, want only %q", got, want)
	}
	// Go does have an active version; it simply has no package managers in
	// svc's model. Blaming the version would send the user off to fix the
	// wrong thing, so the note that names it must stay quiet here.
	reason := catalogWord(t, "zh", "cli.noActiveVersion", map[string]string{"sdk": goDisplayName()})
	if strings.Contains(s.stderr.String(), reason) {
		t.Errorf("stderr = %q, want no note about an active version", s.stderr)
	}
}

func TestPkgListRefusesAnUnknownSdk(t *testing.T) {
	code, s := pipedRun(t, nil, "pkg", "list", "golang")
	if code != ExitNotFound {
		t.Fatalf("svc pkg list golang = exit %d, want %d", code, ExitNotFound)
	}
	want := catalogWord(t, "zh", "errors.unknown-sdk-type", map[string]string{"sdk": "golang"})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
}

func TestPkgListAcceptsTheDisplayName(t *testing.T) {
	_, _, run := seededNodeHome(t)
	code, s := run("pkg", "list", sdk.SdkDisplayName(sdk.NodeJS), "--json")
	if code != ExitOK {
		t.Fatalf("svc pkg list %s = exit %d; stderr:\n%s", sdk.SdkDisplayName(sdk.NodeJS), code, s.stderr)
	}
	var managers []sdk.PackageManagerInfo
	decodeJSON(t, s, &managers)
	if len(managers) != 6 {
		t.Errorf("the document has %d managers, want 6", len(managers))
	}
}

// --- pkg install / update ---------------------------------------------------

func TestPkgInstallRunsTheManagerAndReportsTheGuiSentence(t *testing.T) {
	home, marker, run := seededNodeHome(t)
	code, s := run("pkg", "install", "cnpm")
	if code != ExitOK {
		t.Fatalf("svc pkg install cnpm = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	// cnpm is installed from the China mirror by an explicit flag, and the flag
	// is a separate argv element so the user's own registry is untouched. Both
	// facts are only visible in the command line that actually ran.
	if got, want := readMarker(t, marker), "install -g cnpm --registry="+pkgmgr.RegistryChina; got != want {
		t.Errorf("npm ran %q, want %q", got, want)
	}
	want := catalogWord(t, "zh", "detail.pmInstallSuccess", map[string]string{"name": "cnpm"})
	if got := outLines(s); !slices.Equal(got, []string{want}) {
		t.Errorf("stdout = %q, want only %q", got, want)
	}
	// The install wrote where npm writes, not into svc's config: the group is
	// LevelRead, and a command that quietly became LevelWrite would show up
	// here as a shims.json nobody asked for.
	if _, err := os.Stat(filepath.Join(home, ".svc", "shims.json")); !os.IsNotExist(err) {
		t.Errorf("shims.json exists (err %v); a pkg command reached the write level", err)
	}
}

func TestPkgInstallReportsTheResultInJSON(t *testing.T) {
	_, marker, run := seededNodeHome(t)
	code, s := run("pkg", "install", "nrm", "--json")
	if code != ExitOK {
		t.Fatalf("svc pkg install nrm --json = exit %d; stderr:\n%s", code, s.stderr)
	}
	if got, want := readMarker(t, marker), "install -g nrm"; got != want {
		t.Errorf("npm ran %q, want %q", got, want)
	}
	var result PackageResult
	decodeJSON(t, s, &result)
	if result != (PackageResult{Action: actionInstall, Target: pkgTargetManager, Name: "nrm"}) {
		t.Errorf("the document is %+v", result)
	}
}

func TestPkgInstallOfNpmIsRefusedBeforeAnythingRuns(t *testing.T) {
	// npm ships with Node.js, so there is nothing to install and the service
	// says so with its need-sdk marker. The assertion that matters is the one
	// about the marker file: a refusal that still ran npm would be a refusal
	// in name only.
	_, marker, run := seededNodeHome(t)
	code, s := run("pkg", "install", "npm")
	if code != ExitUsage {
		t.Fatalf("svc pkg install npm = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	want := catalogWord(t, "zh", "errors.need-sdk", map[string]string{"name": "npm", "sdk": "Node.js"})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
	if got := readMarker(t, marker); got != "" {
		t.Errorf("npm ran %q; a refused install must run nothing", got)
	}
}

func TestPkgInstallComposerIsAManualDownload(t *testing.T) {
	home := t.TempDir()
	setActiveVersion(t, home, "php", "8.3.0")
	code, s := runHome(t, home, "pkg", "install", "composer")
	if code != ExitUsage {
		t.Fatalf("svc pkg install composer = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	if !strings.Contains(s.stderr.String(), "getcomposer.org") {
		t.Errorf("stderr = %q, want the manual download URL", s.stderr)
	}
}

func TestPkgInstallRefusesAnUnknownName(t *testing.T) {
	_, marker, run := seededNodeHome(t)
	code, s := run("pkg", "install", "bogus")
	if code != ExitUsage {
		t.Fatalf("svc pkg install bogus = exit %d, want %d", code, ExitUsage)
	}
	want := catalogWord(t, "zh", "errors.unknown-package-manager", map[string]string{"name": "bogus"})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
	if got := readMarker(t, marker); got != "" {
		t.Errorf("npm ran %q; an unknown name must run nothing", got)
	}
}

func TestPkgUpdateInstallsTheLatestVersion(t *testing.T) {
	_, marker, run := seededNodeHome(t)
	code, s := run("pkg", "update", "nrm")
	if code != ExitOK {
		t.Fatalf("svc pkg update nrm = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if got, want := readMarker(t, marker), "install -g nrm@latest"; got != want {
		t.Errorf("npm ran %q, want %q", got, want)
	}
	want := catalogWord(t, "zh", "detail.pmUpdateSuccess", map[string]string{"name": "nrm"})
	if got := outLines(s); !slices.Equal(got, []string{want}) {
		t.Errorf("stdout = %q, want only %q", got, want)
	}
}

func TestPkgUpdateRefusesAnUnknownName(t *testing.T) {
	_, marker, run := seededNodeHome(t)
	code, _ := run("pkg", "update", "bogus", "--json")
	if code != ExitUsage {
		t.Fatalf("svc pkg update bogus = exit %d, want %d", code, ExitUsage)
	}
	if got := readMarker(t, marker); got != "" {
		t.Errorf("npm ran %q; an unknown name must run nothing", got)
	}
}

func TestPkgUpdateReportsTheResultInJSON(t *testing.T) {
	_, _, run := seededNodeHome(t)
	code, s := run("pkg", "update", "corepack", "--json")
	if code != ExitOK {
		t.Fatalf("svc pkg update corepack --json = exit %d; stderr:\n%s", code, s.stderr)
	}
	var result PackageResult
	decodeJSON(t, s, &result)
	if result != (PackageResult{Action: actionUpdate, Target: pkgTargetManager, Name: "corepack"}) {
		t.Errorf("the document is %+v", result)
	}
}

// A manager that is not installed is not refused: `pkg update` for the
// npm-installed ones is `install <name>@latest`, which is how the GUI updates
// a card it is looking at, and checking first would mean six --version probes
// to reach the same command line.
func TestPkgUpdateOfAMissingManagerStillUpdates(t *testing.T) {
	_, marker, run := seededNodeHome(t)
	code, _ := run("pkg", "update", "pnpm")
	if code != ExitOK {
		t.Fatalf("svc pkg update pnpm = exit %d, want %d", code, ExitOK)
	}
	if got := readMarker(t, marker); !strings.Contains(got, "pnpm") {
		t.Errorf("npm ran %q, want a pnpm install", got)
	}
}

// --- completion -------------------------------------------------------------

func TestNoFileCompletionOffersNothingAtAll(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))

	// The argument names a package, so there are no candidates svc could
	// honestly offer -- and the shell's own filename completion is a wrong
	// answer rather than a missing one.
	for _, args := range [][]string{nil, {"yar"}} {
		candidates, directive := r.noFileCompletion(nil, args, "yar")
		if len(candidates) != 0 {
			t.Errorf("args %v offered %v, want nothing", args, candidates)
		}
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("args %v directive = %v, want NoFileComp", args, directive)
		}
	}
}

func TestPkgCommandsAreWiredIntoTheTree(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	root := r.newRootCommand()
	root.InitDefaultHelpCmd()

	pkg, _, err := root.Find([]string{"pkg"})
	if err != nil || pkg == nil || pkg.Name() != "pkg" {
		t.Fatalf("the root has no pkg command: %v", err)
	}
	var names []string
	for _, command := range pkg.Commands() {
		names = append(names, command.Name())
	}
	slices.Sort(names)
	want := []string{"global", "install", "list", "registry", "update"}
	if !slices.Equal(names, want) {
		t.Errorf("svc pkg exposes %v, want %v", names, want)
	}

	registry, _, err := root.Find([]string{"pkg", "registry"})
	if err != nil || registry == nil {
		t.Fatalf("svc pkg registry is missing: %v", err)
	}
	// The parent is runnable -- reading the registry is the common case -- so
	// it must not be a bare group, or `svc pkg registry` would print help
	// instead of the value.
	if registry.RunE == nil {
		t.Error("svc pkg registry has no RunE; it would print help instead of the registry")
	}
	if sub, _, err := registry.Find([]string{"set"}); err != nil || sub == nil || sub.Name() != "set" {
		t.Errorf("svc pkg registry set is missing: %v", err)
	}
}
