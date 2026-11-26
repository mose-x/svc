package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"svc/internal/sdk"
	"svc/internal/svclock"
)

// The version list every end-to-end test below seeds is the same shape the
// fetchers produce: descending, with the LTS flag and a release date on the
// entries that have one. Distinct SDK types per test are load-bearing -- the
// version cache and the refresh cooldown are package globals in internal/sdk,
// so two tests using the same type would see each other's state.

func versions(entries ...sdk.VersionInfo) []sdk.VersionInfo { return entries }

func version(name string, lts bool, released string) sdk.VersionInfo {
	major := 0
	for _, char := range name {
		if char < '0' || char > '9' {
			break
		}
		major = major*10 + int(char-'0')
	}
	return sdk.VersionInfo{
		Version:     name,
		Major:       major,
		DownloadURL: "https://example.invalid/" + name,
		FileName:    name + ".tar.gz",
		IsLTS:       lts,
		ReleaseDate: released,
	}
}

// seedVersions puts a list in the version cache the way a previous run's fetch
// would have. It must run BEFORE the invocation, and it seeds the process-global
// memory tier, which is exactly what sdk.GetCachedVersions reads first.
func seedVersions(kind sdk.SdkType, list []sdk.VersionInfo) {
	sdk.SetCachedVersions(kind, list)
}

