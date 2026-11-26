package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"svc/internal/logmgr"
	"svc/internal/pathmgr"
	"svc/internal/storage"
)

// TestPathListsTheEntriesItFinds covers `svc path`, whose content is whatever
// the host has on PATH -- including, on macOS and Windows, the system-level
// sources pathmgr merges in. The assertions are therefore about the shape of
// the result and not about its rows: a test that named a directory would pass
// on one machine and fail on the next.
func TestPathListsTheEntriesItFinds(t *testing.T) {
	t.Run("--json emits an array of path entries", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--json", "path")
		if code != ExitOK {
			t.Fatalf("svc --json path = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		var entries []pathmgr.PathEntry
		decodeJSON(t, s, &entries)
		if entries == nil {
			t.Error("the document decoded to nil, want an array a consumer can range over")
		}
		var documents []map[string]json.RawMessage
		decodeJSON(t, s, &documents)
		if len(documents) > 0 {
			want := jsonKeysOf(t, pathmgr.PathEntry{})
			got := jsonKeysOf(t, documents[0])
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("the document's keys are [%s], want pathmgr.PathEntry's own [%s]",
					strings.Join(got, " "), strings.Join(want, " "))
			}
		}
	})

	t.Run("text renders a table or says there is nothing", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--lang", "en", "path")
		if code != ExitOK {
			t.Fatalf("svc path = exit %d, want %d", code, ExitOK)
		}
		lines := outLines(s)
		if len(lines) == 0 {
			t.Fatal("stdout is empty; a result is either a table or the empty-path message")
		}
		empty := catalogWord(t, "en", "path.emptyPath", nil)
		if len(lines) == 1 && lines[0] == empty {
			return
		}
		for _, key := range []string{"path.sdkColumn", "cli.columnPath", "cli.columnState"} {
			if want := catalogWord(t, "en", key, nil); !strings.Contains(lines[0], want) {
				t.Errorf("the header %q does not carry %q (%s)", lines[0], want, key)
			}
		}
		if len(lines) < 2 {
			t.Errorf("stdout = %q, want a header and at least one entry", s.stdout)
		}
	})

	t.Run("an extra argument is a usage error", func(t *testing.T) {
		if code, _ := pipedRun(t, nil, "path", "nodejs"); code != ExitUsage {
			t.Errorf("svc path nodejs = exit %d, want %d", code, ExitUsage)
		}
	})
}

// TestPathEntryRendering covers the table branch with entries a host may not
// happen to have, including the one state that only shows up on a machine with
// a competing version manager installed.
func TestPathEntryRendering(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}

	t.Run("the table names each entry and marks the managed ones", func(t *testing.T) {
		r.printPathEntries([]pathmgr.PathEntry{
			{Path: "/home/u/.svc/shims", SdkType: "nodejs", IsManaged: true},
			{Path: "/usr/local/n/versions/node/20.11.0/bin", SdkType: "nodejs", ExternalManager: "n"},
			{Path: "/usr/bin", SystemProtected: true},
			{Path: "/opt/node-v20/bin", SdkType: "nodejs"},
			{Path: "/home/u/bin"},
		})

		lines := outLines(s)
		if len(lines) != 6 {
			t.Fatalf("got %d lines, want a header and five entries:\n%s", len(lines), s.stdout)
		}
		want := [][]string{
			{"Node.js", "/home/u/.svc/shims", catalogWord(t, "zh", "app.configured", nil)},
			{"Node.js", "/usr/local/n/versions/node/20.11.0/bin", catalogWord(t, "zh", "app.externalManager", map[string]string{"manager": "n"})},
			{catalogWord(t, "zh", "path.noSdkDetected", nil), "/usr/bin", catalogWord(t, "zh", "app.systemManaged", nil)},
			{"Node.js", "/opt/node-v20/bin", catalogWord(t, "zh", "cli.stateImportable", nil)},
			{catalogWord(t, "zh", "path.noSdkDetected", nil), "/home/u/bin", placeholderNone},
		}
		for i, cells := range want {
			for _, cell := range cells {
				if !strings.Contains(lines[i+1], cell) {
					t.Errorf("line %d = %q, want it to carry %q", i+1, lines[i+1], cell)
				}
			}
		}
	})

	t.Run("no entries says so rather than printing a bare header", func(t *testing.T) {
		before := s.stdout.Len()
		r.printPathEntries(nil)
		got := s.stdout.String()[before:]
		if want := catalogWord(t, "zh", "path.emptyPath", nil) + "\n"; got != want {
			t.Errorf("stdout gained %q, want %q", got, want)
		}
	})
}

