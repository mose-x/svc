package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"svc/internal/pathmgr"
	"svc/internal/sdk"
)

// The command tests drive Execute end to end against a temporary SVC home with
// a scrubbed PATH, so every assertion is about the command layer: what it puts
// on which stream, and which exit code it reports. Nothing here installs,
// downloads or touches the real ~/.svc.
//
// A fresh home has fourteen SDKs, none configured and none installed. That is
// the least interesting state and the most useful one to pin, because it is the
// state every empty-result branch of every command has to survive.

// catalogWord renders one shipped catalog key so an assertion can pin the
// structure of a result without hardcoding a sentence the GUI is free to
// reword. output_guard_test.go separately proves the key still exists.
func catalogWord(t *testing.T, lang, key string, params map[string]string) string {
	t.Helper()
	return loadShippedCatalog(t, lang).T(key, params)
}

func TestListReportsEverySdk(t *testing.T) {
	t.Run("a pipe gets the table on stdout and the scan on stderr", func(t *testing.T) {
		code, s := pipedRun(t, nil, "list")
		if code != ExitOK {
			t.Fatalf("svc list = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}

		lines := outLines(s)
		if len(lines) != len(sdk.AllSdkTypes())+1 {
			t.Fatalf("stdout has %d lines, want a header and %d rows:\n%s",
				len(lines), len(sdk.AllSdkTypes()), s.stdout)
		}
		// The header is the GUI's own column wording, not a CLI invention.
		for _, key := range []string{"path.sdkColumn", "detail.currentVersion", "detail.installed", "cli.columnPath"} {
			if want := catalogWord(t, "zh", key, nil); !strings.Contains(lines[0], want) {
				t.Errorf("the header %q does not carry %q (%s)", lines[0], want, key)
			}
		}
		for _, kind := range sdk.AllSdkTypes() {
			if row := tableRow(lines[1:], sdk.SdkDisplayName(kind)); row == "" {
				t.Errorf("no row for %s in:\n%s", sdk.SdkDisplayName(kind), s.stdout)
			}
		}
		if strings.Contains(s.stdout.String(), ansiDim) {
			t.Errorf("a piped run emitted colour: %q", s.stdout)
		}

		// The scan is progress, so it belongs on stderr and nowhere else.
		scan := errLines(s)
		if len(scan) != len(sdk.AllSdkTypes()) {
			t.Errorf("stderr has %d scan lines, want one per SDK:\n%s", len(scan), s.stderr)
		}
		for _, kind := range sdk.AllSdkTypes() {
			if !strings.Contains(s.stderr.String(), sdk.SdkDisplayName(kind)) {
				t.Errorf("the scan never reported %s", sdk.SdkDisplayName(kind))
			}
		}
	})

	t.Run("--quiet silences the scan but keeps the result", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--quiet", "list")
		if code != ExitOK {
			t.Fatalf("svc --quiet list = exit %d, want %d", code, ExitOK)
		}
		if s.stderr.Len() != 0 {
			t.Errorf("stderr = %q, want nothing under --quiet", s.stderr)
		}
		if len(outLines(s)) != len(sdk.AllSdkTypes())+1 {
			t.Errorf("stdout = %q, want the full table even when quiet", s.stdout)
		}
	})

	t.Run("--json puts the document alone on stdout", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--json", "list")
		if code != ExitOK {
			t.Fatalf("svc --json list = exit %d, want %d", code, ExitOK)
		}

		var statuses []sdk.SdkStatus
		decodeJSON(t, s, &statuses)
		if len(statuses) != len(sdk.AllSdkTypes()) {
			t.Fatalf("the document holds %d statuses, want %d", len(statuses), len(sdk.AllSdkTypes()))
		}
		for i, kind := range sdk.AllSdkTypes() {
			if statuses[i].SdkType != kind {
				t.Errorf("status %d is %q, want %q (registry order)", i, statuses[i].SdkType, kind)
			}
		}

		// The payload is []sdk.SdkStatus verbatim: the same object the GUI
		// receives, with no CLI-only field folded in. A consumer that
		// understands one understands the other.
		var documents []map[string]json.RawMessage
		decodeJSON(t, s, &documents)
		want := jsonKeysOf(t, sdk.SdkStatus{})
		got := jsonKeysOf(t, documents[0])
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("the document's keys are [%s], want sdk.SdkStatus's own [%s]",
				strings.Join(got, " "), strings.Join(want, " "))
		}

		// Every stderr line is JSON Lines progress, never prose: one stray
		// sentence there would be indistinguishable from a document.
		for i, line := range errLines(s) {
			var progress sdk.ScanProgress
			if err := json.Unmarshal([]byte(line), &progress); err != nil {
				t.Errorf("stderr line %d = %q is not a scan event: %v", i, line, err)
			}
		}
	})

	t.Run("ls is the same command", func(t *testing.T) {
		_, spelled := pipedRun(t, nil, "list")
		_, alias := pipedRun(t, nil, "ls")
		if alias.stdout.String() != spelled.stdout.String() {
			t.Errorf("svc ls printed:\n%s\nwant what svc list prints:\n%s", alias.stdout, spelled.stdout)
		}
	})

	t.Run("an extra argument is a usage error", func(t *testing.T) {
		code, s := pipedRun(t, nil, "list", "nodejs")
		if code != ExitUsage {
			t.Errorf("svc list nodejs = exit %d, want %d", code, ExitUsage)
		}
		if s.stdout.Len() != 0 {
			t.Errorf("the result stream got %q, want nothing for a usage error", s.stdout)
		}
		if !strings.Contains(s.stderr.String(), "Usage:") {
			t.Errorf("stderr = %q, want the usage of the command that was parsed", s.stderr)
		}
	})

	t.Run("a dangling active version is flagged, not hidden", func(t *testing.T) {
		// The one state a reader must not miss is a configured SDK whose active
		// version directory is gone: every shim for it is currently broken. A
		// fresh home cannot produce it, so the note is asserted through the
		// renderer it goes to instead -- stderr, and only at a terminal.
		code, s := terminalRun(t, nil, "list")
		if code != ExitOK {
			t.Fatalf("svc list at a terminal = exit %d, want %d", code, ExitOK)
		}
		if strings.Contains(s.stderr.String(), catalogWord(t, "zh", "detail.needsSwitch", nil)) {
			t.Errorf("stderr = %q warns about a switch a fresh home does not need", s.stderr)
		}
	})
}