// seedInstalled lays down an installed version directory and an active version
// in config.json, so the "installed" and "active" badges have something to mark.
func seedInstalled(t *testing.T, home string, kind sdk.SdkType, installed []string, active string) {
	t.Helper()
	svcDir := filepath.Join(home, ".svc")
	for _, name := range installed {
		if err := os.MkdirAll(filepath.Join(svcDir, string(kind), name), 0755); err != nil {
			t.Fatalf("mkdir the %s version dir: %v", name, err)
		}
	}
	if active == "" {
		return
	}
	if err := os.MkdirAll(svcDir, 0755); err != nil {
		t.Fatalf("mkdir the svc dir: %v", err)
	}
	raw, err := json.Marshal(map[string]any{"activeVersions": map[string]string{string(kind): active}})
	if err != nil {
		t.Fatalf("marshal config.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(svcDir, "config.json"), raw, 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

// spawnRecord is one detached spawn a test intercepted.
type spawnRecord struct {
	exe     string
	argv    []string
	logFile string
}

// runWithSpawn drives one invocation with the spawn intercepted, so a test can
// assert the decision and the argument vector without starting a process that
// would outlive it and touch a real cache.
func runWithSpawn(t *testing.T, env *Environment, seed func(home string), args ...string) (int, *streams, []spawnRecord) {
	t.Helper()
	var records []spawnRecord
	s := newStreams()
	code := Execute(ExecuteInput{
		Args:     args,
		Catalogs: shippedCatalogs(t),
		Stdin:    s.stdin,
		Stdout:   s.stdout,
		Stderr:   s.stderr,
		OpenHome: tempHome(t, seed),
		Env:      env,
		Spawn: func(exe string, argv []string, logFile string) error {
			records = append(records, spawnRecord{exe: exe, argv: argv, logFile: logFile})
			return nil
		},
	})
	return code, s, records
}

// sharedHomeWithSpawn binds ONE temporary home and intercepts the spawn of
// every invocation made through the returned function. Two runs against the same
// home are what make the cooldown testable end to end: the refresh stamp lives in
// that home's cache dir, so a fresh home per run would see a fresh window every
// time and the second run would spawn too.
func sharedHomeWithSpawn(t *testing.T, seed func(home string)) func(args ...string) (int, *streams, []spawnRecord) {
	t.Helper()
	openHome := tempHome(t, seed)
	catalogs := shippedCatalogs(t)
	return func(args ...string) (int, *streams, []spawnRecord) {
		var records []spawnRecord
		s := newStreams()
		code := Execute(ExecuteInput{
			Args:     args,
			Catalogs: catalogs,
			Stdin:    s.stdin,
			Stdout:   s.stdout,
			Stderr:   s.stderr,
			OpenHome: openHome,
			Env:      &Environment{},
			Spawn: func(exe string, argv []string, logFile string) error {
				records = append(records, spawnRecord{exe: exe, argv: argv, logFile: logFile})
				return nil
			},
		})
		return code, s, records
	}
}

func TestVersionSourceString(t *testing.T) {
	want := map[VersionSource]string{
		SourceCache:      "cache",
		SourceNetwork:    "network",
		SourceNone:       "none",
		VersionSource(9): "VersionSource(9)",
	}
	for source, name := range want {
		if got := source.String(); got != name {
			t.Errorf("VersionSource(%d).String() = %q, want %q", int(source), got, name)
		}
	}
}

// TestPlanVersionSource is the truth table of where the list comes from. The
// last column matters as much as the first: sdk.ShouldRefreshVersions RECORDS the
// cooldown when it answers "due", so a plan that consulted it on a path which
// then fetched synchronously would silently burn the window for the next run.
func TestPlanVersionSource(t *testing.T) {
	cases := []struct {
		name       string
		cacheHit   bool
		offline    bool
		force      bool
		due        bool
		wantSource VersionSource
		wantSpawn  bool
		wantAsked  bool
	}{
		{"a cache hit prints the cache and asks about a refresh", true, false, false, true, SourceCache, true, true},
		{"a cache hit inside the cooldown does not spawn", true, false, false, false, SourceCache, false, true},
		{"--refresh on a cache hit fetches instead", true, false, true, true, SourceNetwork, false, false},
		{"a cache miss fetches synchronously and never spawns", false, false, false, true, SourceNetwork, false, false},
		{"a cache miss with --refresh still fetches once", false, false, true, true, SourceNetwork, false, false},
		{"--offline on a cache hit prints it and never spawns", true, true, false, true, SourceCache, false, false},
		{"--offline on a cache miss has nothing to print", false, true, false, true, SourceNone, false, false},
		{"--offline beats --refresh on a cache hit", true, true, true, true, SourceCache, false, false},
		{"--offline beats --refresh on a cache miss", false, true, true, true, SourceNone, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			asked := false
			due := func() bool { asked = true; return c.due }
			source, spawn := planVersionSource(c.cacheHit, c.offline, c.force, due)
			if source != c.wantSource {
				t.Errorf("source = %v, want %v", source, c.wantSource)
			}
			if spawn != c.wantSpawn {
				t.Errorf("spawn = %v, want %v", spawn, c.wantSpawn)
			}
			if asked != c.wantAsked {
				t.Errorf("the cooldown was consulted = %v, want %v (consulting it records the window)", asked, c.wantAsked)
			}
		})
	}

	t.Run("a nil cooldown predicate never spawns", func(t *testing.T) {
		if source, spawn := planVersionSource(true, false, false, nil); source != SourceCache || spawn {
			t.Errorf("planVersionSource with a nil predicate = (%v, %v), want (cache, false)", source, spawn)
		}
	})
}

func TestFilterLts(t *testing.T) {
	list := versions(
		version("24.0.0", false, "2026-05-01"),
		version("22.11.0", true, "2025-11-01"),
		version("21.0.0", false, "2025-05-01"),
		version("20.18.0", true, "2024-10-01"),
	)
	got := filterLts(list)
	if len(got) != 2 || got[0].Version != "22.11.0" || got[1].Version != "20.18.0" {
		t.Fatalf("filterLts kept %v, want the two LTS entries in order", got)
	}
	// The input must survive untouched: it is the cached list, and a filter that
	// compacted in place would corrupt the cache for the next reader.
	if len(list) != 4 {
		t.Errorf("filterLts modified its input, which now holds %d entries", len(list))
	}
	if empty := filterLts(nil); len(empty) != 0 {
		t.Errorf("filterLts(nil) = %v, want an empty slice", empty)
	}
	if none := filterLts(versions(version("1.0.0", false, ""))); len(none) != 0 {
		t.Errorf("filterLts on a list with no LTS entries = %v, want empty", none)
	}
}

func TestLimitVersions(t *testing.T) {
	list := versions(version("3.0.0", false, ""), version("2.0.0", false, ""), version("1.0.0", false, ""))
	cases := []struct {
		name  string
		limit int
		want  int
	}{
		{"zero means all", 0, 3},
		{"negative means all", -1, 3},
		{"below the length truncates", 2, 2},
		{"exactly the length keeps all", 3, 3},
		{"above the length keeps all", 9, 3},
		{"one keeps the newest", 1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := limitVersions(list, c.limit)
			if len(got) != c.want {
				t.Fatalf("limitVersions(list, %d) kept %d, want %d", c.limit, len(got), c.want)
			}
			if len(got) > 0 && got[0].Version != "3.0.0" {
				t.Errorf("limitVersions kept from the wrong end: %v", got)
			}
		})
	}
	if got := limitVersions(nil, 5); len(got) != 0 {
		t.Errorf("limitVersions(nil, 5) = %v, want empty", got)
	}
}

func TestLatestVersionOf(t *testing.T) {
	if got := latestVersionOf(nil); got != "" {
		t.Errorf("latestVersionOf(nil) = %q, want empty", got)
	}
	// The head of the UNFILTERED list, which is what the GUI's badge uses; a
	// filtered head would mislabel whichever row survived the filter first.
	list := versions(version("24.0.0", false, ""), version("22.11.0", true, ""))
	if got := latestVersionOf(filterLts(list)); got != "22.11.0" {
		t.Errorf("latestVersionOf of an LTS-filtered list = %q; the caller must pass the unfiltered list", got)
	}
	if got := latestVersionOf(list); got != "24.0.0" {
		t.Errorf("latestVersionOf = %q, want 24.0.0", got)
	}
}

func TestEmptyIfNilVersions(t *testing.T) {
	if got := emptyIfNilVersions(nil); got == nil || len(got) != 0 {
		t.Errorf("emptyIfNilVersions(nil) = %#v, want a non-nil empty slice so JSON encodes []", got)
	}
	list := versions(version("1.0.0", false, ""))
	if got := emptyIfNilVersions(list); len(got) != 1 {
		t.Errorf("emptyIfNilVersions dropped entries: %v", got)
	}
}

func TestRefreshArgv(t *testing.T) {
	if got, want := refreshArgv(sdk.NodeJS, ""), []string{"__refresh", "nodejs"}; !slices.Equal(got, want) {
		t.Errorf("refreshArgv without an install path = %q, want %q", got, want)
	}
	// The override has to travel: without it the child warms the default home
	// while the parent printed from the one the user asked for.
	got := refreshArgv(sdk.Golang, filepath.Join(string(filepath.Separator), "elsewhere"))
	want := []string{"__refresh", "go", "--install-path", filepath.Join(string(filepath.Separator), "elsewhere")}
	if !slices.Equal(got, want) {
		t.Errorf("refreshArgv with an install path = %q, want %q", got, want)
	}
	if got[0] != refreshCommandName {
		t.Errorf("argv[0] = %q, want the carrier name %q", got[0], refreshCommandName)
	}
}

// TestVersionsCommandPrintsTheCachedList is the offline happy path: the table
// carries the same badges the GUI draws, in the GUI's words and order, plus the
// release date the GUI shows next to them.
func TestVersionsCommandPrintsTheCachedList(t *testing.T) {
	list := versions(
		version("24.0.0", false, "2026-05-01"),
		version("22.11.0", true, "2025-11-01"),
		version("20.18.0", true, ""),
	)
	seedVersions(sdk.NodeJS, list)

	code, s := pipedRun(t, func(home string) {
		seedInstalled(t, home, sdk.NodeJS, []string{"22.11.0"}, "22.11.0")
	}, "versions", "nodejs", "--offline")

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d\n%s", code, ExitOK, s.stderr)
	}
	rows := outLines(s)
	if len(rows) != 4 {
		t.Fatalf("got %d lines, want a header and three rows:\n%s", len(rows), s.stdout)
	}

	header := rows[0]
	for _, want := range []string{catalogWord(t, "zh", "cli.columnVersion", nil), catalogWord(t, "zh", "cli.columnTags", nil), catalogWord(t, "zh", "cli.columnReleased", nil)} {
		if !strings.Contains(header, want) {
			t.Errorf("the header %q has no %q column", header, want)
		}
	}

	newest := tableRow(rows, "24.0.0")
	if !strings.Contains(newest, catalogWord(t, "zh", "detail.latest", nil)) {
		t.Errorf("the newest row %q is not marked as latest", newest)
	}
	if !strings.Contains(newest, "2026-05-01") {
		t.Errorf("the newest row %q dropped its release date", newest)
	}

	active := tableRow(rows, "22.11.0")
	for _, want := range []string{catalogWord(t, "zh", "detail.installed", nil), catalogWord(t, "zh", "detail.currentConfig", nil), ltsBadge} {
		if !strings.Contains(active, want) {
			t.Errorf("the active LTS row %q is not marked %q", active, want)
		}
	}
	if strings.Contains(active, catalogWord(t, "zh", "detail.latest", nil)) {
		t.Errorf("the active row %q is also marked latest; only versions[0] of the unfiltered list is", active)
	}

	// A missing release date renders as the table placeholder rather than as a
	// cell that looks like a rendering bug.
	if plain := tableRow(rows, "20.18.0"); !strings.Contains(plain, placeholderNone) {
		t.Errorf("the row %q shows nothing for a missing release date, want %q", plain, placeholderNone)
	}
}