func TestStorageReportsTheBreakdownAndTheTotals(t *testing.T) {
	t.Run("--json reports an array whose sizes add up", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--json", "storage")
		if code != ExitOK {
			t.Fatalf("svc --json storage = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		var result StorageResult
		decodeJSON(t, s, &result)
		if result.SDKs == nil {
			t.Error("sdks decoded to nil, want an array: this is a CLI envelope and owes a stable shape")
		}
		var total int64
		for _, info := range result.SDKs {
			total += info.TotalSize
		}
		if result.TotalBytes != total {
			t.Errorf("totalBytes = %d, want the sum of the per-SDK sizes (%d)", result.TotalBytes, total)
		}
		if result.TmpCacheBytes < 0 {
			t.Errorf("tmpCacheBytes = %d, want a size and never a negative one", result.TmpCacheBytes)
		}
	})

	t.Run("a fresh home has nothing to report", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--lang", "en", "storage")
		if code != ExitOK {
			t.Fatalf("svc storage = exit %d, want %d", code, ExitOK)
		}
		lines := outLines(s)
		if len(lines) != 1 || lines[0] != catalogWord(t, "en", "cli.noStorage", nil) {
			t.Errorf("stdout = %q, want the one line that says nothing is stored", s.stdout)
		}
	})

	t.Run("an extra argument is a usage error", func(t *testing.T) {
		if code, _ := pipedRun(t, nil, "storage", "nodejs"); code != ExitUsage {
			t.Errorf("svc storage nodejs = exit %d, want %d", code, ExitUsage)
		}
	})
}

// TestStorageRendering covers the non-empty branch, which a fresh home cannot
// reach without installing an SDK. Sizes go through FormatBytes, so the
// terminal and the window describe one download the same way.
func TestStorageRendering(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}

	r.printStorage(StorageResult{
		SDKs: []storage.StorageInfo{
			{SdkType: "nodejs", DisplayName: "Node.js", SdkDir: "/x/.svc/nodejs", TotalSize: 1536, VersionCount: 2, ActiveVer: "20.11.0"},
			{SdkType: "go", DisplayName: "Go", SdkDir: "/x/.svc/go", TotalSize: 0, VersionCount: 1, ActiveVer: ""},
		},
		TmpCacheBytes: 2048,
		TotalBytes:    1536,
	})

	text := s.stdout.String()
	lines := outLines(s)
	// A header, two rows, the blank line that separates the blocks, and the
	// two labelled totals.
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want a table and a totals block:\n%s", len(lines), text)
	}
	if lines[3] != "" {
		t.Errorf("line 3 = %q, want the blank line that separates the table from the totals", lines[3])
	}
	for _, key := range []string{"path.sdkColumn", "cli.columnVersions", "cli.columnActive", "cli.columnSize", "cli.columnDirectory"} {
		if want := catalogWord(t, "zh", key, nil); !strings.Contains(lines[0], want) {
			t.Errorf("the header %q does not carry %q (%s)", lines[0], want, key)
		}
	}
	for _, want := range []string{
		"Node.js", "20.11.0", FormatBytes(1536), "/x/.svc/nodejs",
		// An SDK with no active version shows the placeholder, not a gap.
		"Go", placeholderNone, FormatBytes(0),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("stdout does not carry %q:\n%s", want, text)
		}
	}
	// The totals block is a KeyValue, so both sums are labelled.
	for _, want := range []string{
		catalogWord(t, "zh", "cli.totalSize", nil),
		catalogWord(t, "zh", "settings.tmpCache", nil),
		FormatBytes(2048),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the totals block does not carry %q:\n%s", want, text)
		}
	}
}

