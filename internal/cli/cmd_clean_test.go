package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"svc/internal/logmgr"
	"svc/internal/svclock"
)

// The clean family deletes, so its tests are mostly about what survives: the
// active version, a declined deletion, a name that is not a bare file name,
// and the source a `clean tmp` was never meant to touch.
//
// Two of the four targets need no service graph at all. `clean logs` and
// `logs rm` talk to the logger singleton, which TestMain claims once for the
// whole binary, so those tests run on every platform and against the shared log
// directory -- which is why every file they create has a date in it that cannot
// occur, and every one of them is registered for cleanup.
//
// `clean inactive` bootstraps at LevelWrite, because removing the last version
// of an SDK tears down its shims and that edits the shell rc or the registry
// PATH. Those tests skip on Windows for the reason skipOnWindowsRegistryWrites
// gives. `clean tmp` bootstraps read-only and therefore does not skip.

// seedTmpCache fills the SVC temp directory with what a download and an
// interrupted extraction leave behind, and returns the number of bytes and the
// number of top-level entries it wrote. The extraction tree is a second entry
// holding a third file, so the count and the size deliberately disagree about
// what "how much" means -- a cache cleaner that reported one when it meant the
// other would still look plausible.
func seedTmpCache(t *testing.T, home string) (bytes int64, entries int) {
	t.Helper()
	dir := filepath.Join(home, ".svc", "tmp")
	tree := filepath.Join(dir, "import_go1.22.0", "bin")
	if err := os.MkdirAll(tree, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", tree, err)
	}
	files := map[string]int{
		filepath.Join(dir, "go1.22.0.tar.gz"):            100,
		filepath.Join(dir, "node-v20.11.0-linux-x64.xz"): 200,
		filepath.Join(tree, "go"):                        1,
	}
	var total int64
	for path, size := range files {
		if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		total += int64(size)
	}
	return total, 3
}

// logName builds a log file name that cannot collide with the logger's own
// date-named files or with another test's: the year cannot occur, and the day
// is what makes one caller's name distinct from the next one's.
func logName(day int) string {
	return fmt.Sprintf("svc-1900-01-%02d.log", day)
}

// writeLogFile drops one extra file into the log directory the test binary
// shares and returns its path. It is registered for cleanup so a test that
// fails before deleting it does not leave the next test counting a stray.
func writeLogFile(t *testing.T, name string) string {
	t.Helper()
	dir := logmgr.GetLogDir()
	if dir == "" {
		t.Fatal("the logger was never initialised, so there is no log directory to test against")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(name+" says hello\n"), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

// logNames lists the log files that exist right now, so a test can assert that
// a declined deletion changed nothing without hardcoding the logger's own file.
func logNames(t *testing.T) []string {
	t.Helper()
	files, err := logmgr.GetLogFiles()
	if err != nil {
		t.Fatalf("list the log files: %v", err)
	}
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, file.Name)
	}
	slices.Sort(names)
	return names
}

// --- pure helpers -----------------------------------------------------------

func TestCountDirEntries(t *testing.T) {
	dir := t.TempDir()
	if got := countDirEntries(dir); got != 0 {
		t.Errorf("countDirEntries of an empty directory = %d, want 0", got)
	}
	// A directory that is not there is "nothing to delete", not a failure: the
	// count is a report, and the service that deletes produces its own error.
	if got := countDirEntries(filepath.Join(dir, "nope")); got != 0 {
		t.Errorf("countDirEntries of a missing directory = %d, want 0", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "b"), 0755); err != nil {
		t.Fatal(err)
	}
	// Both entries, not their contents: this is the count of things the cleaner
	// is about to remove at the top level.
	if got := countDirEntries(dir); got != 2 {
		t.Errorf("countDirEntries = %d, want 2", got)
	}
}