// TestVersionsCommandJSONIsTheServiceListVerbatim pins the machine contract:
// --json emits the filtered []sdk.VersionInfo with no CLI envelope around it, so
// a consumer that understands the GUI's payload understands this one.
func TestVersionsCommandJSONIsTheServiceListVerbatim(t *testing.T) {
	list := versions(
		version("17.0.1", false, "2026-01-01"),
		version("11.0.2", true, "2025-01-01"),
		version("8.0.3", true, "2024-01-01"),
	)
	seedVersions(sdk.JDK, list)

	code, s := pipedRun(t, nil, "versions", "jdk", "--offline", "--json")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d\n%s", code, ExitOK, s.stderr)
	}

	var got []sdk.VersionInfo
	decodeJSON(t, s, &got)
	if len(got) != 3 {
		t.Fatalf("got %d versions, want 3", len(got))
	}
	if got[0].Version != "17.0.1" || !got[1].IsLTS || got[2].ReleaseDate != "2024-01-01" {
		t.Errorf("the JSON payload lost fields: %+v", got)
	}
	if keys := jsonKeysOf(t, got[0]); !slices.Contains(keys, "downloadUrl") || !slices.Contains(keys, "isLts") {
		t.Errorf("the emitted object carries %v, want the sdk.VersionInfo field names", keys)
	}
	// Nothing but the document: a note on stderr would be fine, but a note on
	// stdout would break every consumer of the pipe.
	if lines := errLines(s); len(lines) != 0 {
		t.Errorf("--json wrote to stderr: %q", lines)
	}
}