// TestLogsDirPrintsOnePath pins the command a user pipes into an editor: one
// absolute path, one line, nothing else.
func TestLogsDirPrintsOnePath(t *testing.T) {
	run := sharedHome(t, nil)

	code, s := run("logs", "dir")
	if code != ExitOK {
		t.Fatalf("svc logs dir = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	lines := outLines(s)
	if len(lines) != 1 {
		t.Fatalf("stdout = %q, want exactly one line", s.stdout)
	}
	if !filepath.IsAbs(lines[0]) {
		t.Errorf("logs dir printed %q, want an absolute path", lines[0])
	}
	if dir := logmgr.GetLogDir(); lines[0] != dir {
		t.Errorf("logs dir printed %q, want %q", lines[0], dir)
	}

	jsonCode, jsonRun := run("--json", "logs", "dir")
	if jsonCode != ExitOK {
		t.Fatalf("svc --json logs dir = exit %d, want %d", jsonCode, ExitOK)
	}
	var dir string
	decodeJSON(t, jsonRun, &dir)
	if dir != lines[0] {
		t.Errorf("--json reported %q but text reported %q", dir, lines[0])
	}

	if code, _ := run("logs", "dir", "extra"); code != ExitUsage {
		t.Errorf("svc logs dir extra = exit %d, want %d", code, ExitUsage)
	}
}

func TestLogsListAndShow(t *testing.T) {
	run := sharedHome(t, nil)

	code, s := run("--json", "logs", "list")
	if code != ExitOK {
		t.Fatalf("svc --json logs list = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var files []logFileInfo
	decodeJSON(t, s, &files)
	if files == nil {
		t.Error("the document decoded to nil, want an array")
	}
	if len(files) == 0 {
		t.Skip("the test binary has no log file to show; logger.Init did not create one")
	}

	// The name `logs list` reports is the name `logs show` accepts: that
	// round trip is the whole interface between the two commands. Pick a file
	// that actually holds something, so this test does not depend on no other
	// test having left an empty one in the shared log directory.
	name := ""
	for _, file := range files {
		if file.Size > 0 {
			name = file.Name
			break
		}
	}
	if name == "" {
		t.Skip("every log file is empty; nothing to show")
	}
	showCode, showRun := run("logs", "show", name)
	if showCode != ExitOK {
		t.Fatalf("svc logs show %s = exit %d, want %d; stderr:\n%s", name, showCode, ExitOK, showRun.stderr)
	}
	if showRun.stdout.Len() == 0 {
		t.Errorf("svc logs show %s printed nothing", name)
	}
	if !strings.HasSuffix(showRun.stdout.String(), "\n") {
		t.Errorf("stdout = %q, want it to end in a newline so a pipe does not swallow the last line", showRun.stdout)
	}
}

// logFileInfo mirrors logger.LogFileInfo for decoding, so the test asserts the
// JSON shape a consumer sees rather than importing a struct it could not
// otherwise reach through the command's own result type.
type logFileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// TestLogsShowRefusesAnythingThatIsNotALogFile is the security half of
// `logs show`: the argument is a bare file name, and neither a traversal nor an
// absolute path may reach the filesystem. A command that printed /etc/passwd
// because somebody typed `svc logs show ../../etc/passwd` would be a hole in a
// tool users are told to trust with their PATH.
func TestLogsShowRefusesAnythingThatIsNotALogFile(t *testing.T) {
	cases := []struct {
		name string
		arg  string
	}{
		{"a traversal", "../../etc/passwd"},
		{"a bare traversal segment", ".."},
		{"an absolute path", "/etc/hosts"},
		{"a windows-style traversal", `..\..\windows\win.ini`},
		{"a well-formed name that is not a log file", "svc-1999-01-01.log"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, s := pipedRun(t, nil, "logs", "show", c.arg)
			if code == ExitOK {
				t.Errorf("svc logs show %q = exit 0, want a failure", c.arg)
			}
			if s.stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing: the file must not be read at all", s.stdout)
			}
			if !strings.HasPrefix(s.stderr.String(), "svc: ") {
				t.Errorf("stderr = %q, want the failure to carry the prefix", s.stderr)
			}
		})
	}

	t.Run("a missing argument is a usage error", func(t *testing.T) {
		if code, _ := pipedRun(t, nil, "logs", "show"); code != ExitUsage {
			t.Errorf("svc logs show = exit %d, want %d", code, ExitUsage)
		}
	})
}

// TestLogsShowReportsAnEmptyFile covers the one branch where silence would be
// ambiguous: the file exists and holds nothing. A human staring at an empty
// terminal is told so, on stderr, and the exit code is still 0 -- an empty log
// is a result.
func TestLogsShowReportsAnEmptyFile(t *testing.T) {
	dir := logmgr.GetLogDir()
	if dir == "" {
		t.Skip("the logger has no directory; nothing to write an empty log into")
	}
	name := "svc-empty-cli-test.log"
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0644); err != nil {
		t.Fatalf("write the empty log file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, name)) })

	t.Run("a pipe gets silence and success", func(t *testing.T) {
		code, s := pipedRun(t, nil, "logs", "show", name)
		if code != ExitOK {
			t.Fatalf("svc logs show %s = exit %d, want %d; stderr:\n%s", name, code, ExitOK, s.stderr)
		}
		if s.stdout.Len() != 0 {
			t.Errorf("stdout = %q, want nothing for an empty file", s.stdout)
		}
		if s.stderr.Len() != 0 {
			t.Errorf("stderr = %q, want nothing: a note needs a terminal", s.stderr)
		}
	})

	t.Run("a terminal is told the file is empty", func(t *testing.T) {
		code, s := terminalRun(t, nil, "--lang", "en", "logs", "show", name)
		if code != ExitOK {
			t.Fatalf("svc logs show %s = exit %d, want %d", name, code, ExitOK)
		}
		if s.stdout.Len() != 0 {
			t.Errorf("stdout = %q, want nothing: the note is not a result", s.stdout)
		}
		if want := catalogWord(t, "en", "settings.noContent", nil); !strings.Contains(s.stderr.String(), want) {
			t.Errorf("stderr = %q, want it to say %q", s.stderr, want)
		}
	})

	t.Run("--json emits the empty string rather than nothing", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--json", "logs", "show", name)
		if code != ExitOK {
			t.Fatalf("svc --json logs show %s = exit %d, want %d", name, code, ExitOK)
		}
		var content string
		decodeJSON(t, s, &content)
		if content != "" {
			t.Errorf("the document is %q, want an empty string", content)
		}
	})
}

// TestLogsAloneIsHelpNotAnError pins the parent-command behaviour: `svc logs`
// with no subcommand lists them on stdout and exits 0, because the user asked
// for the command and got an answer rather than a failure.
func TestLogsAloneIsHelpNotAnError(t *testing.T) {
	for _, args := range [][]string{{"logs"}, {"settings"}, {"endpoints"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, s := pipedRun(t, nil, args...)
			if code != ExitOK {
				t.Errorf("svc %s = exit %d, want %d", strings.Join(args, " "), code, ExitOK)
			}
			if !strings.Contains(s.stdout.String(), "Available Commands:") {
				t.Errorf("stdout = %q, want the subcommand list", s.stdout)
			}
			if s.stderr.Len() != 0 {
				t.Errorf("stderr = %q, want it empty for help", s.stderr)
			}
		})
	}
}
