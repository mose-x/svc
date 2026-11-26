package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The tests in this file drive a Printer built directly rather than through
// Execute: the output contract is a property of the writer, and reaching it
// through a command would tie every assertion to a service graph. Keys passed
// to T are deliberately invented ("k.one"): a Printer with no catalog returns
// the key itself, so the assertions hold no matter how the shipped catalogs are
// worded.

// newPrinterAt builds a Printer in one mode with colour off and both streams
// captured, which is the configuration every alignment assertion below needs.
func newPrinterAt(mode Mode) (*Printer, *bytes.Buffer, *bytes.Buffer) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	return NewPrinter(Options{Mode: mode}, nil, stdout, stderr), stdout, stderr
}

// TestPrinterModeDecidesWhichWriterSpeaks is the output contract of §5 as an
// executable table. Each writer speaks in exactly the modes it may, and the
// failure it prevents is the silent one: a prose line in front of a JSON
// document, or a note in a pipe, is invisible to the test that only checks the
// exit code.
func TestPrinterModeDecidesWhichWriterSpeaks(t *testing.T) {
	cases := []struct {
		name     string
		mode     Mode
		quiet    bool
		wantJSON bool
		wantText bool
		wantNote bool
	}{
		{"a terminal gets text and notes", ModeTTY, false, false, true, true},
		{"--quiet keeps the text, drops the notes", ModeTTY, true, false, true, false},
		{"a pipe gets text only", ModePlain, false, false, true, false},
		{"--json gets the document only", ModeJSON, false, true, false, false},
		{"--json --quiet is unchanged", ModeJSON, true, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			p := NewPrinter(Options{Mode: c.mode, Quiet: c.quiet}, nil, stdout, stderr)

			// Every writer is called, in the order a command would call them,
			// so a leaked one shows up as unexpected bytes rather than as a
			// missing call.
			p.Note("a note")
			p.Line("a result line")
			p.Blank()
			p.KeyValue([]Pair{{Key: "k.one", Value: "v"}})
			p.Table(Table{Columns: []Column{{Header: "h"}}, Rows: [][]string{{"cell"}}})
			if err := p.JSON(map[string]string{"sdk": "nodejs"}); err != nil {
				t.Fatalf("JSON: %v", err)
			}

			if gotJSON := stdout.String(); c.wantJSON {
				var decoded map[string]string
				if err := json.Unmarshal([]byte(gotJSON), &decoded); err != nil {
					t.Fatalf("stdout = %q, which is not a JSON document: %v", gotJSON, err)
				}
				if decoded["sdk"] != "nodejs" {
					t.Errorf("the document decoded to %v, want the payload verbatim", decoded)
				}
			} else if strings.Contains(gotJSON, `"sdk"`) {
				t.Errorf("stdout = %q, want no JSON document in mode %v", gotJSON, c.mode)
			}

			for _, want := range []string{"a result line", "k.one", "cell"} {
				if got := strings.Contains(stdout.String(), want); got != c.wantText {
					t.Errorf("stdout contains %q = %v, want %v in mode %v", want, got, c.wantText, c.mode)
				}
			}
			if gotNote := strings.Contains(stderr.String(), "a note"); gotNote != c.wantNote {
				t.Errorf("stderr contains the note = %v, want %v in mode %v (quiet=%v)",
					gotNote, c.wantNote, c.mode, c.quiet)
			}
		})
	}
}

// TestPrinterJSONLeavesStdoutParseable is the safety net the mode discipline
// exists for: a command that writes prose around its result cannot corrupt the
// document, because in JSON mode the prose writers are the ones that are off.
func TestPrinterJSONLeavesStdoutParseable(t *testing.T) {
	p, stdout, stderr := newPrinterAt(ModeJSON)

	p.Line("by the way")
	p.Note("from the cache")
	if err := p.JSON([]string{"20.11.0", "18.19.0"}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	p.Table(Table{Columns: []Column{{Header: "版本"}}, Rows: [][]string{{"20.11.0"}}})
	p.Blank()

	var decoded []string
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout = %q is not parseable: %v", stdout, err)
	}
	if len(decoded) != 2 || decoded[0] != "20.11.0" {
		t.Errorf("decoded %v, want the two versions", decoded)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want it empty: JSON Lines progress owns that stream", stderr)
	}
}