// TestVersionsCommandJSONRespectsTheFilters keeps --json honest about --lts and
// --limit: they are what the user asked for, so the document reflects them
// rather than dumping the whole cache and leaving the filtering to jq.
func TestVersionsCommandJSONRespectsTheFilters(t *testing.T) {
	list := versions(
		version("3.9.0", false, ""),
		version("3.8.0", true, ""),
		version("3.7.0", true, ""),
		version("3.6.0", false, ""),
	)
	seedVersions(sdk.Python, list)

	code, s := pipedRun(t, nil, "versions", "python", "--offline", "--lts", "--limit", "1", "--json")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d\n%s", code, ExitOK, s.stderr)
	}
	var got []sdk.VersionInfo
	decodeJSON(t, s, &got)
	// Filtering happens before truncation, so --lts --limit 1 is "the newest LTS
	// release" and not "whatever survives of the newest release".
	if len(got) != 1 || got[0].Version != "3.8.0" {
		t.Errorf("got %v, want only 3.8.0", got)
	}
}

// TestVersionsCommandEmitsAnEmptyArray covers the filter that matches nothing.
// An empty JSON array rather than null is the difference between `.length`
// working and a consumer having to special-case the one run with no results.
func TestVersionsCommandEmitsAnEmptyArray(t *testing.T) {
	seedVersions(sdk.Ruby, versions(version("3.4.0", false, "")))

	t.Run("json", func(t *testing.T) {
		code, s := pipedRun(t, nil, "versions", "ruby", "--offline", "--lts", "--json")
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d\n%s", code, ExitOK, s.stderr)
		}
		if text := strings.TrimSpace(s.stdout.String()); text != "[]" {
			t.Errorf("stdout = %q, want []", text)
		}
	})

	t.Run("text", func(t *testing.T) {
		code, s := pipedRun(t, nil, "versions", "ruby", "--offline", "--lts")
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d\n%s", code, ExitOK, s.stderr)
		}
		// An empty result is a result, not a failure: no header row, no table,
		// just the sentence that says nothing matched.
		if lines := outLines(s); len(lines) != 1 || lines[0] != catalogWord(t, "zh", "cli.noVersionsMatch", nil) {
			t.Errorf("stdout = %q, want the single %q line", lines, catalogWord(t, "zh", "cli.noVersionsMatch", nil))
		}
	})
}

// TestVersionsOfflineWithoutACacheFails is the --offline error path. Exit 3 says
// "not there" rather than "failed", which is what lets a script distinguish a
// machine that has never fetched from one that cannot reach the network.
func TestVersionsOfflineWithoutACacheFails(t *testing.T) {
	code, s, spawns := runWithSpawn(t, &Environment{}, nil, "versions", "dotnet", "--offline")
	if code != ExitNotFound {
		t.Errorf("exit code = %d, want %d", code, ExitNotFound)
	}
	if len(spawns) != 0 {
		t.Errorf("a cache miss spawned %v; there is nothing cached to print while a child works", spawns)
	}
	stderr := s.stderr.String()
	if !strings.Contains(stderr, "svc: ") {
		t.Errorf("the failure was not reported: %q", stderr)
	}
	if strings.Contains(stderr, "[svc:no-cached-versions]") {
		t.Errorf("the raw marker reached the user instead of the translated message: %q", stderr)
	}
	if !strings.Contains(stderr, "dotnet") {
		t.Errorf("the message dropped the SDK name: %q", stderr)
	}
	if !strings.Contains(stderr, "--offline") {
		t.Errorf("the message does not say how to fix it: %q", stderr)
	}
	if len(outLines(s)) != 0 {
		t.Errorf("a failure wrote to the result stream: %q", outLines(s))
	}
}

// TestVersionsRejectsANegativeLimit covers the flag validation. A limit that
// cannot mean anything is a usage mistake (exit 2), and it is refused before the
// SVC home is opened, so the failure cannot depend on the machine's state.
func TestVersionsRejectsANegativeLimit(t *testing.T) {
	code, s := pipedRun(t, nil, "versions", "android", "--limit", "-1", "--offline")
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(s.stderr.String(), "--limit") {
		t.Errorf("the refusal does not name the flag: %q", s.stderr)
	}
	// The usage printed must be the subcommand's: the flag belongs to `versions`,
	// and the root's usage would not even list it.
	if !strings.Contains(s.stderr.String(), "svc versions <sdk> [flags]") {
		t.Errorf("a usage error printed the wrong usage: %q", s.stderr)
	}
	if !strings.Contains(s.stderr.String(), "--limit int") {
		t.Errorf("the usage printed does not list the offending flag: %q", s.stderr)
	}
}

