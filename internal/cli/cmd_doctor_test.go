package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"svc/internal/sdk"
)

// `svc doctor` bootstraps at LevelWrite, because repairing is the point of it,
// so every command-level test here skips on Windows for the reason
// skipOnWindowsRegistryWrites gives. The two pure helpers -- the PATH membership
// rule and the label switches -- run everywhere, and the PATH one takes the OS
// as a parameter precisely so both platforms' rules are covered on any host.

// shimsOnPath is a seed step that puts the home's shims directory on PATH. It
// is the one check that reads the process environment rather than the
// filesystem, and tempHome points PATH at an empty directory, so without this
// the healthy case is not expressible at all. It must run inside a seed, after
// tempHome's own t.Setenv, or it is overwritten.
func shimsOnPath(t *testing.T, home string) {
	t.Helper()
	t.Setenv("PATH", filepath.Join(home, ".svc", "shims"))
}

// doctorResult decodes a doctor run's JSON document.
func doctorResult(t *testing.T, s *streams) DoctorResult {
	t.Helper()
	var result DoctorResult
	decodeJSON(t, s, &result)
	return result
}

// checkNamed finds one check in a result, failing the test when the report does
// not carry it. A missing check would otherwise show up as a zero-value struct
// with an empty name, which reads like "not broken".
func checkNamed(t *testing.T, result DoctorResult, name string) DoctorCheck {
	t.Helper()
	for _, check := range result.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("the report carries %v, want a %q check", result.Checks, name)
	return DoctorCheck{}
}

func TestPathHasEntry(t *testing.T) {
	cases := []struct {
		name     string
		dir      string
		pathList string
		goos     string
		want     bool
	}{
		{"a unix entry matches exactly", "/home/u/.svc/shims", "/usr/bin:/home/u/.svc/shims", "linux", true},
		{"a unix entry that is the whole PATH matches", "/home/u/.svc/shims", "/home/u/.svc/shims", "darwin", true},
		{"a unix prefix is not a match", "/home/u/.svc/shims", "/home/u/.svc/shims-extra:/usr/bin", "linux", false},
		{"unix is case sensitive", "/home/u/.svc/shims", "/home/u/.SVC/shims", "linux", false},
		{"a colon inside a windows path is not a separator", `C:\Users\u\.svc\shims`, `C:\Users\u\.svc\shims;C:\Windows`, "windows", true},
		{"windows matches case-insensitively", `C:\Users\u\.svc\shims`, `c:\users\u\.svc\SHIMS`, "windows", true},
		{"a windows trailing separator still matches", `C:\Users\u\.svc\shims`, `C:\Users\u\.svc\shims\;C:\Windows`, "windows", true},
		{"a unix trailing slash still matches", "/home/u/.svc/shims", "/usr/bin:/home/u/.svc/shims/", "linux", true},
		{"a quoted windows entry still matches", `C:\Users\u\.svc\shims`, `"C:\Users\u\.svc\shims";C:\Windows`, "windows", true},
		{"an empty entry is skipped", "/home/u/.svc/shims", "/usr/bin::/home/u/.svc/shims", "linux", true},
		{"an empty PATH has nothing in it", "/home/u/.svc/shims", "", "linux", false},
		{"an empty directory never matches", "", "/usr/bin", "linux", false},
		{"a directory that is not there is not there", "/home/u/.svc/shims", "/usr/bin:/bin", "darwin", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pathHasEntry(c.dir, c.pathList, c.goos); got != c.want {
				t.Errorf("pathHasEntry(%q, %q, %q) = %v, want %v", c.dir, c.pathList, c.goos, got, c.want)
			}
		})
	}
}