func TestCountInactiveVersions(t *testing.T) {
	dir := t.TempDir()
	for _, version := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		if err := os.MkdirAll(filepath.Join(dir, version), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// A file among the version directories is not a version, and
	// CleanInactiveVersions skips it too.
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		active string
		want   int
	}{
		{"the active version is kept", "1.1.0", 2},
		{"the oldest version is kept", "1.0.0", 2},
		{"an active version that is not on disk keeps nothing", "9.9.9", 3},
		// No active version at all means every directory is inactive, which is
		// exactly what storage.Manager.CleanInactiveVersions does: it compares
		// each name against the empty string and no directory is named that.
		{"no active version makes every directory inactive", "", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := countInactiveVersions(dir, c.active)
			if err != nil {
				t.Fatalf("countInactiveVersions: %v", err)
			}
			if got != c.want {
				t.Errorf("countInactiveVersions(dir, %q) = %d, want %d", c.active, got, c.want)
			}
		})
	}

	t.Run("an unreadable directory is an error, not a zero", func(t *testing.T) {
		// Unlike countDirEntries this one reports failure: the number goes into
		// a question the user answers, and a wrong zero would turn "delete
		// nothing" into an approved no-op that hides a broken directory.
		if _, err := countInactiveVersions(filepath.Join(dir, "nope"), ""); err == nil {
			t.Error("countInactiveVersions of a missing directory reported a count instead of an error")
		}
	})

	t.Run("an empty directory has nothing to delete", func(t *testing.T) {
		got, err := countInactiveVersions(t.TempDir(), "1.0.0")
		if err != nil {
			t.Fatalf("countInactiveVersions: %v", err)
		}
		if got != 0 {
			t.Errorf("countInactiveVersions of an empty directory = %d, want 0", got)
		}
	})
}

func TestCleanResultShape(t *testing.T) {
	t.Run("the target tokens are the documented literals", func(t *testing.T) {
		targets := []string{cleanTargetTmp, cleanTargetLogs, cleanTargetInactive, cleanTargetLogFiles}
		want := []string{"tmp", "logs", "inactive", "log-files"}
		if !slices.Equal(targets, want) {
			t.Errorf("the target tokens changed: %v, want %v -- they are a --json contract", targets, want)
		}
	})
	cases := []struct {
		name   string
		result CleanResult
		want   []string
	}{
		{"tmp carries the bytes it freed", CleanResult{Target: cleanTargetTmp, Removed: 3, FreedBytes: 301},
			[]string{"freedBytes", "removed", "target"}},
		{"inactive carries the SDK it was scoped to", CleanResult{Target: cleanTargetInactive, SdkType: goSdk, Removed: 2},
			[]string{"removed", "sdkType", "target"}},
		{"logs carries only what every target carries", CleanResult{Target: cleanTargetLogs, Removed: 4},
			[]string{"removed", "target"}},
		{"a no-op omits the keys it has nothing to say about", CleanResult{Target: cleanTargetLogs},
			[]string{"removed", "target"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := jsonKeysOf(t, c.result); !slices.Equal(got, c.want) {
				t.Errorf("CleanResult marshals to %v, want %v", got, c.want)
			}
		})
	}
}

// TestReportCleanRoutesEachChannel is the clean family's half of the output
// contract: the document to stdout in JSON mode and nothing else, the outcome
// sentences to stdout in the text modes.
func TestReportCleanRoutesEachChannel(t *testing.T) {
	report := CleanReport{
		Result:  CleanResult{Target: cleanTargetTmp, Removed: 3, FreedBytes: 301},
		Summary: "cleaned",
		Detail:  "freed 301 B",
	}
	cases := []struct {
		name       string
		flags      globalFlags
		json       bool
		wantStdout []string
	}{
		{"a pipe gets both result lines", globalFlags{}, false, []string{"cleaned", "freed 301 B"}},
		{"--json gets one document", globalFlags{json: true}, true, nil},
		{"--quiet still gets the results", globalFlags{quiet: true}, false, []string{"cleaned", "freed 301 B"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStreams()
			r := newRunner(t, s, c.flags, tempHome(t, nil))
			if err := r.resolveOptions(); err != nil {
				t.Fatalf("resolveOptions: %v", err)
			}
			if err := r.reportClean(report); err != nil {
				t.Fatalf("reportClean: %v", err)
			}
			if c.json {
				var decoded CleanResult
				decodeJSON(t, s, &decoded)
				if decoded != report.Result {
					t.Errorf("the document is %+v, want %+v", decoded, report.Result)
				}
				return
			}
			if got := outLines(s); !slices.Equal(got, c.wantStdout) {
				t.Errorf("stdout = %q, want %q", got, c.wantStdout)
			}
		})
	}
}