// TestVersionsRejectsAnUnknownSdk keeps the shared resolver wired: an
// unresolvable name is exit 3 plus the list of accepted spellings on stderr of a
// terminal, and never on the result stream.
func TestVersionsRejectsAnUnknownSdk(t *testing.T) {
	code, s := terminalRun(t, nil, "versions", "nosuchsdk", "--offline")
	if code != ExitNotFound {
		t.Errorf("exit code = %d, want %d", code, ExitNotFound)
	}
	// The hint is rendered with an empty list because the accepted spellings are
	// interpolated at run time; matching the prefix still ties the assertion to
	// the catalog key rather than to a hardcoded sentence.
	hint := catalogWord(t, "zh", "cli.availableSdks", map[string]string{"list": ""})
	if !strings.Contains(s.stderr.String(), hint) {
		t.Errorf("the hint listing the accepted SDKs (%q) is missing: %q", hint, s.stderr)
	}
	for _, accepted := range []string{"nodejs", "flutter"} {
		if !strings.Contains(s.stderr.String(), accepted) {
			t.Errorf("the hint does not list %q: %q", accepted, s.stderr)
		}
	}
	if len(outLines(s)) != 0 {
		t.Errorf("a failure wrote to the result stream: %q", outLines(s))
	}
}

// TestVersionsSpawnsTheDetachedRefresh is the G4 acceptance test: a cache hit
// outside the cooldown prints immediately AND starts a child that survives this
// process, carrying the canonical SDK name and the parent's install path.
//
// The ordering is the subtle part. sdk.ShouldRefreshVersions answers "due" at
// most once per SDK per RefreshCooldown and RECORDS the answer in a process
// global, so the command itself has to be the first asker for this SDK type -- a
// test that warmed the cooldown up beforehand would end up asserting the
// opposite of what it set out to prove. Every test below therefore owns its own
// SDK type.
func TestVersionsSpawnsTheDetachedRefresh(t *testing.T) {
	seedVersions(sdk.PHP, versions(version("8.4.1", false, "2026-01-01")))

	// A plain t.TempDir() rather than a path computed inside the seed callback:
	// Go evaluates every argument before the call, so a custom path assigned by
	// the seed would still be empty in the argument list built alongside it.
	custom := t.TempDir()
	code, s, spawns := runWithSpawn(t, &Environment{}, nil, "versions", "php", "--install-path", custom)

	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d\n%s", code, ExitOK, s.stderr)
	}
	if len(spawns) != 1 {
		t.Fatalf("spawned %d children, want exactly 1: %v", len(spawns), spawns)
	}
	spawn := spawns[0]
	if spawn.exe == "" {
		t.Error("the child was started from an empty executable path")
	}
	want := []string{"__refresh", "php", "--install-path", custom}
	if !slices.Equal(spawn.argv, want) {
		t.Errorf("argv = %q, want %q", spawn.argv, want)
	}
	// The child's stdio must go to the null device, never to the parent's: a
	// background refresh writing into the pipe a foreground --json is read from
	// is the corruption detach exists to prevent.
	if spawn.logFile != "" {
		t.Errorf("logFile = %q, want empty so detach sends stdio to the null device", spawn.logFile)
	}
	// The result was printed from the cache while the child was starting, so the
	// user never waited for the network.
	if row := tableRow(outLines(s), "8.4.1"); row == "" {
		t.Errorf("the cached list was not printed:\n%s", s.stdout)
	}
}

// TestVersionsDoesNotSpawnTwiceInsideTheCooldown is R10: a script running
// `svc versions` in a loop must not leave a pile of fetching processes behind.
// The first run consumes the window; the second, identical run prints the same
// list and spawns nothing.
func TestVersionsDoesNotSpawnTwiceInsideTheCooldown(t *testing.T) {
	seedVersions(sdk.Perl, versions(version("5.40.0", false, "")))

	run := sharedHomeWithSpawn(t, nil)

	first, _, firstSpawns := run("versions", "perl")
	if first != ExitOK {
		t.Fatalf("the first run exited %d", first)
	}
	if len(firstSpawns) != 1 {
		t.Fatalf("the first run spawned %d children, want 1", len(firstSpawns))
	}

	second, s, secondSpawns := run("versions", "perl")
	if second != ExitOK {
		t.Fatalf("the second run exited %d\n%s", second, s.stderr)
	}
	if len(secondSpawns) != 0 {
		t.Errorf("the second run inside the cooldown spawned %v, want none", secondSpawns)
	}
	if row := tableRow(outLines(s), "5.40.0"); row == "" {
		t.Errorf("the second run printed no list:\n%s", s.stdout)
	}
}

// TestVersionsOfflineNeverSpawns covers the --offline promise in full: no
// network in this process, and no child that would do it either.
func TestVersionsOfflineNeverSpawns(t *testing.T) {
	seedVersions(sdk.Maven, versions(version("3.9.9", false, "")))

	code, s, spawns := runWithSpawn(t, &Environment{}, nil, "versions", "maven", "--offline")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d\n%s", code, ExitOK, s.stderr)
	}
	if len(spawns) != 0 {
		t.Errorf("--offline spawned %v; the flag promises the network is never touched", spawns)
	}
}

// TestVersionsRefreshFlagDoesNotSpawn is the other half of the same reasoning:
// --refresh fetches in this process, so a child would fetch the same list a
// second time for nothing.
func TestVersionsRefreshFlagDoesNotSpawn(t *testing.T) {
	// No cache is seeded, and the run cannot be allowed to reach the network, so
	// the assertion is made on the plan rather than on the command: the same
	// decision table TestPlanVersionSource pins, checked here against the flag
	// wiring that feeds it.
	source, spawn := planVersionSource(false, false, true, func() bool { return true })
	if source != SourceNetwork || spawn {
		t.Errorf("planVersionSource(miss, --refresh) = (%v, %v), want (network, false)", source, spawn)
	}
	source, spawn = planVersionSource(true, false, true, func() bool { return true })
	if source != SourceNetwork || spawn {
		t.Errorf("planVersionSource(hit, --refresh) = (%v, %v), want (network, false)", source, spawn)
	}
}