// TestCurrentPrintsOnlyTheVersion pins the substitution contract: `svc current
// nodejs` exists to be written as $(svc current nodejs), so its stdout is the
// version string and nothing else -- and when there is no active version,
// stdout is empty and the exit code is still 0, because "none" is an answer and
// not a failure. Failing there would break every script running under set -e.
func TestCurrentPrintsOnlyTheVersion(t *testing.T) {
	t.Run("no active version is silence and success", func(t *testing.T) {
		code, s := pipedRun(t, nil, "current", "nodejs")
		if code != ExitOK {
			t.Errorf("svc current nodejs = exit %d, want %d", code, ExitOK)
		}
		if s.stdout.Len() != 0 {
			t.Errorf("stdout = %q, want nothing: there is no active version", s.stdout)
		}
		if s.stderr.Len() != 0 {
			t.Errorf("stderr = %q, want nothing: a read of one SDK does not scan", s.stderr)
		}
	})

	t.Run("--json emits an array in both forms", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--json", "current", "nodejs")
		if code != ExitOK {
			t.Fatalf("svc --json current nodejs = exit %d, want %d", code, ExitOK)
		}
		var statuses []sdk.SdkStatus
		decodeJSON(t, s, &statuses)
		if len(statuses) != 1 {
			t.Fatalf("the document holds %d statuses, want 1", len(statuses))
		}
		if statuses[0].SdkType != sdk.NodeJS {
			t.Errorf("the status is for %q, want %q", statuses[0].SdkType, sdk.NodeJS)
		}
	})

	t.Run("no argument lists every SDK", func(t *testing.T) {
		_, all := pipedRun(t, nil, "current")
		_, list := pipedRun(t, nil, "list")
		if all.stdout.String() != list.stdout.String() {
			t.Errorf("svc current printed:\n%s\nwant what svc list prints:\n%s", all.stdout, list.stdout)
		}
	})

	t.Run("a display name is accepted", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--json", "current", "Node.js")
		if code != ExitOK {
			t.Fatalf("svc current Node.js = exit %d, want %d", code, ExitOK)
		}
		var statuses []sdk.SdkStatus
		decodeJSON(t, s, &statuses)
		if len(statuses) != 1 || statuses[0].SdkType != sdk.NodeJS {
			t.Errorf("the document is %v, want the one nodejs status", statuses)
		}
	})

	t.Run("an unknown SDK is not found", func(t *testing.T) {
		code, s := pipedRun(t, nil, "current", "klingon")
		if code != ExitNotFound {
			t.Errorf("svc current klingon = exit %d, want %d", code, ExitNotFound)
		}
		if s.stdout.Len() != 0 {
			t.Errorf("stdout = %q, want nothing for a failure", s.stdout)
		}
		// The failure is translated, and the marker never reaches the shell.
		if strings.Contains(s.stderr.String(), "[svc:") {
			t.Errorf("stderr = %q leaked an apperr marker", s.stderr)
		}
		if !strings.Contains(s.stderr.String(), "klingon") {
			t.Errorf("stderr = %q, want it to name the SDK that was typed", s.stderr)
		}
	})

	t.Run("two arguments is a usage error", func(t *testing.T) {
		if code, _ := pipedRun(t, nil, "current", "nodejs", "go"); code != ExitUsage {
			t.Errorf("svc current nodejs go = exit %d, want %d", code, ExitUsage)
		}
	})
}