// TestPrinterJSONReportsAMarshalFailure keeps the one error JSON can produce on
// the command's error path, where it becomes exit 1 rather than a truncated
// document a consumer half-parses.
func TestPrinterJSONReportsAMarshalFailure(t *testing.T) {
	p, stdout, _ := newPrinterAt(ModeJSON)

	if err := p.JSON(make(chan int)); err == nil {
		t.Error("JSON(chan) = nil error, want the marshal failure")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing written for a value that cannot be encoded", stdout)
	}
	// The same value in a text mode is a no-op, not an error: the mode decided
	// the writer was silent before the value was ever looked at.
	plain, _, _ := newPrinterAt(ModePlain)
	if err := plain.JSON(make(chan int)); err != nil {
		t.Errorf("JSON(chan) in plain mode = %v, want nil", err)
	}
}

// TestPrinterIsSafeBeforeOptionResolution covers the nil receiver. A command
// constructed before PersistentPreRunE ran has no Printer; degrading to silence
// is the difference between a confusing empty run and a panic in front of a
// user.
func TestPrinterIsSafeBeforeOptionResolution(t *testing.T) {
	var p *Printer

	if got := p.Mode(); got != ModePlain {
		t.Errorf("Mode() = %v, want %v", got, ModePlain)
	}
	if p.JSONMode() {
		t.Error("JSONMode() = true, want false so the text writers stay on")
	}
	if p.Catalog() != nil {
		t.Error("Catalog() != nil, want no catalog before resolution")
	}
	if got := p.T("k.one", map[string]string{"a": "b"}); got != "k.one" {
		t.Errorf("T() = %q, want the key itself", got)
	}
	if got := p.Paint("text", ToneError); got != "text" {
		t.Errorf("Paint() = %q, want the text unchanged", got)
	}
	if err := p.JSON(map[string]string{"sdk": "go"}); err != nil {
		t.Errorf("JSON() = %v, want nil", err)
	}
	// None of these may panic; there is nothing to assert but that they return.
	p.Line("%s", "ignored")
	p.Blank()
	p.Note("%s", "ignored")
	p.KeyValue([]Pair{{Key: "k", Value: "v"}})
	p.Table(Table{Columns: []Column{{Header: "h"}}, Rows: [][]string{{"r"}}})
}

// TestNewPrinterDiscardsNilStreams pins the constructor's nil handling: a
// command with no streams wired still writes, and the write failures Printer
// ignores throughout are the reason a closed pipe cannot turn a successful
// read into a failed one.
func TestNewPrinterDiscardsNilStreams(t *testing.T) {
	p := NewPrinter(Options{Mode: ModeTTY}, nil, nil, nil)

	p.Line("to nowhere")
	p.Note("to nowhere")
	if err := p.JSON("value"); err != nil {
		t.Errorf("JSON with a discarded stdout = %v, want nil", err)
	}
}

func TestPrinterTranslatesAndFallsBackToTheKey(t *testing.T) {
	catalog := loadShippedCatalog(t, "zh")
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	p := NewPrinter(Options{Mode: ModePlain}, catalog, stdout, stderr)

	if got := p.T("path.sdkColumn", nil); got != "SDK" {
		t.Errorf("T(path.sdkColumn) = %q, want the shipped wording", got)
	}
	// A renamed GUI key must surface as an ugly literal rather than as a blank
	// column nobody can diagnose.
	if got := p.T("path.renamedAway", nil); got != "path.renamedAway" {
		t.Errorf("T for a missing key = %q, want the key itself", got)
	}
	if got := p.Catalog(); got != catalog {
		t.Error("Catalog() did not return the run's catalog")
	}
	if p.JSONMode() {
		t.Error("JSONMode() = true in plain mode")
	}
}