// TestVersionsCacheNoteReachesOnlyATerminal pins §5.1 for the freshness note: a
// human at a terminal is told the list is cached and being refreshed, a pipe and
// a --json consumer are told nothing at all.
func TestVersionsCacheNoteReachesOnlyATerminal(t *testing.T) {
	note := catalogWord(t, "zh", "cli.versionsFromCache", nil)

	t.Run("terminal", func(t *testing.T) {
		seedVersions(sdk.Gradle, versions(version("8.10.0", false, "")))
		code, s, spawns := runWithSpawn(t, &Environment{StdoutTTY: true, EnvNoColor: "1"}, nil, "versions", "gradle")
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d\n%s", code, ExitOK, s.stderr)
		}
		if len(spawns) != 1 {
			t.Fatalf("spawned %d children, want 1", len(spawns))
		}
		if !strings.Contains(s.stderr.String(), note) {
			t.Errorf("a terminal got no freshness note: %q", s.stderr)
		}
		// The note is on stderr, so the result stream stays a clean table.
		if strings.Contains(s.stdout.String(), note) {
			t.Errorf("the freshness note leaked into the result stream:\n%s", s.stdout)
		}
	})

	t.Run("pipe", func(t *testing.T) {
		seedVersions(sdk.Flutter, versions(version("3.24.0", false, "")))
		code, s, spawns := runWithSpawn(t, &Environment{}, nil, "versions", "flutter")
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d\n%s", code, ExitOK, s.stderr)
		}
		if len(spawns) != 1 {
			t.Fatalf("spawned %d children, want 1", len(spawns))
		}
		if len(errLines(s)) != 0 {
			t.Errorf("a pipe got a note on stderr: %q", errLines(s))
		}
	})
}

// TestVersionsLimitNoteCountsTheFilteredTotal checks the truncation note against
// the list the filter produced, not the whole cache: "the first 2 of 4" is the
// answer to "where did the rest go", while "the first 2 of 40" is not.
func TestVersionsLimitNoteCountsTheFilteredTotal(t *testing.T) {
	seedVersions(sdk.Dart, versions(
		version("3.5.0", true, ""),
		version("3.4.0", true, ""),
		version("3.3.0", false, ""),
	))
	code, s, _ := runWithSpawn(t, &Environment{StdoutTTY: true, EnvNoColor: "1"}, nil,
		"versions", "dart", "--lts", "--limit", "1")
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d\n%s", code, ExitOK, s.stderr)
	}
	want := catalogWord(t, "zh", "cli.versionsTruncated", map[string]string{"shown": "1", "total": "2"})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("no %q note: %q", want, s.stderr)
	}
	// The unfiltered cache holds three entries; counting those instead of the two
	// that survived --lts would tell the user a version was hidden that the
	// filter had already excluded.
	if wrong := catalogWord(t, "zh", "cli.versionsTruncated", map[string]string{"shown": "1", "total": "3"}); strings.Contains(s.stderr.String(), wrong) {
		t.Errorf("the note counted the unfiltered cache: %q", s.stderr)
	}
	if rows := outLines(s); len(rows) != 2 {
		t.Errorf("got %d lines, want a header and the one version shown:\n%s", len(rows), s.stdout)
	}
}

// TestRefreshCommandIsHiddenAndSilent covers the carrier's contract from the
// outside: it is not offered in help, and running it by hand produces nothing on
// either stream.
func TestRefreshCommandIsHiddenAndSilent(t *testing.T) {
	code, s := pipedRun(t, nil, "--help")
	if code != ExitOK {
		t.Fatalf("svc --help exited %d", code)
	}
	if strings.Contains(s.stdout.String(), refreshCommandName) {
		t.Errorf("the hidden carrier is advertised in help:\n%s", s.stdout)
	}
	if !strings.Contains(s.stdout.String(), "versions") {
		t.Errorf("help does not list the versions command:\n%s", s.stdout)
	}
}

