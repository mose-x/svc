package cli

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"svc/internal/pkgmgr"
	"svc/internal/sdk"
	"svc/internal/svclock"
)

// The npm-scoped half of `svc pkg`. Same fake npm as cmd_pkg_test.go, same
// reason: the commands are thin, and the interesting behaviour -- what npm was
// asked to do, what the user was told, what a refusal costs -- is all visible
// from the command line the fake records.
//
// `svc pkg global uninstall` is the one command in the group that asks before
// it acts, so it is also the one with something to test around the prompt:
// that it asks with the GUI's own two sentences, that a "no" leaves the
// package installed and exits as a cancellation rather than a failure, that a
// pipe is refused rather than guessed at, and that npm itself is refused
// before the question is ever asked.

// --- the pure helpers -------------------------------------------------------

func TestRegistryModeClassifiesTheRegistriesTheGuiOffers(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"the official registry", pkgmgr.RegistryOfficial, registryModeOfficial},
		{"the official registry with npm's trailing slash", pkgmgr.RegistryOfficial + "/", registryModeOfficial},
		{"the China mirror", pkgmgr.RegistryChina, registryModeChina},
		{"the China mirror with a trailing slash", pkgmgr.RegistryChina + "/", registryModeChina},
		{"a registry with surrounding space", "  " + pkgmgr.RegistryOfficial + "  ", registryModeOfficial},
		{"anything else is custom", "https://npm.example.com/", registryModeCustom},
		{"a near miss of the official host is custom", "https://registry.npmjs.org.example.com", registryModeCustom},
		// The comparison is of the URL, not of a lowercased copy of it: a host
		// in the wrong case is a different registry as far as npm is concerned,
		// and calling it official would be a claim svc cannot check.
		{"the official host in the wrong case is custom", "https://Registry.npmjs.org", registryModeCustom},
		{"nothing loaded is nothing classified", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := registryMode(c.url); got != c.want {
				t.Errorf("registryMode(%q) = %q, want %q", c.url, got, c.want)
			}
		})
	}
}

func TestResolveRegistryTargetMapsTheAliases(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{registryAliasOfficial, pkgmgr.RegistryOfficial},
		{registryAliasChina, pkgmgr.RegistryChina},
		// The aliases are words, so they are matched as words: a shell user
		// who capitalises one meant the same thing.
		{"Official", pkgmgr.RegistryOfficial},
		{" CHINA ", pkgmgr.RegistryChina},
		// A URL is a URL, and it is returned trimmed because that is the exact
		// form the service hands to npm -- the sentence reporting the switch
		// then quotes what was written rather than what was typed.
		{" https://npm.example.com/ ", "https://npm.example.com/"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := resolveRegistryTarget(c.input); got != c.want {
			t.Errorf("resolveRegistryTarget(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// registryConstantRE reads the two URLs out of the component that renders them
// as radio buttons.
var registryConstantRE = regexp.MustCompile(`(?m)^const (\w+) = '([^']+)'$`)

// TestRegistryAliasesResolveToTheFrontendButtons pins the claim that `official`
// and `china` mean what the GUI's two one-click choices mean. The URLs live in
// three places -- the component, pkgmgr, and the mirror cnpm is installed from
// -- and only the last two are Go, so the guard reads the component rather
// than trusting a comment about it.
func TestRegistryAliasesResolveToTheFrontendButtons(t *testing.T) {
	source := string(repoFile(t, "frontend/src/components/Panel/NpmRegistrySection.tsx"))
	found := map[string]string{}
	for _, match := range registryConstantRE.FindAllStringSubmatch(source, -1) {
		found[match[1]] = match[2]
	}
	want := map[string]string{"OFFICIAL": pkgmgr.RegistryOfficial, "CHINA": pkgmgr.RegistryChina}
	for name, url := range want {
		if got := found[name]; got != url {
			t.Errorf("NpmRegistrySection's %s = %q, but the CLI's %q alias resolves to %q", name, got, name, url)
		}
	}
	// The aliases themselves: what a user types must land on those two values.
	if got := resolveRegistryTarget(registryAliasOfficial); got != found["OFFICIAL"] {
		t.Errorf("`%s` resolves to %q, want the component's %q", registryAliasOfficial, got, found["OFFICIAL"])
	}
	if got := resolveRegistryTarget(registryAliasChina); got != found["CHINA"] {
		t.Errorf("`%s` resolves to %q, want the component's %q", registryAliasChina, got, found["CHINA"])
	}
}

func TestRegistryCompletionOffersTheTwoAliases(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))

	candidates, directive := r.registryCompletion(nil, nil, "o")
	if !slices.Equal(candidates, []string{registryAliasOfficial}) {
		t.Errorf("completing `o` offered %v, want [official]", candidates)
	}
	candidates, directive = r.registryCompletion(nil, nil, "")
	if !slices.Equal(candidates, []string{registryAliasOfficial, registryAliasChina}) {
		t.Errorf("completing nothing offered %v, want both aliases", candidates)
	}
	// A registry is a URL: the shell's filename completion is not a fallback
	// here, it is a wrong answer.
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("the directive = %v, want NoFileComp", directive)
	}
	if candidates, directive := r.registryCompletion(nil, []string{"official"}, ""); len(candidates) != 0 ||
		directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("a second argument offered %v with directive %v, want nothing", candidates, directive)
	}
}