func TestPrinterPaintFollowsTheColourDecision(t *testing.T) {
	cases := []struct {
		name  string
		color bool
		text  string
		tone  Tone
		want  string
	}{
		{"colour off leaves an error alone", false, "boom", ToneError, "boom"},
		{"colour off leaves an ok alone", false, "done", ToneOK, "done"},
		{"plain is dim", true, "boom", TonePlain, ansiDim + "boom" + ansiReset},
		{"ok is green", true, "done", ToneOK, ansiGreen + "done" + ansiReset},
		{"error is red", true, "boom", ToneError, ansiRed + "boom" + ansiReset},
		{"empty text is never wrapped", true, "", ToneOK, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, _, _ := newPrinterAt(ModePlain)
			p.color = c.color
			if got := p.Paint(c.text, c.tone); got != c.want {
				t.Errorf("Paint(%q, %v) = %q, want %q", c.text, c.tone, got, c.want)
			}
		})
	}
}

// TestPrinterKeyValueAlignsOnDisplayCells covers the case rune count gets
// wrong in the direction that matters most: the default catalog is Chinese, so
// a four-rune key occupies eight cells.
func TestPrinterKeyValueAlignsOnDisplayCells(t *testing.T) {
	p, stdout, _ := newPrinterAt(ModePlain)

	p.KeyValue([]Pair{
		{Key: "SDK", Value: "Node.js"},
		{Key: "当前版本", Value: "20.11.0", Tone: ToneOK},
		{Key: "N", Value: "3"},
	})

	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want one per pair: %q", len(lines), lines)
	}
	want := []string{
		"SDK       Node.js",
		"当前版本  20.11.0",
		"N         3",
	}
	values := []string{"Node.js", "20.11.0", "3"}
	for i, line := range lines {
		if line != want[i] {
			t.Errorf("line %d = %q, want %q", i, line, want[i])
		}
		// The values must all start in the same cell, which is the whole point
		// of measuring keys in display cells rather than runes.
		index := strings.Index(line, values[i])
		if index < 0 {
			t.Fatalf("line %d = %q lost its value %q", i, line, values[i])
		}
		if offset := DisplayWidth(line[:index]); offset != 10 {
			t.Errorf("line %d starts its value at cell %d, want 10 (the widest key plus the two-cell gap)", i, offset)
		}
	}
}

func TestPrinterKeyValueSkipsEmptyAndSilentModes(t *testing.T) {
	p, stdout, _ := newPrinterAt(ModePlain)
	p.KeyValue(nil)
	p.KeyValue([]Pair{})
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing for no pairs", stdout)
	}

	inJSON, jsonOut, _ := newPrinterAt(ModeJSON)
	inJSON.KeyValue([]Pair{{Key: "k", Value: "v"}})
	if jsonOut.Len() != 0 {
		t.Errorf("stdout = %q, want nothing in JSON mode", jsonOut)
	}
}

func TestPrinterTableAlignsOnDisplayCells(t *testing.T) {
	p, stdout, _ := newPrinterAt(ModePlain)

	p.Table(Table{
		Columns: []Column{
			{Header: "SDK"},
			{Header: "当前版本"},
			{Header: "N", Right: true},
		},
		Rows: [][]string{
			{"Node.js", "20.11.0", "3"},
			{"JDK", "-", "17"},
		},
	})

	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	want := []string{
		"SDK      当前版本   N",
		"Node.js  20.11.0    3",
		"JDK      -         17",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), lines)
	}
	for i, line := range lines {
		if line != want[i] {
			t.Errorf("line %d = %q, want %q", i, line, want[i])
		}
		if strings.HasSuffix(line, " ") {
			t.Errorf("line %d = %q ends in padding, which a golden file then has to explain", i, line)
		}
	}
	// A right-aligned column lines its digits up by place value, and it does so
	// in the last column too: both counts end in the same cell.
	for _, i := range []int{1, 2} {
		if got := DisplayWidth(lines[i]); got != 21 {
			t.Errorf("line %d = %q is %d cells wide, want 21", i, lines[i], got)
		}
	}
	// And the middle column, which is left-aligned, starts every value in the
	// same cell.
	for _, i := range []int{1, 2} {
		offset := DisplayWidth(lines[i][:strings.Index(lines[i], []string{"20.11.0", "-"}[i-1])])
		if offset != 9 {
			t.Errorf("line %d starts its second column at cell %d, want 9", i, offset)
		}
	}
}