// TestRefreshCommandGivesUpWhenAnotherRefreshHoldsTheLock is the single-flight
// half of R10. Holding the cache lock is also what keeps this test off the
// network: a child that cannot take the lock returns before it fetches.
func TestRefreshCommandGivesUpWhenAnotherRefreshHoldsTheLock(t *testing.T) {
	// The lock has to be on the home the command actually opens, so it is taken
	// from inside the seed callback that receives it.
	var held *svclock.Lock
	openHome := tempHome(t, func(home string) {
		lock, err := svclock.TryAcquire(filepath.Join(home, ".svc", "cache"))
		if err != nil {
			t.Fatalf("take the cache lock this test needs held: %v", err)
		}
		held = lock
	})
	defer held.Release()

	s := newStreams()
	code := Execute(ExecuteInput{
		Args:     []string{"__refresh", "nodejs"},
		Catalogs: shippedCatalogs(t),
		Stdin:    s.stdin,
		Stdout:   s.stdout,
		Stderr:   s.stderr,
		OpenHome: openHome,
		Env:      &Environment{},
		Spawn: func(string, []string, string) error {
			t.Error("the refresh carrier must never spawn another refresh")
			return nil
		},
	})

	// Exit 0 and silence: its status is unobservable by design, and a child that
	// reported failure would be reporting something nobody could act on.
	if code != ExitOK {
		t.Errorf("exit code = %d, want %d", code, ExitOK)
	}
	if len(outLines(s)) != 0 {
		t.Errorf("the carrier wrote to stdout: %q", outLines(s))
	}
	if len(errLines(s)) != 0 {
		t.Errorf("the carrier wrote to stderr: %q", errLines(s))
	}
}

// TestRefreshCommandIgnoresAnUnknownSdk is the other network-free path through
// the carrier: a name it cannot resolve is logged and dropped, still exiting 0
// and still saying nothing.
func TestRefreshCommandIgnoresAnUnknownSdk(t *testing.T) {
	code, s := pipedRun(t, nil, "__refresh", "nosuchsdk")
	if code != ExitOK {
		t.Errorf("exit code = %d, want %d", code, ExitOK)
	}
	if len(outLines(s)) != 0 || len(errLines(s)) != 0 {
		t.Errorf("the carrier was not silent: stdout=%q stderr=%q", outLines(s), errLines(s))
	}
}

// TestRefreshCommandNeedsAnSdk pins the arity. The carrier is spawned by code,
// not typed by users, so a wrong invocation is a bug in the spawner and must fail
// loudly enough to be found in a log rather than silently refreshing nothing.
func TestRefreshCommandNeedsAnSdk(t *testing.T) {
	code, s := pipedRun(t, nil, "__refresh")
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(s.stderr.String(), "Usage:") {
		t.Errorf("a usage error printed no usage: %q", s.stderr)
	}
}

// newTagRunner builds a runner with a resolved catalog and a plain-mode printer,
// which is everything versionTags needs: it translates and joins, and writes
// nothing itself.
func newTagRunner(t *testing.T, lang string) *runner {
	t.Helper()
	s := newStreams()
	r := newRunner(t, s, globalFlags{lang: lang}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}
	r.printer = NewPrinter(Options{Mode: ModePlain}, r.catalog, s.stdout, s.stderr)
	return r
}

// TestVersionTags pins the badge set, its order and its words. The order is the
// GUI's (DetailPanel.tsx renders latest, installed, active, then the LTS badge),
// and the three translated ones come from the same detail.* keys, so a wording
// change in the window changes the terminal without anyone editing this file.
func TestVersionTags(t *testing.T) {
	r := newTagRunner(t, "zh")
	installed := map[string]bool{"22.11.0": true}
	latest := catalogWord(t, "zh", "detail.latest", nil)
	isInstalled := catalogWord(t, "zh", "detail.installed", nil)
	current := catalogWord(t, "zh", "detail.currentConfig", nil)

	cases := []struct {
		name    string
		version sdk.VersionInfo
		latest  string
		current string
		want    string
	}{
		{"a plain version carries no badge", version("21.0.0", false, ""), "24.0.0", "22.11.0", placeholderNone},
		{"the newest is marked latest", version("24.0.0", false, ""), "24.0.0", "", latest},
		{"an installed version is marked installed", version("22.11.0", false, ""), "24.0.0", "", isInstalled},
		{"the active version is marked active", version("20.18.0", false, ""), "24.0.0", "20.18.0", current},
		{"an LTS release is marked LTS", version("20.18.0", true, ""), "24.0.0", "", ltsBadge},
		{
			"every badge at once keeps the GUI's order",
			version("22.11.0", true, ""), "22.11.0", "22.11.0",
			strings.Join([]string{latest, isInstalled, current, ltsBadge}, ", "),
		},
		// An empty latest or current must not mark the row that happens to be
		// compared against it: with no list and no active version, every version
		// string equals the empty one only if the guard is missing.
		{"an empty latest marks nothing", version("", false, ""), "", "", placeholderNone},
		{"an empty current marks nothing", version("22.11.0", false, ""), "", "", isInstalled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := r.versionTags(c.version, c.latest, installed, c.current); got != c.want {
				t.Errorf("versionTags = %q, want %q", got, c.want)
			}
		})
	}
}

// TestVersionTagsFollowTheRunLanguage is the A3 check at the smallest scale: the
// same row renders in whichever language the run resolved, because the words come
// from the catalog rather than from this package.
func TestVersionTagsFollowTheRunLanguage(t *testing.T) {
	r := newTagRunner(t, "en")
	got := r.versionTags(version("22.11.0", true, ""), "24.0.0", map[string]bool{"22.11.0": true}, "")
	want := strings.Join([]string{
		catalogWord(t, "en", "detail.installed", nil),
		ltsBadge,
	}, ", ")
	if got != want {
		t.Errorf("versionTags in English = %q, want %q", got, want)
	}
	if got == (newTagRunner(t, "zh")).versionTags(version("22.11.0", true, ""), "24.0.0", map[string]bool{"22.11.0": true}, "") {
		t.Errorf("the language selection had no effect on the badges: %q", got)
	}
}