func TestStatusAssemblesWhatTheGuiAssemblesInReact(t *testing.T) {
	t.Run("--json reports one envelope with four fields", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--json", "status", "nodejs")
		if code != ExitOK {
			t.Fatalf("svc --json status nodejs = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}

		var result StatusResult
		decodeJSON(t, s, &result)
		if result.Status == nil || result.Status.SdkType != sdk.NodeJS {
			t.Errorf("status = %+v, want the nodejs status", result.Status)
		}
		if result.InstallDir == "" || !filepath.IsAbs(result.InstallDir) {
			t.Errorf("installDir = %q, want an absolute path", result.InstallDir)
		}

		var document map[string]json.RawMessage
		decodeJSON(t, s, &document)
		want := jsonKeysOf(t, StatusResult{})
		got := jsonKeysOf(t, document)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("the document's keys are [%s], want [%s]", strings.Join(got, " "), strings.Join(want, " "))
		}
		// The conflict list is an array whatever its length, so a consumer can
		// take len() without a nil check. Its CONTENT is host-dependent -- a
		// machine with Node.js in Program Files really does have a conflict --
		// so the shape is what a portable test can pin.
		if got := strings.TrimSpace(string(document["conflicts"])); !strings.HasPrefix(got, "[") {
			t.Errorf("conflicts = %s, want a JSON array rather than null", got)
		}
	})

	t.Run("text reports the state and then the conflicts", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--lang", "en", "status", "nodejs")
		if code != ExitOK {
			t.Fatalf("svc status nodejs = exit %d, want %d", code, ExitOK)
		}

		text := s.stdout.String()
		// The conflict section's heading is always there; which of its two
		// bodies follows depends on the host's PATH and is pinned by
		// TestConflictsRenderingCoversBothBranches instead.
		for _, key := range []string{"path.sdkColumn", "detail.currentVersion", "detail.installed",
			"cli.installDir", "cli.columnPath", "detail.systemConflictTitle"} {
			if want := catalogWord(t, "en", key, nil); !strings.Contains(text, want) {
				t.Errorf("stdout does not carry %q (%s):\n%s", want, key, text)
			}
		}
		if !strings.Contains(text, "Node.js") {
			t.Errorf("stdout does not name the SDK:\n%s", text)
		}
		// Nothing is installed, so the version line shows the placeholder and
		// the installed line says so in words.
		if !strings.Contains(text, catalogWord(t, "en", "cli.noVersionsInstalled", nil)) {
			t.Errorf("stdout does not say that nothing is installed:\n%s", text)
		}
		if s.stderr.Len() != 0 {
			t.Errorf("stderr = %q, want nothing: status is a result, not a note", s.stderr)
		}
	})

	t.Run("a missing argument is a usage error", func(t *testing.T) {
		if code, _ := pipedRun(t, nil, "status"); code != ExitUsage {
			t.Errorf("svc status = exit %d, want %d", code, ExitUsage)
		}
	})

	t.Run("an unknown SDK is not found", func(t *testing.T) {
		code, s := pipedRun(t, nil, "status", "klingon")
		if code != ExitNotFound {
			t.Errorf("svc status klingon = exit %d, want %d", code, ExitNotFound)
		}
		if s.stdout.Len() != 0 {
			t.Errorf("stdout = %q, want nothing for a failure", s.stdout)
		}
	})
}

