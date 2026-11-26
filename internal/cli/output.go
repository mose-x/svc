package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/rivo/uniseg"

	"svc/internal/cli/i18n"
)

// Printer writes what a command produced, under the output contract of §5:
// results on stdout, everything a human reads while waiting on stderr, and
// exactly one of three shapes chosen by the resolved mode.
//
// The mode discipline lives here rather than in each command. JSON writes only
// in JSON mode; the text writers write only in the other two; a Note writes
// only to an interactive terminal. A command therefore describes its result
// once -- table for a human, struct for a machine -- and cannot corrupt either
// stream by forgetting a branch. That matters because the failure is silent:
// one stray prose line in front of a JSON document breaks every consumer of
// `svc list --json | jq`, and nothing downstream can tell where it came from.
//
// A nil Printer is safe and writes nothing, so a command constructed before
// option resolution degrades instead of panicking.
type Printer struct {
	mode    Mode
	color   bool
	quiet   bool
	catalog *i18n.Catalog
	stdout  io.Writer
	stderr  io.Writer
}

// NewPrinter builds a Printer for a resolved run. stdout is the result stream
// and stderr is where notes go; nil writers are replaced with io.Discard rather
// than left to panic. Write failures are otherwise ignored throughout: output
// is decoration relative to the exit code, so a closed pipe or a redirected-
// then-gone stderr must not turn a successful read into a failed one.
func NewPrinter(opts Options, catalog *i18n.Catalog, stdout, stderr io.Writer) *Printer {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return &Printer{
		mode:    opts.Mode,
		color:   opts.Color,
		quiet:   opts.Quiet,
		catalog: catalog,
		stdout:  stdout,
		stderr:  stderr,
	}
}

// Mode reports the output contract in force.
func (p *Printer) Mode() Mode {
	if p == nil {
		return ModePlain
	}
	return p.mode
}

// JSONMode reports whether results must be emitted as a JSON document.
func (p *Printer) JSONMode() bool { return p.Mode() == ModeJSON }

// Catalog returns the run's message catalog, which may be nil before option
// resolution. T handles that.
func (p *Printer) Catalog() *i18n.Catalog {
	if p == nil {
		return nil
	}
	return p.catalog
}

// T translates a catalog key. A missing key yields the key itself, which is
// what i18n.Catalog.T does and what output_guard_test.go fails on: a renamed
// GUI key must show up as an ugly literal here rather than as a blank column.
func (p *Printer) T(key string, params map[string]string) string {
	if p == nil || p.catalog == nil {
		return key
	}
	return p.catalog.T(key, params)
}

// Paint wraps text in the ANSI sequence for tone, or returns it unchanged when
// colour is off. Colour is decided once per run by ResolveColor, so no caller
// re-checks the environment.
func (p *Printer) Paint(text string, tone Tone) string {
	if p == nil || !p.color || text == "" {
		return text
	}
	code := ansiDim
	switch tone {
	case ToneOK:
		code = ansiGreen
	case ToneError:
		code = ansiRed
	}
	return code + text + ansiReset
}