// TestReportCleanOfANoOpStaysQuiet pins the empty-detail case: `clean inactive`
// with nothing to delete prints its one line and no blank, so a script never
// has to filter an empty record out of the result.
func TestReportCleanOfANoOpStaysQuiet(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}
	report := CleanReport{
		Result:  CleanResult{Target: cleanTargetInactive, SdkType: goSdk},
		Summary: "removed nothing",
	}
	if err := r.reportClean(report); err != nil {
		t.Fatalf("reportClean: %v", err)
	}
	if got := outLines(s); !slices.Equal(got, []string{"removed nothing"}) {
		t.Errorf("stdout = %q, want exactly the summary", got)
	}
}

// --- clean tmp --------------------------------------------------------------

func TestCleanTmpEmptiesTheCache(t *testing.T) {
	home := t.TempDir()
	freed, entries := seedTmpCache(t, home)

	code, s := runHome(t, home, "--json", "clean", "tmp")
	if code != ExitOK {
		t.Fatalf("svc clean tmp = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result CleanResult
	decodeJSON(t, s, &result)
	want := CleanResult{Target: cleanTargetTmp, Removed: entries, FreedBytes: freed}
	if result != want {
		t.Errorf("the document is %+v, want %+v", result, want)
	}
	if got := countDirEntries(filepath.Join(home, ".svc", "tmp")); got != 0 {
		t.Errorf("%d entries survived the clean", got)
	}
	// The cache is the only thing that went: the SDK directories next to it are
	// what a user would notice first if this command overreached.
	if _, err := os.Stat(filepath.Join(home, ".svc", goSdk)); err != nil {
		t.Errorf("clean tmp took the SDK directory with it: %v", err)
	}
}

func TestCleanTmpSaysHowMuchWent(t *testing.T) {
	home := t.TempDir()
	freed, _ := seedTmpCache(t, home)

	code, s := runHome(t, home, "clean", "tmp")
	if code != ExitOK {
		t.Fatalf("svc clean tmp = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	lines := outLines(s)
	want := []string{
		catalogWord(t, "zh", "settings.cleanSuccess", nil),
		catalogWord(t, "zh", "cli.freedBytes", map[string]string{"size": FormatBytes(freed)}),
	}
	if !slices.Equal(lines, want) {
		t.Errorf("stdout = %q, want %q", lines, want)
	}
}

// TestCleanTmpNeedsNoAnswer is the GUI-parity half: SettingsPage's
// handleCleanTmpCache asks nothing, so neither does this, and a pipe with no
// --yes still succeeds. A cache of leftover downloads is not a decision.
func TestCleanTmpNeedsNoAnswer(t *testing.T) {
	home := t.TempDir()
	seedTmpCache(t, home)
	if code, s := runHome(t, home, "clean", "tmp"); code != ExitOK {
		t.Fatalf("svc clean tmp off a pipe without --yes = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
}

func TestCleanTmpOnAnEmptyCacheIsANoOp(t *testing.T) {
	home := t.TempDir()
	code, s := runHome(t, home, "--json", "clean", "tmp")
	if code != ExitOK {
		t.Fatalf("svc clean tmp on an empty cache = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result CleanResult
	decodeJSON(t, s, &result)
	// freedBytes is omitted rather than zero, so a consumer can tell "nothing
	// was there" from "something was there and it was empty".
	if got := jsonKeysOf(t, result); slices.Contains(got, "freedBytes") {
		t.Errorf("an empty cache reported freedBytes: %v", got)
	}
	if result.Removed != 0 || result.Target != cleanTargetTmp {
		t.Errorf("the document is %+v, want an empty tmp clean", result)
	}
}

// TestCleanTmpFailsFastOnAContendedLockWithNoWait is the reason `clean tmp`
// takes the operation lock even though it bootstraps read-only: an install in
// flight keeps its download in this directory, and emptying it underneath that
// install is exactly the race the lock exists to prevent.
func TestCleanTmpFailsFastOnAContendedLockWithNoWait(t *testing.T) {
	home := t.TempDir()
	_, entries := seedTmpCache(t, home)
	held, err := svclock.Acquire(filepath.Join(home, ".svc"), nil)
	if err != nil {
		t.Fatalf("take the operation lock: %v", err)
	}
	defer held.Release()

	code, s := runHome(t, home, "--no-wait", "clean", "tmp")
	if code != ExitBusy {
		t.Fatalf("a contended --no-wait clean = exit %d, want %d; stderr:\n%s", code, ExitBusy, s.stderr)
	}
	if want := catalogWord(t, "zh", "errors.operation-in-progress", nil); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
	// Nothing was deleted while another process held the lock.
	if got := countDirEntries(filepath.Join(home, ".svc", "tmp")); got != entries {
		t.Errorf("%d of %d entries survived; a refused clean must delete nothing", got, entries)
	}
}

func TestCleanTmpUsage(t *testing.T) {
	code, s := pipedRun(t, nil, "clean", "tmp", "extra")
	if code != ExitUsage {
		t.Fatalf("svc clean tmp extra = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("a usage failure wrote to the result stream:\n%s", s.stdout)
	}
}

// TestCleanParentListsItsTargets pins the parent's behaviour, which is the one
// `logs`, `settings` and `endpoints` already have: with no subcommand -- or with
// one it does not recognise -- `svc clean` prints its help on stdout and exits 0,
// because the user asked for the command and got an answer rather than a
// failure. Nothing is deleted either way, which is the part that matters here.
func TestCleanParentListsItsTargets(t *testing.T) {
	for _, args := range [][]string{{"clean"}, {"clean", "nope"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, s := pipedRun(t, nil, args...)
			if code != ExitOK {
				t.Fatalf("svc %s = exit %d, want %d", strings.Join(args, " "), code, ExitOK)
			}
			if !strings.Contains(s.stdout.String(), "Available Commands:") {
				t.Errorf("stdout = %q, want the subcommand list", s.stdout)
			}
			for _, target := range []string{cleanTargetTmp, cleanTargetLogs, cleanTargetInactive} {
				if !strings.Contains(s.stdout.String(), target) {
					t.Errorf("stdout does not list the %q target:\n%s", target, s.stdout)
				}
			}
			if s.stderr.Len() != 0 {
				t.Errorf("stderr = %q, want it empty for help", s.stderr)
			}
		})
	}
}

// --- clean logs -------------------------------------------------------------

func TestCleanLogsDeletesEveryLogFile(t *testing.T) {
	writeLogFile(t, logName(1))
	writeLogFile(t, logName(2))
	before := logNames(t)
	if len(before) < 3 {
		t.Fatalf("the seeded log files are not all there: %v", before)
	}

	code, s := pipedRun(t, nil, "--json", "clean", "logs", "--yes")
	if code != ExitOK {
		t.Fatalf("svc clean logs = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result CleanResult
	decodeJSON(t, s, &result)
	if result.Target != cleanTargetLogs || result.Removed != len(before) {
		t.Errorf("the document is %+v, want target %q and %d removed", result, cleanTargetLogs, len(before))
	}

	after := logNames(t)
	// Today's log is back and it is the only one: a svc that cannot log is
	// worse than a svc with an empty log, which is why CleanLogs reopens rather
	// than merely deletes.
	if len(after) != 1 {
		t.Errorf("clean logs left %v, want the single file it reopened", after)
	}
	for _, name := range []string{logName(1), logName(2)} {
		if slices.Contains(after, name) {
			t.Errorf("%s survived clean logs", name)
		}
	}
}

func TestCleanLogsSaysWhatItDid(t *testing.T) {
	writeLogFile(t, logName(3))
	code, s := pipedRun(t, nil, "clean", "logs", "--yes")
	if code != ExitOK {
		t.Fatalf("svc clean logs = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if want := catalogWord(t, "zh", "logs.cleanSuccess", nil); !strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", s.stdout, want)
	}
}

func TestCleanLogsAsksHowManyItWillDelete(t *testing.T) {
	writeLogFile(t, logName(4))
	before := logNames(t)

	code, s := interactiveRun(t, "n\n", nil, "clean", "logs")
	if code != ExitCanceled {
		t.Fatalf("a declined clean logs = exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
	}
	want := catalogWord(t, "zh", "cli.cleanLogsConfirm", map[string]string{"count": strconv.Itoa(len(before))})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want it to ask %q", s.stderr, want)
	}
	if got := logNames(t); !slices.Equal(got, before) {
		t.Errorf("a declined clean changed the log directory: %v -> %v", before, got)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("a declined clean wrote to the result stream:\n%s", s.stdout)
	}
}

func TestCleanLogsOffAPipeNeedsYes(t *testing.T) {
	code, s := pipedRun(t, nil, "clean", "logs")
	if code != ExitUsage {
		t.Fatalf("svc clean logs off a pipe without --yes = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
}

func TestCleanLogsUsage(t *testing.T) {
	if code, _ := pipedRun(t, nil, "clean", "logs", "extra"); code != ExitUsage {
		t.Errorf("svc clean logs extra = exit %d, want %d", code, ExitUsage)
	}
}

// --- clean inactive ---------------------------------------------------------

// seedSwitchedGoHome lays down three Go versions and makes the middle one
// active through `svc switch`, so a `clean inactive` test starts from the state
// a user is actually in rather than from a hand-written config.json.
func seedSwitchedGoHome(t *testing.T) (home, active string) {
	t.Helper()
	skipOnWindowsRegistryWrites(t)
	home = t.TempDir()
	active = "1.23.0"
	seedGoVersion(home, "1.22.0", active, "1.24.0")
	if code, s := runHome(t, home, "switch", goSdk, active, "--yes"); code != ExitOK {
		t.Fatalf("the switch that sets this test up = exit %d; stderr:\n%s", code, s.stderr)
	}
	return home, active
}

// installedGoVersions lists what is left under the Go directory.
func installedGoVersions(t *testing.T, home string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(home, ".svc", goSdk))
	if err != nil {
		t.Fatalf("read the Go directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

func TestCleanInactiveKeepsTheActiveVersion(t *testing.T) {
	home, active := seedSwitchedGoHome(t)

	code, s := runHome(t, home, "clean", "inactive", goSdk, "--yes")
	if code != ExitOK {
		t.Fatalf("svc clean inactive = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if want := catalogWord(t, "zh", "cli.removedVersions", map[string]string{"count": "2"}); !strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", s.stdout, want)
	}
	if got, want := installedGoVersions(t, home), []string{active}; !slices.Equal(got, want) {
		t.Errorf("the Go directory holds %v, want exactly %v", got, want)
	}
	// The active version still resolves, which is the whole promise the
	// confirmation makes.
	if code, s := runHome(t, home, "current", goSdk); code != ExitOK || strings.TrimSpace(s.stdout.String()) != active {
		t.Errorf("svc current %s = exit %d %q, want %q", goSdk, code, s.stdout, active)
	}
}

func TestCleanInactiveReportsItsResultAsJSON(t *testing.T) {
	home, _ := seedSwitchedGoHome(t)

	code, s := runHome(t, home, "--json", "clean", "inactive", goSdk, "--yes")
	if code != ExitOK {
		t.Fatalf("svc --json clean inactive = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result CleanResult
	decodeJSON(t, s, &result)
	want := CleanResult{Target: cleanTargetInactive, SdkType: goSdk, Removed: 2}
	if result != want {
		t.Errorf("the document is %+v, want %+v", result, want)
	}
}

func TestCleanInactiveQuotesTheCountBeforeAsking(t *testing.T) {
	home, _ := seedSwitchedGoHome(t)

	code, s := interactiveRunHome(t, home, "n\n", "clean", "inactive", goSdk)
	if code != ExitCanceled {
		t.Fatalf("a declined clean inactive = exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
	}
	want := catalogWord(t, "zh", "cli.cleanInactiveConfirm", map[string]string{
		"sdk":   goDisplayName(),
		"count": "2",
	})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want it to ask %q", s.stderr, want)
	}
	if got, want := installedGoVersions(t, home), []string{"1.22.0", "1.23.0", "1.24.0"}; !slices.Equal(got, want) {
		t.Errorf("a declined clean left %v, want %v", got, want)
	}
}

// TestCleanInactiveWithNothingToDeleteNeedsNoAnswer pins the no-op rule: when
// there is nothing to remove there is nothing to approve, so the command
// reports zero and exits 0 even off a pipe with no --yes. Asking a question
// whose only possible answer is "yes, delete nothing" is how users learn to
// answer prompts without reading them.
func TestCleanInactiveWithNothingToDeleteNeedsNoAnswer(t *testing.T) {
	home, active := seedSwitchedGoHome(t)
	if code, s := runHome(t, home, "clean", "inactive", goSdk, "--yes"); code != ExitOK {
		t.Fatalf("the first clean = exit %d; stderr:\n%s", code, s.stderr)
	}

	code, s := runHome(t, home, "clean", "inactive", goSdk)
	if code != ExitOK {
		t.Fatalf("a second clean with nothing left = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if want := catalogWord(t, "zh", "cli.removedVersions", map[string]string{"count": "0"}); !strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", s.stdout, want)
	}
	if got, want := installedGoVersions(t, home), []string{active}; !slices.Equal(got, want) {
		t.Errorf("the Go directory holds %v, want exactly %v", got, want)
	}
}

func TestCleanInactiveRefusesAnUnknownSdk(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	code, s := runHome(t, t.TempDir(), "clean", "inactive", "golang", "--yes")
	if code != ExitNotFound {
		t.Fatalf("svc clean inactive golang = exit %d, want %d; stderr:\n%s", code, ExitNotFound, s.stderr)
	}
	if want := catalogWord(t, "zh", "errors.unknown-sdk-type", map[string]string{"sdk": "golang"}); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
}

func TestCleanInactiveUsage(t *testing.T) {
	for _, args := range [][]string{{"clean", "inactive"}, {"clean", "inactive", "go", "extra"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, s := pipedRun(t, nil, args...)
			if code != ExitUsage {
				t.Fatalf("svc %s = exit %d, want %d; stderr:\n%s", strings.Join(args, " "), code, ExitUsage, s.stderr)
			}
			if s.stdout.Len() != 0 {
				t.Errorf("a usage failure wrote to the result stream:\n%s", s.stdout)
			}
		})
	}
}

// --- logs rm ----------------------------------------------------------------

func TestLogsRemoveOneFile(t *testing.T) {
	path := writeLogFile(t, logName(5))

	code, s := pipedRun(t, nil, "--json", "logs", "rm", logName(5), "--yes")
	if code != ExitOK {
		t.Fatalf("svc logs rm = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result CleanResult
	decodeJSON(t, s, &result)
	if want := (CleanResult{Target: cleanTargetLogFiles, Removed: 1}); result != want {
		t.Errorf("the document is %+v, want %+v", result, want)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s survived logs rm", path)
	}
}

func TestLogsRemoveOneFileSaysWhatItDid(t *testing.T) {
	writeLogFile(t, logName(6))
	code, s := pipedRun(t, nil, "logs", "rm", logName(6), "--yes")
	if code != ExitOK {
		t.Fatalf("svc logs rm = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if want := catalogWord(t, "zh", "logs.deleteSuccess", nil); !strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", s.stdout, want)
	}
}

func TestLogsRemoveSeveralFiles(t *testing.T) {
	first := writeLogFile(t, logName(7))
	second := writeLogFile(t, logName(8))

	code, s := pipedRun(t, nil, "--json", "logs", "rm", logName(7), logName(8), "--yes")
	if code != ExitOK {
		t.Fatalf("svc logs rm of two files = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result CleanResult
	decodeJSON(t, s, &result)
	if want := (CleanResult{Target: cleanTargetLogFiles, Removed: 2}); result != want {
		t.Errorf("the document is %+v, want %+v", result, want)
	}
	for _, path := range []string{first, second} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived logs rm", path)
		}
	}
}

func TestLogsRemoveSeveralFilesAsksOnceForTheSet(t *testing.T) {
	writeLogFile(t, logName(9))
	writeLogFile(t, logName(10))

	code, s := interactiveRun(t, "n\n", nil, "logs", "rm", logName(9), logName(10))
	if code != ExitCanceled {
		t.Fatalf("a declined logs rm = exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
	}
	want := catalogWord(t, "zh", "logs.cleanSelectedConfirm", map[string]string{"count": "2"})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want it to ask %q", s.stderr, want)
	}
	// One question for the set, not one per file: a prompt per file is how a
	// user ends up answering "y" without reading the third one.
	if n := strings.Count(s.stderr.String(), "[y/N]"); n != 1 {
		t.Errorf("the set was asked about %d times, want once:\n%s", n, s.stderr)
	}
	for _, name := range []string{logName(9), logName(10)} {
		if _, err := os.Stat(filepath.Join(logmgr.GetLogDir(), name)); err != nil {
			t.Errorf("a declined logs rm deleted %s", name)
		}
	}
}

// TestLogsRemoveOfAMissingFileCountsAsDeleted pins the idempotence the help
// text promises: DeleteLogFile treats a name that is already gone as a success,
// so a repeated `logs rm` in a script exits 0 both times.
func TestLogsRemoveOfAMissingFileCountsAsDeleted(t *testing.T) {
	code, s := pipedRun(t, nil, "--json", "logs", "rm", logName(11), "--yes")
	if code != ExitOK {
		t.Fatalf("svc logs rm of a file that is not there = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result CleanResult
	decodeJSON(t, s, &result)
	if result.Removed != 1 {
		t.Errorf("the document is %+v, want one name reported as deleted", result)
	}
}

// TestLogsRemoveReportsTheNamesThatFailed is the partial-failure half. A
// non-empty directory inside the log directory survives os.Remove on every
// platform, which is the one failure this test can provoke without playing
// permission games that do not mean the same thing on Windows.
func TestLogsRemoveReportsTheNamesThatFailed(t *testing.T) {
	dir := filepath.Join(logmgr.GetLogDir(), "not-a-file")
	if err := os.MkdirAll(filepath.Join(dir, "inner"), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	gone := writeLogFile(t, logName(12))

	code, s := pipedRun(t, nil, "logs", "rm", logName(12), "not-a-file", "--yes")
	if code != ExitError {
		t.Fatalf("a partly failed logs rm = exit %d, want %d; stderr:\n%s", code, ExitError, s.stderr)
	}
	if want := catalogWord(t, "zh", "logs.cleanFail", map[string]string{"error": ""}); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want it to carry %q", s.stderr, want)
	}
	if !strings.Contains(s.stderr.String(), "not-a-file") {
		t.Errorf("stderr = %q, want it to name the file that failed", s.stderr)
	}
	// The command was asked to do more than it managed, so it reports failure
	// even though one deletion did happen -- and that deletion is not undone.
	if s.stdout.Len() != 0 {
		t.Errorf("a failed logs rm wrote to the result stream:\n%s", s.stdout)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("%s was not deleted even though it could have been", gone)
	}
}

func TestLogsRemoveRefusesAnythingThatIsNotALogFile(t *testing.T) {
	cases := []struct {
		name string
		arg  string
	}{
		{"a traversal", "../../etc/passwd"},
		{"a bare traversal segment", ".."},
		{"an absolute path", "/etc/hosts"},
		{"a windows-style traversal", `..\..\windows\win.ini`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, s := pipedRun(t, nil, "logs", "rm", c.arg, "--yes")
			if code == ExitOK {
				t.Errorf("svc logs rm %q = exit 0, want a failure", c.arg)
			}
			if s.stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing: the file must not be touched at all", s.stdout)
			}
			if !strings.HasPrefix(s.stderr.String(), "svc: ") {
				t.Errorf("stderr = %q, want the failure to carry the prefix", s.stderr)
			}
		})
	}
}

// TestLogsRemoveRefusesABadNameBeforeAsking pins the ordering that makes the
// set form safe: a mistyped name is refused before the question, so one typo
// cannot turn into a deletion of everything else that was named alongside it.
func TestLogsRemoveRefusesABadNameBeforeAsking(t *testing.T) {
	keep := writeLogFile(t, logName(13))

	code, s := interactiveRun(t, "y\n", nil, "logs", "rm", logName(13), "../../etc/passwd")
	if code == ExitOK || code == ExitCanceled {
		t.Fatalf("svc logs rm with a traversal in it = exit %d, want a refusal; stderr:\n%s", code, s.stderr)
	}
	if strings.Contains(s.stderr.String(), catalogWord(t, "zh", "logs.cleanSelectedConfirm", map[string]string{"count": "2"})) {
		t.Error("the command asked about a set it had already refused")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the well-formed name in a refused set was deleted anyway: %v", err)
	}
}

func TestLogsRemoveUsage(t *testing.T) {
	if code, _ := pipedRun(t, nil, "logs", "rm"); code != ExitUsage {
		t.Errorf("svc logs rm = exit %d, want %d", code, ExitUsage)
	}
}

func TestLogsRemoveOffAPipeNeedsYes(t *testing.T) {
	writeLogFile(t, logName(14))
	code, _ := pipedRun(t, nil, "logs", "rm", logName(14))
	if code != ExitUsage {
		t.Errorf("svc logs rm off a pipe without --yes = exit %d, want %d", code, ExitUsage)
	}
}