func TestDirPrintsOnePath(t *testing.T) {
	t.Run("text prints exactly one absolute path", func(t *testing.T) {
		code, s := pipedRun(t, nil, "dir", "nodejs")
		if code != ExitOK {
			t.Fatalf("svc dir nodejs = exit %d, want %d", code, ExitOK)
		}
		lines := outLines(s)
		if len(lines) != 1 {
			t.Fatalf("stdout = %q, want exactly one line", s.stdout)
		}
		if !filepath.IsAbs(lines[0]) {
			t.Errorf("dir printed %q, want an absolute path", lines[0])
		}
		if !strings.HasSuffix(lines[0], string(sdk.NodeJS)) {
			t.Errorf("dir printed %q, want it to end in the SDK identifier", lines[0])
		}
		if s.stderr.Len() != 0 {
			t.Errorf("stderr = %q, want nothing", s.stderr)
		}
	})

	t.Run("--json emits the same bare string the text mode does", func(t *testing.T) {
		// One home for both invocations, or the two paths would differ for no
		// reason other than that they ran twice.
		run := sharedHome(t, nil)

		jsonCode, jsonRun := run("--json", "dir", "nodejs")
		if jsonCode != ExitOK {
			t.Fatalf("svc --json dir nodejs = exit %d, want %d", jsonCode, ExitOK)
		}
		var dir string
		decodeJSON(t, jsonRun, &dir)

		textCode, textRun := run("dir", "nodejs")
		if textCode != ExitOK {
			t.Fatalf("svc dir nodejs = exit %d, want %d", textCode, ExitOK)
		}
		if got := strings.TrimSpace(textRun.stdout.String()); dir != got {
			t.Errorf("--json reported %q but text reported %q; one command must not have two answers", dir, got)
		}
		// The JSON form is a bare string, so `svc --json dir x | jq -r .` needs
		// no knowledge of an envelope that does not exist.
		if !strings.HasPrefix(strings.TrimSpace(jsonRun.stdout.String()), `"`) {
			t.Errorf("stdout = %q, want a JSON string rather than an object", jsonRun.stdout)
		}
	})

	t.Run("an unknown SDK is not found", func(t *testing.T) {
		if code, _ := pipedRun(t, nil, "dir", "klingon"); code != ExitNotFound {
			t.Errorf("svc dir klingon = exit %d, want %d", code, ExitNotFound)
		}
	})

	t.Run("a missing argument is a usage error", func(t *testing.T) {
		if code, _ := pipedRun(t, nil, "dir"); code != ExitUsage {
			t.Errorf("svc dir = exit %d, want %d", code, ExitUsage)
		}
	})
}

// TestUrlRejectsBadInputWithoutGoingToTheNetwork covers the argument handling
// of `svc url`. Its success path resolves a download URL, which every fetcher
// does over HTTP, so it is exercised by the fetcher tests rather than here: a
// command test that reached the network would be slow, flaky and useless in CI
// with no egress. What is asserted is that both mistakes are reported before
// any request could be made.
func TestUrlRejectsBadInputWithoutGoingToTheNetwork(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"an unknown SDK is not found", []string{"url", "klingon", "1.0.0"}, ExitNotFound},
		{"one argument is a usage error", []string{"url", "nodejs"}, ExitUsage},
		{"no arguments is a usage error", []string{"url"}, ExitUsage},
		{"three arguments is a usage error", []string{"url", "nodejs", "20.11.0", "extra"}, ExitUsage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, s := pipedRun(t, nil, c.args...)
			if code != c.want {
				t.Errorf("svc %s = exit %d, want %d", strings.Join(c.args, " "), code, c.want)
			}
			if s.stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing for a failure", s.stdout)
			}
			if !strings.HasPrefix(s.stderr.String(), "svc: ") {
				t.Errorf("stderr = %q, want the failure to carry the prefix", s.stderr)
			}
		})
	}
}