// TestApplyDefaultsWiresTheProductionSpawn pins the seam itself. Every other
// spawn test injects a recorder, which is the only way to assert the decision
// without starting a process -- but an injected test double proves nothing about
// what a release build does. This is the one test that checks the default is
// detach.Spawn, and that it rejects a child it cannot start rather than
// reporting success for a refresh that never happened.
func TestApplyDefaultsWiresTheProductionSpawn(t *testing.T) {
	s := newStreams()
	// newRunner calls applyDefaults with no injected Spawn, so r.spawn is
	// whatever a real invocation gets.
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if r.spawn == nil {
		t.Fatal("the default run has no spawn function; `svc versions` would panic on a cache hit")
	}
	if got, want := reflect.ValueOf(r.spawn).Pointer(), reflect.ValueOf(spawnDetached).Pointer(); got != want {
		t.Error("the default spawn is not spawnDetached, so a release build would not go through detach.Spawn")
	}
	if err := r.spawn("", nil, ""); err == nil {
		t.Error("the default spawn accepted an empty executable path")
	}
	if err := r.spawn(filepath.Join(t.TempDir(), "no-such-svc"), nil, ""); err == nil {
		t.Error("the default spawn accepted a missing executable; a failed refresh must be loggable")
	}
}

// TestApplyDefaultsKeepsAnInjectedSpawn is the other half of the seam: an
// injected spawn must survive applyDefaults, or every test asserting a spawn
// decision would be asserting nothing.
func TestApplyDefaultsKeepsAnInjectedSpawn(t *testing.T) {
	called := false
	injected := func(string, []string, string) error { called = true; return nil }
	r := &runner{in: ExecuteInput{Catalogs: shippedCatalogs(t), Spawn: injected}}
	r.applyDefaults()
	if r.spawn == nil {
		t.Fatal("applyDefaults dropped the injected spawn")
	}
	if err := r.spawn("svc", []string{"__refresh", "go"}, ""); err != nil || !called {
		t.Errorf("the injected spawn was replaced: called=%v err=%v", called, err)
	}
}

// TestSpawnRefreshReportsAFailingSpawn is the best-effort contract, from both
// ends: the failure is reported so the caller can withhold the freshness note,
// and it reaches neither user stream, because a background refresh that could
// not start is a missed optimisation rather than a failed command.
func TestSpawnRefreshReportsAFailingSpawn(t *testing.T) {
	override := t.TempDir()
	s := newStreams()
	r := newRunner(t, s, globalFlags{installPath: override}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}
	fork := errors.New("fork: resource temporarily unavailable")
	var argv []string
	r.spawn = func(_ string, got []string, _ string) error { argv = got; return fork }

	err := r.spawnRefresh(sdk.Perl)
	if !errors.Is(err, fork) {
		t.Errorf("spawnRefresh = %v, want it to wrap %v", err, fork)
	}
	if want := []string{"__refresh", "perl", "--install-path", override}; !slices.Equal(argv, want) {
		t.Errorf("the child was started with %q, want %q", argv, want)
	}
	if s.stdout.Len() != 0 || s.stderr.Len() != 0 {
		t.Errorf("a failed spawn wrote to a user stream: stdout=%q stderr=%q", s.stdout, s.stderr)
	}
}

// TestVersionsWithholdsTheFreshnessNoteWhenTheSpawnFails closes the loop on the
// best-effort contract: the list is still printed and the exit code is still 0,
// but the command does not claim a background refresh is running when starting
// one failed. A promise the terminal cannot keep is worse than no promise.
func TestVersionsWithholdsTheFreshnessNoteWhenTheSpawnFails(t *testing.T) {
	seedVersions(sdk.Android, versions(version("35.0.0", false, "2026-01-01")))

	s := newStreams()
	code := Execute(ExecuteInput{
		Args:     []string{"versions", "android"},
		Catalogs: shippedCatalogs(t),
		Stdin:    s.stdin,
		Stdout:   s.stdout,
		Stderr:   s.stderr,
		OpenHome: tempHome(t, nil),
		Env:      &Environment{StdoutTTY: true, EnvNoColor: "1"},
		Spawn:    func(string, []string, string) error { return errors.New("fork: resource temporarily unavailable") },
	})

	if code != ExitOK {
		t.Errorf("exit code = %d, want %d: a failed background refresh must not fail the command", code, ExitOK)
	}
	if row := tableRow(outLines(s), "35.0.0"); row == "" {
		t.Errorf("the cached list was not printed:\n%s", s.stdout)
	}
	if note := catalogWord(t, "zh", "cli.versionsFromCache", nil); strings.Contains(s.stderr.String(), note) {
		t.Errorf("the freshness note %q was printed although no child started: %q", note, s.stderr)
	}
}