// JSON writes v to stdout as one indented document, and only in JSON mode.
//
// The payload is the service-layer value verbatim -- the same object the GUI
// receives over the Wails binding -- so there is no second schema to keep in
// sync and a consumer that understands one understands the other. Indenting
// costs nothing downstream (jq does not care) and makes `--json` readable when
// a human runs it by hand.
func (p *Printer) JSON(value any) error {
	if p == nil || p.mode != ModeJSON {
		return nil
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = p.stdout.Write(append(encoded, '\n'))
	return err
}

// Line writes one result line to stdout in the text modes. It is a no-op in
// JSON mode, where the only thing on stdout is the document JSON wrote.
func (p *Printer) Line(format string, args ...any) {
	if p == nil || p.mode == ModeJSON {
		return
	}
	fmt.Fprintf(p.stdout, format+"\n", args...)
}

// Blank writes an empty line between two blocks of results, text modes only.
func (p *Printer) Blank() {
	if p == nil || p.mode == ModeJSON {
		return
	}
	_, _ = io.WriteString(p.stdout, "\n")
}

// Note writes one informational line to stderr, and only to an interactive
// terminal that did not ask for quiet.
//
// This is the channel for "by the way" text that is not a result: the hint
// that a version list came from the cache, the reminder to reopen a terminal
// after a switch. It is deliberately absent from plain and JSON modes -- a
// note in a pipe is noise the consumer did not ask for, and a note on stderr
// in JSON mode would land in the same stream as the JSON Lines progress.
func (p *Printer) Note(format string, args ...any) {
	if p == nil || p.quiet || p.mode != ModeTTY {
		return
	}
	fmt.Fprintf(p.stderr, format+"\n", args...)
}

// Pair is one key/value line of a KeyValue block.
type Pair struct {
	Key   string
	Value string
	// Tone colours the value, not the key: the key is a label and stays quiet.
	Tone Tone
}

// KeyValue writes an aligned `key  value` block to stdout, text modes only.
// Keys are padded to the widest one so the values line up; the padding is
// measured in terminal cells, not bytes, because the keys are localized and
// the default catalog is Chinese.
func (p *Printer) KeyValue(pairs []Pair) {
	if p == nil || p.mode == ModeJSON || len(pairs) == 0 {
		return
	}
	width := 0
	for _, pair := range pairs {
		if w := DisplayWidth(pair.Key); w > width {
			width = w
		}
	}
	var text strings.Builder
	for _, pair := range pairs {
		key := p.Paint(pair.Key, TonePlain)
		text.WriteString(key)
		text.WriteString(strings.Repeat(" ", width-DisplayWidth(pair.Key)+2))
		text.WriteString(p.Paint(pair.Value, pair.Tone))
		text.WriteString("\n")
	}
	_, _ = io.WriteString(p.stdout, text.String())
}

// Column describes one column of a Table.
type Column struct {
	// Header is the column title, already translated by the caller.
	Header string
	// Right aligns the column's cells to the right. Use it for counts and
	// sizes, where digits should line up by place value.
	Right bool
}

// Table is a result rendered as aligned columns.
type Table struct {
	Columns []Column
	// Rows holds one string per column; a short row is padded with empty
	// cells rather than rejected, so a command cannot panic on ragged data.
	Rows [][]string
	// RowTones colours a whole row, indexed like Rows. A nil or short slice
	// leaves the remaining rows plain.
	RowTones []Tone
}

// Table renders rows as aligned columns to stdout, text modes only.
//
// There is no rule line under the header and no box drawing: the output has to
// survive being piped into `grep` and `awk`, where decoration is just noise in
// the way of a field separator. Columns are separated by two spaces, which is
// wide enough to read and narrow enough that `awk` on whitespace still works.
func (p *Printer) Table(table Table) {
	if p == nil || p.mode == ModeJSON || len(table.Columns) == 0 {
		return
	}

	columns := len(table.Columns)
	widths := make([]int, columns)
	for i, column := range table.Columns {
		widths[i] = DisplayWidth(column.Header)
	}
	for _, row := range table.Rows {
		for i := 0; i < columns && i < len(row); i++ {
			if w := DisplayWidth(row[i]); w > widths[i] {
				widths[i] = w
			}
		}
	}

	var text strings.Builder
	// The header row is dimmed as a whole -- TonePlain is the dim tone -- so it
	// recedes behind the data, which is the same hierarchy the progress line
	// uses.
	// The header and the rows are padded by the same rule, so a header can
	// never end up trailing spaces its own columns do not have.
	last := columns - 1
	for i, column := range table.Columns {
		if i > 0 {
			text.WriteString("  ")
		}
		text.WriteString(p.Paint(p.cell(column.Header, widths[i], column.Right, i == last), TonePlain))
	}
	text.WriteString("\n")

	for rowIndex, row := range table.Rows {
		var tone Tone
		if rowIndex < len(table.RowTones) {
			tone = table.RowTones[rowIndex]
		}
		for i := 0; i < columns; i++ {
			if i > 0 {
				text.WriteString("  ")
			}
			value := ""
			if i < len(row) {
				value = row[i]
			}
			text.WriteString(p.Paint(p.cell(value, widths[i], table.Columns[i].Right, i == last), tone))
		}
		text.WriteString("\n")
	}
	_, _ = io.WriteString(p.stdout, text.String())
}

// cell spaces one cell out to its column width, on the side the column asks
// for -- except that the last column never gains trailing padding. It is
// invisible there, and leaving it in would make every line of `svc list` end in
// spaces a diff or a golden file then has to explain. A right-aligned last
// column keeps its LEADING padding, which is the padding that does the
// aligning, so `Right` still means something in the final column.
func (p *Printer) cell(value string, width int, right, last bool) string {
	if right {
		return p.pad(value, width, true)
	}
	if last {
		return value
	}
	return p.pad(value, width, false)
}

// pad spaces a cell out to width on the side its column asks for. The header
// is painted dim separately so the padding itself carries no escape sequences,
// which keeps DisplayWidth honest for the row pass.
func (p *Printer) pad(cell string, width int, right bool) string {
	fill := width - DisplayWidth(cell)
	if fill <= 0 {
		return cell
	}
	if right {
		return strings.Repeat(" ", fill) + cell
	}
	return cell + strings.Repeat(" ", fill)
}

// DisplayWidth returns the number of terminal cells s occupies: ANSI sequences
// count as zero and East Asian wide characters count as two.
//
// Rune count is wrong in both directions here. The default catalog is Chinese,
// so headers like "当前版本" are four runes and eight cells; and a coloured cell
// carries escape bytes that occupy no cell at all. Getting this wrong does not
// crash anything -- it silently produces a table whose columns drift by one or
// two characters, which is exactly the kind of defect nobody reports and
// everybody notices.
func DisplayWidth(text string) int {
	return uniseg.StringWidth(stripANSI(text))
}

// stripANSI removes the CSI escape sequences this package emits. It is a
// hand-rolled scanner rather than a regexp on purpose: it runs once per cell
// per render, and the only sequences in play are the ones Paint produces.
func stripANSI(text string) string {
	if !strings.Contains(text, "\x1b") {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] != '\x1b' || i+1 >= len(text) || text[i+1] != '[' {
			out.WriteByte(text[i])
			i++
			continue
		}
		// Skip ESC [ ... <final byte in @..~>.
		i += 2
		for i < len(text) && !(text[i] >= 0x40 && text[i] <= 0x7e) {
			i++
		}
		if i < len(text) {
			i++
		}
	}
	return out.String()
}