// TestPrinterTableDropsTrailingPaddingFromTheLastColumn is the case `svc list`
// actually hits: a left-aligned last column whose header is shorter than its
// widest cell. Padding the header there ends every table in invisible spaces,
// which a diff or a golden file then has to explain -- and which the rows of
// the same table do not have, so the two would disagree about their own width.
func TestPrinterTableDropsTrailingPaddingFromTheLastColumn(t *testing.T) {
	p, stdout, _ := newPrinterAt(ModePlain)

	p.Table(Table{
		Columns: []Column{{Header: "SDK"}, {Header: "PATH"}},
		Rows:    [][]string{{"Node.js", "Importable"}, {"JDK", "-"}},
	})

	lines := linesOf(stdout.String())
	want := []string{
		"SDK      PATH",
		"Node.js  Importable",
		"JDK      -",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), stdout)
	}
	for i, line := range lines {
		if line != want[i] {
			t.Errorf("line %d = %q, want %q", i, line, want[i])
		}
		if strings.HasSuffix(line, " ") {
			t.Errorf("line %d = %q ends in trailing padding", i, line)
		}
	}

	// A right-aligned last column keeps its leading padding, which is the
	// padding that does the aligning, so Right still means something there.
	right, rightOut, _ := newPrinterAt(ModePlain)
	right.Table(Table{
		Columns: []Column{{Header: "SDK"}, {Header: "N", Right: true}},
		Rows:    [][]string{{"Node.js", "3"}, {"JDK", "17"}},
	})
	rightLines := linesOf(rightOut.String())
	for _, line := range rightLines {
		if strings.HasSuffix(line, " ") {
			t.Errorf("line %q ends in trailing padding", line)
		}
	}
	// Both counts end in the same cell.
	if got := DisplayWidth(rightLines[1]); got != DisplayWidth(rightLines[2]) {
		t.Errorf("the right-aligned rows are %d and %d cells wide, want them equal",
			DisplayWidth(rightLines[1]), DisplayWidth(rightLines[2]))
	}
	if !strings.Contains(rightLines[1], " 3") || !strings.Contains(rightLines[2], "17") {
		t.Errorf("the right-aligned column did not align: %q and %q", rightLines[1], rightLines[2])
	}
}

func TestPrinterTableToleratesRaggedRows(t *testing.T) {
	p, stdout, _ := newPrinterAt(ModePlain)

	p.Table(Table{
		Columns: []Column{{Header: "A"}, {Header: "B"}},
		Rows: [][]string{
			{"short"},               // fewer cells than columns
			{"one", "two", "three"}, // more cells than columns
			{},                      // none at all
		},
	})

	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want a header and three rows: %q", len(lines), lines)
	}
	if !strings.HasPrefix(lines[1], "short") {
		t.Errorf("line 1 = %q, want it to start with the one cell given", lines[1])
	}
	if strings.TrimRight(lines[1], " ") != "short" {
		t.Errorf("line 1 = %q, want the missing cell padded out to the column width", lines[1])
	}
	if strings.Contains(lines[2], "three") {
		t.Errorf("line 2 = %q, want the extra cell dropped rather than widening the table", lines[2])
	}
	if strings.TrimSpace(lines[3]) != "" {
		t.Errorf("line 3 = %q, want an empty row to render as blanks", lines[3])
	}
}

func TestPrinterTableSkipsAnEmptyTable(t *testing.T) {
	p, stdout, _ := newPrinterAt(ModePlain)

	p.Table(Table{})
	p.Table(Table{Columns: nil, Rows: [][]string{{"orphan"}}})
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing without columns", stdout)
	}

	inJSON, jsonOut, _ := newPrinterAt(ModeJSON)
	inJSON.Table(Table{Columns: []Column{{Header: "h"}}, Rows: [][]string{{"r"}}})
	if jsonOut.Len() != 0 {
		t.Errorf("stdout = %q, want nothing in JSON mode", jsonOut)
	}
}