// TestConflictsTreatsTheListAsAResult pins the exit code claim in the
// command's own help: 0 whether or not conflicts were found, because an empty
// list is an answer. A script that treats "no conflicts" as a failure would
// have to parse a localized sentence to find out it was wrong.
//
// Whether this host HAS conflicts is not the command's business -- a Windows
// box with Node.js in Program Files genuinely does -- so the assertions are
// about the shape of the answer and the exit code, and both bodies of the
// conflict section are covered by TestConflictsRenderingCoversBothBranches.
func TestConflictsTreatsTheListAsAResult(t *testing.T) {
	t.Run("text reports under the GUI's heading whatever it found", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--lang", "en", "conflicts", "nodejs")
		if code != ExitOK {
			t.Fatalf("svc conflicts nodejs = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		lines := outLines(s)
		if len(lines) == 0 {
			t.Fatal("stdout is empty; the command owes a heading at minimum")
		}
		if want := catalogWord(t, "en", "detail.systemConflictTitle", nil); lines[0] != want {
			t.Errorf("the first line = %q, want the heading %q", lines[0], want)
		}
		// The heading is always followed by a body: either the single "none
		// found" line, or one two-space-indented entry per conflict. Nothing
		// else may share the result stream.
		if len(lines) < 2 {
			t.Fatalf("stdout = %q has a heading but no body", s.stdout)
		}
		if empty := catalogWord(t, "en", "cli.noConflicts", nil); len(lines) != 2 || lines[1] != empty {
			for _, line := range lines[1:] {
				if !strings.HasPrefix(line, "  ") || strings.TrimSpace(line) == "" {
					t.Errorf("conflict line %q is not a two-space-indented entry", line)
				}
			}
		}
		if s.stderr.Len() != 0 {
			t.Errorf("stderr = %q, want nothing: conflicts is a result", s.stderr)
		}
	})

	t.Run("--json emits an array, never null", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--json", "conflicts", "nodejs")
		if code != ExitOK {
			t.Fatalf("svc --json conflicts nodejs = exit %d, want %d", code, ExitOK)
		}
		var conflicts []string
		decodeJSON(t, s, &conflicts)
		if conflicts == nil {
			t.Error("conflicts decoded to nil, want an array a consumer can take len() of")
		}
		if !strings.HasPrefix(strings.TrimSpace(s.stdout.String()), "[") {
			t.Errorf("stdout = %q, want a JSON array", s.stdout)
		}
	})

	t.Run("text and --json agree about how many conflicts there are", func(t *testing.T) {
		run := sharedHome(t, nil)

		jsonCode, jsonRun := run("--json", "conflicts", "nodejs")
		if jsonCode != ExitOK {
			t.Fatalf("svc --json conflicts nodejs = exit %d, want %d", jsonCode, ExitOK)
		}
		var conflicts []string
		decodeJSON(t, jsonRun, &conflicts)

		textCode, textRun := run("--lang", "en", "conflicts", "nodejs")
		if textCode != ExitOK {
			t.Fatalf("svc conflicts nodejs = exit %d, want %d", textCode, ExitOK)
		}
		body := outLines(textRun)[1:]
		if len(conflicts) == 0 {
			if len(body) != 1 || body[0] != catalogWord(t, "en", "cli.noConflicts", nil) {
				t.Errorf("stdout = %q, want the empty-list message", textRun.stdout)
			}
			return
		}
		if len(body) != len(conflicts) {
			t.Fatalf("text listed %d conflicts but --json listed %d", len(body), len(conflicts))
		}
		for i, conflict := range conflicts {
			if got := strings.TrimSpace(body[i]); got != conflict {
				t.Errorf("conflict %d is %q in text but %q in --json", i, got, conflict)
			}
		}
	})

	t.Run("an unknown SDK is not found", func(t *testing.T) {
		if code, _ := pipedRun(t, nil, "conflicts", "klingon"); code != ExitNotFound {
			t.Errorf("svc conflicts klingon = exit %d, want %d", code, ExitNotFound)
		}
	})

	t.Run("a missing argument is a usage error", func(t *testing.T) {
		if code, _ := pipedRun(t, nil, "conflicts"); code != ExitUsage {
			t.Errorf("svc conflicts = exit %d, want %d", code, ExitUsage)
		}
	})
}