// --- pkg registry -----------------------------------------------------------

func TestRegistryShowReportsTheLiveValue(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	code, s := interactiveRunHome(t, home, "", "pkg", "registry")
	if code != ExitOK {
		t.Fatalf("svc pkg registry = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	out := s.stdout.String()
	// npm's own default prints a trailing slash; GetNpmRegistry promises it
	// trimmed, and the fake npm answers with the slash to keep that promise
	// under test.
	if !strings.Contains(out, pkgmgr.RegistryOfficial) || strings.Contains(out, pkgmgr.RegistryOfficial+"/") {
		t.Errorf("stdout = %q, want the registry without its trailing slash", out)
	}
	if !strings.Contains(out, catalogWord(t, "zh", "detail.npmRegistry", nil)) {
		t.Errorf("stdout = %q, want the GUI's own label for the registry", out)
	}
	if !strings.Contains(out, catalogWord(t, "zh", "detail.registryOfficial", nil)) {
		t.Errorf("stdout = %q, want the mode named with the GUI's word for it", out)
	}
	// The scope line is the one thing a user about to run `set` needs: the
	// choice is user-global, not per version. It is advice, so it belongs on
	// stderr and only at a terminal.
	if want := catalogWord(t, "zh", "detail.registryScopeHint", nil); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
	if got := readMarker(t, marker); got != "" {
		t.Errorf("reading the registry ran %q; a read must not write", got)
	}
}

func TestRegistryShowInJSONReportsTheValueAndItsMode(t *testing.T) {
	home := t.TempDir()
	writeFakeNpm(t, seedNodeVersion(t, home), markerFile(t))

	code, s := runHome(t, home, "pkg", "registry", "--json")
	if code != ExitOK {
		t.Fatalf("svc pkg registry --json = exit %d; stderr:\n%s", code, s.stderr)
	}
	var result RegistryResult
	decodeJSON(t, s, &result)
	if result != (RegistryResult{Registry: pkgmgr.RegistryOfficial, Mode: registryModeOfficial}) {
		t.Errorf("the document is %+v", result)
	}
	if got, want := jsonKeysOf(t, result), []string{"mode", "registry"}; !slices.Equal(got, want) {
		t.Errorf("RegistryResult marshals to %v, want %v", got, want)
	}
	if s.stderr.Len() != 0 {
		t.Errorf("a piped read wrote %q to stderr", s.stderr)
	}
}

// `pkg global list` is deliberately absent from this table: the service
// answers a missing Node.js with an empty list and no error, so that command
// succeeds and explains itself instead. It has its own test below.
func TestRegistryWithoutAnActiveNodeRefuses(t *testing.T) {
	cases := []struct {
		args []string
		// name is what the refusal names as needing Node.js: the registry
		// commands act through npm and say so, the package commands name the
		// package the user asked about.
		name string
	}{
		{[]string{"pkg", "registry"}, "npm"},
		{[]string{"pkg", "registry", "set", registryAliasOfficial}, "npm"},
		{[]string{"pkg", "global", "install", "typescript"}, "typescript"},
		{[]string{"pkg", "global", "uninstall", "typescript", "--yes"}, "typescript"},
		{[]string{"pkg", "global", "update", "typescript"}, "typescript"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			code, s := pipedRun(t, nil, c.args...)
			if code != ExitUsage {
				t.Fatalf("svc %s = exit %d, want %d; stderr:\n%s", strings.Join(c.args, " "), code, ExitUsage, s.stderr)
			}
			want := catalogWord(t, "zh", "errors.need-sdk", map[string]string{"name": c.name, "sdk": "Node.js"})
			if !strings.Contains(s.stderr.String(), want) {
				t.Errorf("stderr = %q, want %q", s.stderr, want)
			}
		})
	}
}

// `pkg global list` is the exception in the group: the service answers a
// missing Node.js with an empty list and no error, exactly as it answers the
// GUI, so the command succeeds and says why the list is empty.
func TestGlobalListWithoutAnActiveNodeIsAnEmptyListWithAReason(t *testing.T) {
	t.Run("a pipe gets an empty array", func(t *testing.T) {
		code, s := pipedRun(t, nil, "pkg", "global", "list", "--json")
		if code != ExitOK {
			t.Fatalf("svc pkg global list = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		var packages []sdk.GlobalPackage
		decodeJSON(t, s, &packages)
		if len(packages) != 0 {
			t.Errorf("the document has %d packages, want none: %v", len(packages), packages)
		}
		if s.stderr.Len() != 0 {
			t.Errorf("a piped list wrote %q to stderr", s.stderr)
		}
	})

	t.Run("a terminal is told why", func(t *testing.T) {
		code, s := terminalRun(t, nil, "pkg", "global", "list")
		if code != ExitOK {
			t.Fatalf("svc pkg global list = exit %d, want %d", code, ExitOK)
		}
		want := catalogWord(t, "zh", "detail.globalPackagesEmpty", nil)
		if got := outLines(s); !slices.Equal(got, []string{want}) {
			t.Errorf("stdout = %q, want only the GUI's own empty sentence %q", got, want)
		}
		reason := catalogWord(t, "zh", "cli.noActiveVersion",
			map[string]string{"sdk": sdk.SdkDisplayName(sdk.NodeJS)})
		if !strings.Contains(s.stderr.String(), reason) {
			t.Errorf("stderr = %q, want it to name the missing active version", s.stderr)
		}
	})
}

func TestRegistrySetAppliesTheAliasItResolved(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	code, s := runHome(t, home, "pkg", "registry", "set", "china")
	if code != ExitOK {
		t.Fatalf("svc pkg registry set china = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if got, want := readMarker(t, marker), "config set registry "+pkgmgr.RegistryChina; got != want {
		t.Errorf("npm ran %q, want %q", got, want)
	}
	// The sentence quotes the URL that was written, not the alias that was
	// typed: that is what the GUI's toast does when a radio button is clicked.
	want := catalogWord(t, "zh", "detail.registrySwitchSuccess", map[string]string{"url": pkgmgr.RegistryChina})
	if got := outLines(s); !slices.Equal(got, []string{want}) {
		t.Errorf("stdout = %q, want only %q", got, want)
	}
}

func TestRegistrySetInJSONReportsTheNewState(t *testing.T) {
	home := t.TempDir()
	writeFakeNpm(t, seedNodeVersion(t, home), markerFile(t))

	code, s := runHome(t, home, "pkg", "registry", "set", pkgmgr.RegistryChina, "--json")
	if code != ExitOK {
		t.Fatalf("svc pkg registry set --json = exit %d; stderr:\n%s", code, s.stderr)
	}
	// The same document `pkg registry` prints, so a script can set and read
	// with one shape -- which is what the component does when it re-fetches
	// after a successful apply.
	var result RegistryResult
	decodeJSON(t, s, &result)
	if result != (RegistryResult{Registry: pkgmgr.RegistryChina, Mode: registryModeChina}) {
		t.Errorf("the document is %+v", result)
	}
}

func TestRegistrySetAcceptsAnyHttpURL(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	const url = "https://npm.example.com/"
	code, s := runHome(t, home, "pkg", "registry", "set", url, "--json")
	if code != ExitOK {
		t.Fatalf("svc pkg registry set %s = exit %d; stderr:\n%s", url, code, s.stderr)
	}
	if got, want := readMarker(t, marker), "config set registry "+url; got != want {
		t.Errorf("npm ran %q, want %q", got, want)
	}
	var result RegistryResult
	decodeJSON(t, s, &result)
	if result.Mode != registryModeCustom || result.Registry != url {
		t.Errorf("the document is %+v, want a custom registry at %s", result, url)
	}
}

func TestRegistrySetRefusesABlankArgument(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	code, s := runHome(t, home, "pkg", "registry", "set", "   ")
	if code != ExitUsage {
		t.Fatalf("a blank registry = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	if !strings.Contains(s.stderr.String(), errRegistryNoURL.Error()) {
		t.Errorf("stderr = %q, want it to name the aliases", s.stderr)
	}
	if got := readMarker(t, marker); got != "" {
		t.Errorf("npm ran %q; a blank registry must run nothing", got)
	}
}

// The scheme rule is the service's, and the exit code it maps to is the one a
// script needs: a URL svc will not use is not a transport failure to retry,
// but it is not a broken machine either.
func TestRegistrySetRefusesANonHttpScheme(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	code, s := runHome(t, home, "pkg", "registry", "set", "ftp://npm.example.com")
	if code != ExitNetwork {
		t.Fatalf("an ftp registry = exit %d, want %d; stderr:\n%s", code, ExitNetwork, s.stderr)
	}
	want := catalogWord(t, "zh", "errors.scheme-not-allowed", map[string]string{"scheme": "ftp"})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
	if got := readMarker(t, marker); got != "" {
		t.Errorf("npm ran %q; a refused registry must run nothing", got)
	}
}

// TestRegistrySetTakesTheOperationLock pins the claim the group's header
// makes: these commands write inside <svc>/nodejs/<version>/, which is the
// tree `svc uninstall` deletes and `svc switch` repoints, so they hold the
// same lock those do -- even though they bootstrap read-only.
func TestRegistrySetTakesTheOperationLock(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	held, err := svclock.Acquire(filepath.Join(home, ".svc"), nil)
	if err != nil {
		t.Fatalf("take the operation lock: %v", err)
	}
	defer held.Release()

	code, s := runHome(t, home, "--no-wait", "pkg", "registry", "set", registryAliasOfficial)
	if code != ExitBusy {
		t.Fatalf("a contended --no-wait set = exit %d, want %d; stderr:\n%s", code, ExitBusy, s.stderr)
	}
	if got := readMarker(t, marker); got != "" {
		t.Errorf("npm ran %q while another process held the lock", got)
	}
}

// --- pkg global -------------------------------------------------------------

func TestGlobalListParsesTheNpmReport(t *testing.T) {
	home := t.TempDir()
	writeFakeNpm(t, seedNodeVersion(t, home), markerFile(t))

	code, s := runHome(t, home, "pkg", "global", "list")
	if code != ExitOK {
		t.Fatalf("svc pkg global list = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	lines := outLines(s)
	if len(lines) != 3 {
		t.Fatalf("stdout has %d lines, want a header and two packages:\n%s", len(lines), s.stdout)
	}
	// The service sorts by name; the fake's JSON lists typescript first, so a
	// command that stopped sorting would show it here.
	if !strings.HasPrefix(lines[1], "npm") || !strings.HasPrefix(lines[2], "typescript") {
		t.Errorf("the rows are %q, want npm before typescript", lines[1:])
	}
	if row := tableRow(lines, "typescript"); !strings.Contains(row, "5.6.3") {
		t.Errorf("the typescript row = %q, want its version", row)
	}
}

func TestGlobalListInJSONIsTheServiceValueVerbatim(t *testing.T) {
	home := t.TempDir()
	writeFakeNpm(t, seedNodeVersion(t, home), markerFile(t))

	code, s := runHome(t, home, "pkg", "global", "list", "--json")
	if code != ExitOK {
		t.Fatalf("svc pkg global list --json = exit %d; stderr:\n%s", code, s.stderr)
	}
	var packages []sdk.GlobalPackage
	decodeJSON(t, s, &packages)
	want := []sdk.GlobalPackage{{Name: "npm", Version: npmVersion}, {Name: "typescript", Version: "5.6.3"}}
	if !slices.Equal(packages, want) {
		t.Errorf("the document is %v, want %v", packages, want)
	}
}

func TestGlobalInstallRunsNpmAndReportsTheGuiSentence(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	code, s := runHome(t, home, "pkg", "global", "install", "typescript")
	if code != ExitOK {
		t.Fatalf("svc pkg global install = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if got, want := readMarker(t, marker), "install -g typescript"; got != want {
		t.Errorf("npm ran %q, want %q", got, want)
	}
	want := catalogWord(t, "zh", "detail.gpInstallSuccess", map[string]string{"name": "typescript"})
	if got := outLines(s); !slices.Equal(got, []string{want}) {
		t.Errorf("stdout = %q, want only %q", got, want)
	}
}

func TestGlobalInstallAcceptsAScopedName(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	code, s := runHome(t, home, "pkg", "global", "install", "@scope/pkg", "--json")
	if code != ExitOK {
		t.Fatalf("a scoped name = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if got, want := readMarker(t, marker), "install -g @scope/pkg"; got != want {
		t.Errorf("npm ran %q, want %q", got, want)
	}
	var result PackageResult
	decodeJSON(t, s, &result)
	if result != (PackageResult{Action: actionInstall, Target: pkgTargetGlobal, Name: "@scope/pkg"}) {
		t.Errorf("the document is %+v", result)
	}
}

func TestGlobalInstallRefusesANameThatIsNotAPackageName(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	for _, name := range []string{"../evil", "a b", "typescript@5.6.3", ""} {
		t.Run(name, func(t *testing.T) {
			code, s := runHome(t, home, "pkg", "global", "install", name)
			if code != ExitUsage {
				t.Fatalf("installing %q = exit %d, want %d; stderr:\n%s", name, code, ExitUsage, s.stderr)
			}
			want := catalogWord(t, "zh", "errors.invalid-package-name", map[string]string{"name": name})
			if !strings.Contains(s.stderr.String(), want) {
				t.Errorf("stderr = %q, want %q", s.stderr, want)
			}
			if got := readMarker(t, marker); got != "" {
				t.Errorf("npm ran %q; a refused name must run nothing", got)
			}
		})
	}
}

func TestGlobalUpdateAsksForTheLatestVersion(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	code, s := runHome(t, home, "pkg", "global", "update", "typescript")
	if code != ExitOK {
		t.Fatalf("svc pkg global update = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if got, want := readMarker(t, marker), "install -g typescript@latest"; got != want {
		t.Errorf("npm ran %q, want %q", got, want)
	}
	// The GUI reports an update of a global package with the package-manager
	// sentence -- GlobalPackagesSection's handleUpdate reaches for
	// detail.pmUpdateSuccess -- and the CLI says the same thing rather than
	// inventing a nearer synonym.
	want := catalogWord(t, "zh", "detail.pmUpdateSuccess", map[string]string{"name": "typescript"})
	if got := outLines(s); !slices.Equal(got, []string{want}) {
		t.Errorf("stdout = %q, want only %q", got, want)
	}
}

func TestGlobalUninstallAsksWithTheGuiSentencesAndThenRemoves(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	code, s := interactiveRunHome(t, home, "y\n", "pkg", "global", "uninstall", "typescript")
	if code != ExitOK {
		t.Fatalf("svc pkg global uninstall = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if got, want := readMarker(t, marker), "uninstall -g typescript"; got != want {
		t.Errorf("npm ran %q, want %q", got, want)
	}
	// The question and its explanation are the modal's own two sentences, on
	// stderr where every other prompt in this CLI lives.
	asked := catalogWord(t, "zh", "detail.gpUninstallConfirm", map[string]string{"name": "typescript"})
	explained := catalogWord(t, "zh", "detail.gpUninstallConfirmDesc", map[string]string{"name": "typescript"})
	if !strings.Contains(s.stderr.String(), asked) || !strings.Contains(s.stderr.String(), explained) {
		t.Errorf("stderr = %q, want the question %q and its explanation", s.stderr, asked)
	}
	want := catalogWord(t, "zh", "detail.gpUninstallSuccess", map[string]string{"name": "typescript"})
	if got := outLines(s); !slices.Equal(got, []string{want}) {
		t.Errorf("stdout = %q, want only %q", got, want)
	}
}

func TestGlobalUninstallDeclinedRemovesNothing(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	code, s := interactiveRunHome(t, home, "n\n", "pkg", "global", "uninstall", "typescript")
	if code != ExitCanceled {
		t.Fatalf("a declined uninstall = exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
	}
	if got := readMarker(t, marker); got != "" {
		t.Errorf("npm ran %q after the user said no", got)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("a declined uninstall reported %q on stdout", s.stdout)
	}
}

func TestGlobalUninstallWithoutYesRefusesAPipe(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	code, s := runHome(t, home, "pkg", "global", "uninstall", "typescript")
	if code != ExitUsage {
		t.Fatalf("a piped uninstall = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	if got := readMarker(t, marker); got != "" {
		t.Errorf("npm ran %q; a pipe must not be guessed at", got)
	}
}

func TestGlobalUninstallWithYesDoesNotAsk(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	code, s := runHome(t, home, "pkg", "global", "uninstall", "typescript", "--yes")
	if code != ExitOK {
		t.Fatalf("svc pkg global uninstall --yes = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if got, want := readMarker(t, marker), "uninstall -g typescript"; got != want {
		t.Errorf("npm ran %q, want %q", got, want)
	}
	asked := catalogWord(t, "zh", "detail.gpUninstallConfirm", map[string]string{"name": "typescript"})
	if strings.Contains(s.stderr.String(), asked) {
		t.Errorf("stderr = %q, want no question after --yes", s.stderr)
	}
}

// TestGlobalUninstallOfNpmIsRefusedBeforeAsking is the reason the protected
// rule is an exported predicate rather than a string the CLI repeats: the
// refusal has to happen before the prompt, because a question whose only
// possible outcome is a failure trains users to answer without reading.
func TestGlobalUninstallOfNpmIsRefusedBeforeAsking(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	code, s := interactiveRunHome(t, home, "y\n", "pkg", "global", "uninstall", "npm")
	if code != ExitUsage {
		t.Fatalf("uninstalling npm = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	want := catalogWord(t, "zh", "errors.protected-package", map[string]string{"name": "npm"})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
	asked := catalogWord(t, "zh", "detail.gpUninstallConfirm", map[string]string{"name": "npm"})
	if strings.Contains(s.stderr.String(), asked) {
		t.Errorf("stderr = %q, want the refusal before any question", s.stderr)
	}
	if got := readMarker(t, marker); got != "" {
		t.Errorf("npm ran %q; a protected package must never reach npm", got)
	}
}

// A package that is not installed counts as removed. That is npm's own
// behaviour and the rule `svc logs rm` follows: a removal is safe to repeat,
// and a command that failed the second time would make every script check
// first.
func TestGlobalUninstallOfAnAbsentPackageStillSucceeds(t *testing.T) {
	home := t.TempDir()
	marker := markerFile(t)
	writeFakeNpm(t, seedNodeVersion(t, home), marker)

	code, s := runHome(t, home, "pkg", "global", "uninstall", "leftpad", "--yes")
	if code != ExitOK {
		t.Fatalf("uninstalling an absent package = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if got, want := readMarker(t, marker), "uninstall -g leftpad"; got != want {
		t.Errorf("npm ran %q, want %q", got, want)
	}
	want := catalogWord(t, "zh", "detail.gpUninstallSuccess", map[string]string{"name": "leftpad"})
	if got := outLines(s); !slices.Equal(got, []string{want}) {
		t.Errorf("stdout = %q, want only %q", got, want)
	}
}

func TestGlobalCommandsAreWiredIntoTheTree(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	root := r.newRootCommand()

	global, _, err := root.Find([]string{"pkg", "global"})
	if err != nil || global == nil || global.Name() != "global" {
		t.Fatalf("svc pkg global is missing: %v", err)
	}
	var names []string
	for _, command := range global.Commands() {
		names = append(names, command.Name())
	}
	slices.Sort(names)
	want := []string{"install", "list", "uninstall", "update"}
	if !slices.Equal(names, want) {
		t.Errorf("svc pkg global exposes %v, want %v", names, want)
	}
}