// TestPrinterTableColoursWholeRows pins RowTones, including the short-slice
// rule: a row with no tone recorded is plain, so a command that only flags the
// interesting rows cannot leave the rest uncoloured by accident.
func TestPrinterTableColoursWholeRows(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	p := NewPrinter(Options{Mode: ModePlain, Color: true}, nil, stdout, stderr)

	p.Table(Table{
		Columns:  []Column{{Header: "SDK"}},
		Rows:     [][]string{{"broken"}, {"fine"}, {"untoned"}},
		RowTones: []Tone{ToneError, ToneOK},
	})

	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want a header and three rows: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], ansiDim) {
		t.Errorf("the header = %q, want it dimmed behind the data", lines[0])
	}
	if !strings.Contains(lines[1], ansiRed) {
		t.Errorf("row 0 = %q, want the error tone", lines[1])
	}
	if !strings.Contains(lines[2], ansiGreen) {
		t.Errorf("row 1 = %q, want the ok tone", lines[2])
	}
	if strings.Contains(lines[3], ansiRed) || strings.Contains(lines[3], ansiGreen) {
		t.Errorf("row 2 = %q, want a missing tone to leave the row plain", lines[3])
	}
	// Colour must not move a single cell: the escape bytes occupy no width.
	for i, line := range lines {
		if got, want := DisplayWidth(line), DisplayWidth(stripANSI(line)); got != want {
			t.Errorf("line %d is %d cells with colour and %d without", i, got, want)
		}
	}
	if got := stripANSI(lines[1]); got != "broken" {
		t.Errorf("row 0 stripped = %q, want the cell text", got)
	}
}

func TestDisplayWidth(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "nodejs", 6},
		{"spaces count", "a b", 3},
		{"wide characters count twice", "当前版本", 8},
		{"mixed", "PATH 中", 7},
		{"ansi counts as nothing", ansiRed + "boom" + ansiReset, 4},
		{"ansi inside wide text", ansiDim + "当前" + ansiReset, 4},
		// stripANSI leaves a lone ESC alone, and uniseg then reports it as a
		// control character, which occupies no cell -- the same answer a
		// terminal gives, since a bare ESC starts a sequence rather than
		// printing anything.
		{"a lone escape occupies no cell", "\x1b", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DisplayWidth(c.text); got != c.want {
				t.Errorf("DisplayWidth(%q) = %d, want %d", c.text, got, c.want)
			}
		})
	}
}

func TestStripANSI(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"no escapes is returned as-is", "plain text", "plain text"},
		{"a wrapped cell", ansiGreen + "ok" + ansiReset, "ok"},
		{"several sequences", "\x1b[1m\x1b[31mred bold\x1b[0m", "red bold"},
		{"wide text keeps its runes", ansiDim + "当前版本" + ansiReset, "当前版本"},
		{"a lone escape survives", "\x1bX", "\x1bX"},
		{"a truncated sequence is dropped", "abc\x1b[", "abc"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripANSI(c.text); got != c.want {
				t.Errorf("stripANSI(%q) = %q, want %q", c.text, got, c.want)
			}
		})
	}
	// Idempotence: what a renderer paints twice must not accumulate.
	painted := ansiRed + ansiDim + "x" + ansiReset + ansiReset
	if got := stripANSI(stripANSI(painted)); got != "x" {
		t.Errorf("stripping twice = %q, want %q", got, "x")
	}
}

// TestPrinterBlankSeparatesBlocks is small but load-bearing: a blank line is
// what keeps a KeyValue block from running into the heading under it, and in
// JSON mode it must not exist at all.
func TestPrinterBlankSeparatesBlocks(t *testing.T) {
	p, stdout, _ := newPrinterAt(ModePlain)
	p.Line("first")
	p.Blank()
	p.Line("second")
	if got := stdout.String(); got != "first\n\nsecond\n" {
		t.Errorf("stdout = %q, want the blank line between the two", got)
	}

	inJSON, jsonOut, _ := newPrinterAt(ModeJSON)
	inJSON.Blank()
	if jsonOut.Len() != 0 {
		t.Errorf("stdout = %q, want nothing in JSON mode", jsonOut)
	}
}