// TestPathStateUsesTheGuisOwnWords pins the label precedence to the four states
// the home page renders, since the CLI borrows the catalog rather than
// inventing a fifth vocabulary.
func TestPathStateUsesTheGuisOwnWords(t *testing.T) {
	r := newRunner(t, newStreams(), globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}

	cases := []struct {
		name   string
		status sdk.SdkStatus
		key    string
		params map[string]string
	}{
		{"managed wins over everything", sdk.SdkStatus{Configured: true, SystemProtected: true, ExternalManager: "nvm", PathConfigured: true}, "app.configured", nil},
		{"system-managed next", sdk.SdkStatus{SystemProtected: true, ExternalManager: "nvm", PathConfigured: true}, "app.systemManaged", nil},
		{"then an external manager", sdk.SdkStatus{ExternalManager: "nvm", PathConfigured: true}, "app.externalManager", map[string]string{"manager": "nvm"}},
		{"then a bare PATH copy", sdk.SdkStatus{PathConfigured: true}, "app.inPathOnly", nil},
		{"and otherwise nothing", sdk.SdkStatus{}, "app.notConfigured", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := r.pathState(c.status)
			want := catalogWord(t, "zh", c.key, c.params)
			if got != want {
				t.Errorf("pathState(%+v) = %q, want %q (%s)", c.status, got, want, c.key)
			}
		})
	}
}

// TestPathEntryStateUsesTheGuisOwnWords is the same precedence for a PATH
// entry, which differs in one place: an entry with no SDK behind it is not
// "unconfigured" but a dash, because `svc path` lists every SDK-looking entry
// rather than only the importable ones.
func TestPathEntryStateUsesTheGuisOwnWords(t *testing.T) {
	r := newRunner(t, newStreams(), globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}

	cases := []struct {
		name  string
		entry pathmgr.PathEntry
		want  string
	}{
		{"managed", pathmgr.PathEntry{IsManaged: true, SdkType: "nodejs"}, catalogWord(t, "zh", "app.configured", nil)},
		{"system-managed", pathmgr.PathEntry{SystemProtected: true, SdkType: "nodejs"}, catalogWord(t, "zh", "app.systemManaged", nil)},
		{"externally managed", pathmgr.PathEntry{ExternalManager: "nvm", SdkType: "nodejs"},
			catalogWord(t, "zh", "app.externalManager", map[string]string{"manager": "nvm"})},
		{"importable", pathmgr.PathEntry{SdkType: "nodejs"}, catalogWord(t, "zh", "cli.stateImportable", nil)},
		{"nothing behind it", pathmgr.PathEntry{}, placeholderNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := r.pathEntryState(c.entry); got != c.want {
				t.Errorf("pathEntryState(%+v) = %q, want %q", c.entry, got, c.want)
			}
		})
	}
}

// TestConflictsRenderingCoversBothBranches drives the shared renderer directly,
// because which branch `svc conflicts` takes depends on the host's PATH: a
// machine with Node.js under Program Files has a conflict and a clean container
// does not. Both bodies are pinned here so neither can rot unnoticed, and
// `status` and `conflicts` cannot drift into describing one thing two ways.
func TestConflictsRenderingCoversBothBranches(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}

	t.Run("an empty list says so rather than printing nothing", func(t *testing.T) {
		for _, empty := range [][]string{nil, {}} {
			before := s.stdout.Len()
			r.printConflicts(empty)
			got := s.stdout.String()[before:]
			if want := catalogWord(t, "zh", "cli.noConflicts", nil) + "\n"; got != want {
				t.Errorf("stdout gained %q, want %q", got, want)
			}
		}
	})

	t.Run("each conflict is one indented line", func(t *testing.T) {
		before := s.stdout.Len()
		r.printConflicts([]string{"PATH: C:\\Program Files\\nodejs\\", "NODE_HOME=/opt/node"})
		lines := linesOf(s.stdout.String()[before:])
		want := []string{"  PATH: C:\\Program Files\\nodejs\\", "  NODE_HOME=/opt/node"}
		if len(lines) != len(want) {
			t.Fatalf("got %d lines, want one per conflict: %q", len(lines), lines)
		}
		for i := range want {
			if lines[i] != want[i] {
				t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
			}
		}
	})

	t.Run("a conflict list is never a note", func(t *testing.T) {
		if s.stderr.Len() != 0 {
			t.Errorf("stderr = %q, want nothing: conflicts are results, not commentary", s.stderr)
		}
	})
}