func TestNormalizePathEntry(t *testing.T) {
	cases := []struct {
		entry   string
		windows bool
		want    string
	}{
		{"  /a/b  ", false, "/a/b"},
		{"/a/b/", false, "/a/b"},
		{"/a/b//", false, "/a/b"},
		{`C:\a\b\`, true, `C:\a\b`},
		// A backslash is an ordinary character on Unix, so it must survive:
		// trimming it would make a legitimate (if strange) entry vanish.
		{`/a\b\`, false, `/a\b\`},
		{`"C:\a\b"`, true, `C:\a\b`},
		{"", true, ""},
		{"/", false, "/"},
	}
	for _, c := range cases {
		t.Run(c.entry, func(t *testing.T) {
			if got := normalizePathEntry(c.entry, c.windows); got != c.want {
				t.Errorf("normalizePathEntry(%q, %v) = %q, want %q", c.entry, c.windows, got, c.want)
			}
		})
	}
}

// TestDoctorReportsEveryCheckInOrder pins the report's shape: five checks, in
// the order the failure cascade runs in, with the machine tokens a script keys
// on. The names are part of the --json contract, so they are asserted literally
// rather than read back from doctorChecks.
func TestDoctorReportsEveryCheckInOrder(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	run := sharedHome(t, nil)
	code, s := run("--json", "doctor")
	if code != ExitError {
		t.Fatalf("a home whose shims are not on PATH = exit %d, want %d; stderr:\n%s", code, ExitError, s.stderr)
	}
	result := doctorResult(t, s)
	got := make([]string, 0, len(result.Checks))
	for _, check := range result.Checks {
		got = append(got, check.Name)
		if check.Status != doctorOK && check.Status != doctorRepaired && check.Status != doctorBroken {
			t.Errorf("the %s check reports the unknown status %q", check.Name, check.Status)
		}
		if check.Status == doctorOK && check.Detail != "" {
			t.Errorf("a healthy %s check explains itself: %q", check.Name, check.Detail)
		}
		if check.Status != doctorOK && check.Detail == "" {
			t.Errorf("the %s check is %q and says nothing about it", check.Name, check.Status)
		}
		if check.Status == doctorRepaired {
			if want := catalogWord(t, "zh", "cli.doctorShimReinstalled", nil); check.Detail != want {
				t.Errorf("the repaired detail is %q, want %q", check.Detail, want)
			}
		}
	}
	want := []string{checkShimBinary, checkShimsOnPath, checkActiveVersions, checkShimsConfig, checkSystemConflicts}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the report is [%s], want [%s]", strings.Join(got, " "), strings.Join(want, " "))
	}
	if got := jsonKeysOf(t, result); len(got) != 3 {
		t.Errorf("the document carries %v, want checks, repaired and broken", got)
	}
	if result.Broken != 1 || result.Repaired != 1 {
		t.Errorf("the counts are repaired=%d broken=%d, want 1 and 1 -- the startup installs the shim binary: %+v",
			result.Repaired, result.Broken, result.Checks)
	}
}

// TestDoctorRepairsTheShimBinaryAndSaysSo covers the one check that can prove
// it changed something: the shim binary is read before the write-level startup
// runs, so a missing binary that the startup installed is reported as repaired
// on the first run and as ok on the second.
func TestDoctorRepairsTheShimBinaryAndSaysSo(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	run := sharedHome(t, func(h string) {
		home = h
		shimsOnPath(t, h)
	})

	name := "svc-shim"
	if runtime.GOOS == "windows" {
		name = "svc-shim.exe"
	}
	shimBinary := filepath.Join(home, ".svc", "shims", name)
	if _, err := os.Stat(shimBinary); !os.IsNotExist(err) {
		t.Fatalf("the shim binary already exists before any run (err %v)", err)
	}

	code, s := run("--json", "doctor")
	if code != ExitOK {
		t.Fatalf("the first run = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	result := doctorResult(t, s)
	if got := checkNamed(t, result, checkShimBinary); got.Status != doctorRepaired {
		t.Errorf("the shim binary check is %+v, want repaired", got)
	}
	if result.Repaired != 1 || result.Broken != 0 {
		t.Errorf("the counts are repaired=%d broken=%d, want 1 and 0", result.Repaired, result.Broken)
	}
	if _, err := os.Stat(shimBinary); err != nil {
		t.Errorf("the startup did not install the shim binary: %v", err)
	}

	code, s = run("--json", "doctor")
	if code != ExitOK {
		t.Fatalf("the second run = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	result = doctorResult(t, s)
	if got := checkNamed(t, result, checkShimBinary); got.Status != doctorOK {
		t.Errorf("the shim binary check is %+v on the second run, want ok", got)
	}
	if result.Repaired != 0 {
		t.Errorf("the second run repaired %d checks, want nothing left to repair", result.Repaired)
	}
}

// TestDoctorReportsAHealthyHome is the exit-0 half of the contract, and the
// text-mode counterpart: a table with the catalog's own column headings and the
// summary sentence underneath it.
func TestDoctorReportsAHealthyHome(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	run := sharedHome(t, func(home string) { shimsOnPath(t, home) })

	code, s := run("doctor")
	if code != ExitOK {
		t.Fatalf("a healthy home = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	lines := outLines(s)
	if len(lines) == 0 {
		t.Fatal("stdout is empty")
	}
	if want := catalogWord(t, "zh", "cli.doctorAllOk", nil); lines[len(lines)-1] != want {
		t.Errorf("the last line is %q, want %q", lines[len(lines)-1], want)
	}
	for _, key := range []string{"cli.columnCheck", "cli.columnState", "cli.columnDetail"} {
		if want := catalogWord(t, "zh", key, nil); !strings.Contains(lines[0], want) {
			t.Errorf("the header %q does not carry %q (%s)", lines[0], want, key)
		}
	}
	// Every check name is rendered from the catalog, so a row whose label is
	// still the machine token is a case the label switch forgot -- and a label
	// that resolves to nothing prints as "cli.checkShimBinary" with no test
	// failing anywhere else.
	for _, c := range []struct{ token, key string }{
		{checkShimBinary, "cli.checkShimBinary"},
		{checkShimsOnPath, "cli.checkShimsOnPath"},
		{checkActiveVersions, "cli.checkActiveVersions"},
		{checkShimsConfig, "cli.checkShimsConfig"},
		{checkSystemConflicts, "cli.checkSystemConflicts"},
	} {
		if strings.Contains(s.stdout.String(), c.token) {
			t.Errorf("stdout carries the raw token %q instead of its label:\n%s", c.token, s.stdout)
		}
		if want := catalogWord(t, "zh", c.key, nil); !strings.Contains(s.stdout.String(), want) {
			t.Errorf("stdout does not carry %q (%s):\n%s", want, c.key, s.stdout)
		}
	}
	if want := catalogWord(t, "zh", "cli.stateOk", nil); !strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout does not carry %q (cli.stateOk):\n%s", want, s.stdout)
	}
}

// TestDoctorReportsABrokenActiveVersion covers the state the GUI's home page
// marks with detail.needsSwitch: config.json names a version whose directory is
// gone, so the shim resolves to nothing.
func TestDoctorReportsABrokenActiveVersion(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	run := sharedHome(t, func(home string) {
		setActiveVersion(t, home, goSdk, "1.22.0")
		shimsOnPath(t, home)
	})

	code, s := run("--json", "doctor")
	if code != ExitError {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitError, s.stderr)
	}
	result := doctorResult(t, s)
	check := checkNamed(t, result, checkActiveVersions)
	if check.Status != doctorBroken {
		t.Fatalf("the active-versions check is %+v, want broken", check)
	}
	// The detail names the SDK and the version, because "something is wrong"
	// is not an actionable sentence, and carries the GUI's own remedy.
	for _, want := range []string{goSdk + " 1.22.0", catalogWord(t, "zh", "detail.needsSwitch", nil)} {
		if !strings.Contains(check.Detail, want) {
			t.Errorf("the detail is %q, want it to carry %q", check.Detail, want)
		}
	}
	// An active version with no directory also has no shims.json entry, so the
	// two checks fire together.
	if got := checkNamed(t, result, checkShimsConfig); got.Status != doctorBroken {
		t.Errorf("the shims-config check is %+v, want broken too", got)
	}
	// The total is NOT asserted here, and that is not an oversight. An active
	// SDK makes checkSystemConflicts ask pathmgr about the machine, which on
	// Unix reads /etc/profile, /etc/paths and friends -- files the test does not
	// own and cannot fake. A GitHub Linux runner has Go in one of them, so this
	// home reports three broken checks there and two on macOS. Asserting a count
	// would pin the runner's /etc, not svc's behaviour; the summary sentence
	// itself is asserted in TestDoctorReportsShimsNotOnPath, where nothing is
	// active and the conflict check has no SDK to ask about.
	if result.Broken < 2 {
		t.Errorf("broken = %d, want at least the two checks above: %+v", result.Broken, result.Checks)
	}
	if code != ExitError {
		t.Errorf("a broken report exits %d, want %d", code, ExitError)
	}
}

// TestDoctorReportsAMissingShimsConfigEntry covers the other half of a shim
// that resolves to nothing: the version directory is there, but shims.json has
// no entry for the SDK, so the shim binary cannot find its bin directories.
func TestDoctorReportsAMissingShimsConfigEntry(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	run := sharedHome(t, func(home string) {
		seedGoVersion(home, "1.22.0")
		setActiveVersion(t, home, goSdk, "1.22.0")
		shimsOnPath(t, home)
	})

	code, s := run("--json", "doctor")
	if code != ExitError {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitError, s.stderr)
	}
	result := doctorResult(t, s)
	if got := checkNamed(t, result, checkActiveVersions); got.Status != doctorOK {
		t.Errorf("the active-versions check is %+v, want ok -- the directory is there", got)
	}
	check := checkNamed(t, result, checkShimsConfig)
	if check.Status != doctorBroken {
		t.Fatalf("the shims-config check is %+v, want broken", check)
	}
	if !strings.Contains(check.Detail, goSdk) {
		t.Errorf("the detail %q does not name the SDK", check.Detail)
	}
	if want := catalogWord(t, "zh", "cli.doctorRunSwitch", nil); !strings.Contains(check.Detail, want) {
		t.Errorf("the detail is %q, want the switch remedy %q", check.Detail, want)
	}
}

// TestDoctorReportsShimsNotOnPath pins the check whose remedy is not svc's to
// apply: the directory exists, the rc line exists, and the terminal the user is
// in was opened before either did.
func TestDoctorReportsShimsNotOnPath(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	run := sharedHome(t, func(h string) { home = h })

	code, s := run("--json", "doctor")
	if code != ExitError {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitError, s.stderr)
	}
	result := doctorResult(t, s)
	check := checkNamed(t, result, checkShimsOnPath)
	if check.Status != doctorBroken {
		t.Fatalf("the shims-on-path check is %+v, want broken", check)
	}
	if want := catalogWord(t, "zh", "detail.reopenTerminalHint", nil); check.Detail != want {
		t.Errorf("the detail is %q, want %q", check.Detail, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".svc", "shims")); err != nil {
		t.Errorf("the shims directory does not exist, so this run measured the wrong thing: %v", err)
	}
	// Nothing is active in this home, so the three per-SDK checks -- including
	// the one that asks pathmgr about the machine's own /etc -- have no SDK to
	// ask about, and the counts are therefore the same on every platform. This
	// is the run that pins the summary sentence the failed run exits with: it
	// is rendered from the catalog rather than assembled in English.
	if result.Broken != 1 || result.Repaired != 1 {
		t.Errorf("the counts are repaired=%d broken=%d, want 1 and 1: %+v", result.Repaired, result.Broken, result.Checks)
	}
	want := catalogWord(t, "zh", "cli.doctorIssues", map[string]string{"broken": "1", "repaired": "1"})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
}

// TestDoctorIgnoresAnSdkWithNoActiveVersion pins the scope of the three
// per-SDK checks: an SDK the user never activated has no shims and no conflicts
// to report, and counting it would make doctor fail on every fresh install.
func TestDoctorIgnoresAnSdkWithNoActiveVersion(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	run := sharedHome(t, func(home string) {
		// A version directory that is NOT active: a leftover an uninstall or a
		// manual delete left behind. It must not be reported as a broken active
		// version, because nothing claims it.
		if err := os.MkdirAll(filepath.Join(home, ".svc", string(sdk.Golang), "1.21.0"), 0755); err != nil {
			t.Fatalf("mkdir the inactive version: %v", err)
		}
		shimsOnPath(t, home)
	})

	code, s := run("--json", "doctor")
	if code != ExitOK {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	result := doctorResult(t, s)
	for _, name := range []string{checkActiveVersions, checkShimsConfig, checkSystemConflicts} {
		if got := checkNamed(t, result, name); got.Status != doctorOK {
			t.Errorf("the %s check is %+v, want ok for a home with nothing active", name, got)
		}
	}
}

// TestDoctorLabelsResolveForEveryTokenAndState guards the two label switches
// the way output_guard_test.go guards the catalog: a token the switch forgot
// would print as the machine name, and a state it forgot would print as broken.
func TestDoctorLabelsResolveForEveryTokenAndState(t *testing.T) {
	r := newRunner(t, newStreams(), globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolve options: %v", err)
	}
	for _, name := range []string{
		checkShimBinary, checkShimsOnPath, checkActiveVersions, checkShimsConfig, checkSystemConflicts,
	} {
		label := r.doctorCheckLabel(name)
		if label == name {
			t.Errorf("doctorCheckLabel(%q) returned the token itself; the switch is missing a case", name)
		}
		if !strings.HasPrefix(label, "cli.") {
			continue
		}
		t.Errorf("doctorCheckLabel(%q) = %q, an unresolved catalog key", name, label)
	}
	if got := r.doctorCheckLabel("not-a-check"); got != "not-a-check" {
		t.Errorf("an unknown token renders as %q, want it passed through", got)
	}
	for _, state := range []string{doctorOK, doctorRepaired, doctorBroken} {
		label := r.doctorStateLabel(state)
		if label == state || strings.HasPrefix(label, "cli.") {
			t.Errorf("doctorStateLabel(%q) = %q, want a resolved sentence", state, label)
		}
	}
	// An unrecognised state falls through to the one label that must not be
	// mistaken for good news.
	if got, want := r.doctorStateLabel("something-else"), r.doctorStateLabel(doctorBroken); got != want {
		t.Errorf("an unknown state renders as %q, want %q", got, want)
	}
}
