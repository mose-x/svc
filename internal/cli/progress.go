package cli

import (
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"svc/internal/cli/i18n"
	"svc/internal/sdk"
)

// BarWidth is the progress bar width in cells. Sixteen cells is wide enough to
// read a percentage off at a glance and narrow enough to stay on one line next
// to the stage text and the byte counters in an 80-column terminal.
const BarWidth = 16

// The stage values of sdk.InstallProgress. They are the JSON contract between
// the backend and the frontend, so they are spelled out here as names rather
// than as literals in a switch; progress_guard_test.go pins this set against
// the contract comment in internal/sdk/types.go.
const (
	StageDownloading     = "downloading"
	StageExtracting      = "extracting"
	StageVerifying       = "verifying"
	StageConfiguringPath = "configuring_path"
	StageDone            = "done"
	StageError           = "error"
)

const (
	barFilled = "▓"
	barEmpty  = "░"

	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
)

// Tone is the colour intent of a progress line. Colour is applied by wrapping
// the whole line, so a tone is all the renderer has to decide.
type Tone int

const (
	// TonePlain is in-progress work: dimmed, so it recedes behind results.
	TonePlain Tone = iota
	// ToneOK is a successful completion.
	ToneOK
	// ToneError is a failure.
	ToneError
)

// ToneForStage picks the tone for a stage, mirroring how the GUI colours its
// progress row (an error is red, completion is green, everything in between is
// quiet).
func ToneForStage(stage string) Tone {
	switch stage {
	case StageError:
		return ToneError
	case StageDone:
		return ToneOK
	default:
		return TonePlain
	}
}

// DownloadPercent is the GUI's percentage: floor(downloaded*100/total).
//
// sdk.InstallProgress carries a Percent field as well, and the GUI ignores it
// here -- DetailPanel recomputes the value from the byte counters so the number
// next to the bar always agrees with the bar. The CLI does the same, for the
// same reason. The result is not clamped, again matching the GUI; ProgressBar
// clamps when it draws.
func DownloadPercent(downloaded, total int64) int {
	if total <= 0 || downloaded <= 0 {
		return 0
	}
	percent := math.Floor(float64(downloaded) * 100 / float64(total))
	if percent > math.MaxInt32 {
		// Only reachable with a corrupt event (downloaded far above total).
		// Clamping keeps strconv and the int conversion well defined.
		return math.MaxInt32
	}
	return int(percent)
}

// ProgressText renders the stage line of a progress event. It is a port of
// translateProgress in frontend/src/components/Panel/DetailPanel.tsx, including
// its default branch: an unrecognised stage falls back to the backend's own
// message so a new stage degrades into readable text instead of a blank line.
func ProgressText(catalog *i18n.Catalog, p sdk.InstallProgress) string {
	if catalog == nil {
		return p.Message
	}
	switch p.Stage {
	case StageDownloading:
		if p.TotalBytes > 0 {
			return catalog.T("progress.downloadingPercent", map[string]string{
				"percent": strconv.Itoa(DownloadPercent(p.DownloadedBytes, p.TotalBytes)),
			})
		}
		return catalog.T("progress.downloading", nil)
	case StageExtracting:
		return catalog.T("progress.extracting", nil)
	case StageVerifying:
		return catalog.T("progress.verifying", nil)
	case StageConfiguringPath:
		return catalog.T("progress.configuring_path", nil)
	case StageDone:
		return catalog.T("progress.done", nil)
	case StageError:
		return catalog.T("progress.error", map[string]string{"error": p.Message})
	default:
		return p.Message
	}
}

// BytesText renders the byte counters shown to the right of the stage line. It
// is a port of the JSX in DetailPanel.tsx, including the two-space separator
// before the speed and the rule that nothing is shown at all until the stage is
// downloading and at least one byte has arrived -- so "0 B / 43.6 MB" never
// flashes for an event that carries no counters.
func BytesText(p sdk.InstallProgress) string {
	if p.Stage != StageDownloading || p.DownloadedBytes <= 0 {
		return ""
	}
	var text strings.Builder
	text.WriteString(FormatBytes(p.DownloadedBytes))
	if p.TotalBytes > 0 {
		text.WriteString(" / ")
		text.WriteString(FormatBytes(p.TotalBytes))
	}
	if p.SpeedBytesPerSec > 0 {
		text.WriteString("  ")
		text.WriteString(FormatBytes(p.SpeedBytesPerSec))
		text.WriteString("/s")
	}
	return text.String()
}

// ProgressBar draws a width-cell bar for a percentage. The percentage is
// clamped to [0,100] and a non-positive width draws nothing. The fill count is
// truncated, so the bar only reads as full once the transfer actually is.
func ProgressBar(percent, width int) string {
	if width <= 0 {
		return ""
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := percent * width / 100
	var bar strings.Builder
	bar.Grow(width * len(barFilled))
	for i := 0; i < filled; i++ {
		bar.WriteString(barFilled)
	}
	for i := filled; i < width; i++ {
		bar.WriteString(barEmpty)
	}
	return bar.String()
}

// ProgressLine is one rendered progress line. It is a plain value so the
// layout can be asserted without a terminal, a catalog or a writer in sight.
type ProgressLine struct {
	// Text is the translated stage line.
	Text string
	// Bytes is the byte counter, empty when there is nothing to count yet.
	Bytes string
	// Bar is the drawn bar, empty when bars are off (plain mode).
	Bar string
	// Color enables the ANSI wrap.
	Color bool
	// Tone picks the colour.
	Tone Tone
}

// String renders the line. Non-empty parts are joined by two spaces, which is
// the separator the GUI's flex row implies and the one the design mockup uses.
func (l ProgressLine) String() string {
	parts := make([]string, 0, 3)
	for _, part := range []string{l.Text, l.Bar, l.Bytes} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	plain := strings.Join(parts, "  ")
	if !l.Color {
		return plain
	}
	code := ansiDim
	switch l.Tone {
	case ToneOK:
		code = ansiGreen
	case ToneError:
		code = ansiRed
	}
	return code + plain + ansiReset
}

// plainWidth is the cell count of the uncoloured line. Progress redraws pad a
// shorter line out to the width of the longest one so no residue is left
// behind; padding is used instead of the ANSI erase-to-end-of-line sequence
// because padding works on every console, including a legacy Windows conhost
// with virtual terminal processing disabled.
//
// Wide (CJK) glyphs are counted as one cell here, which under-pads by one cell
// per wide character. That is invisible in practice: consecutive redraws of the
// same stage differ only in digits, so the wide text is constant.
func (l ProgressLine) plainWidth() int {
	width := 0
	for _, part := range []string{l.Text, l.Bar, l.Bytes} {
		if part == "" {
			continue
		}
		if width > 0 {
			width += 2
		}
		width += utf8.RuneCountInString(part)
	}
	return width
}

// ProgressRenderer turns progress events into output.
//
// Progress never goes to stdout. stdout is the result channel -- `svc list |
// grep` and `svc install --json | jq` must see results and nothing else -- so
// the writer is supplied by the command layer and is os.Stderr there.
//
// Writes that fail are ignored. Progress is decoration: a closed pipe or a
// redirected-then-gone stderr must not turn a successful install into a failed
// one.
type ProgressRenderer struct {
	mode    Mode
	color   bool
	quiet   bool
	catalog *i18n.Catalog
	out     io.Writer

	// tty redraw state
	dirty     bool
	lastText  string
	lastWidth int

	// plain decimation state
	plainSeen    bool
	plainStage   string
	plainDecile  int
	plainMessage string
}

// NewProgressRenderer builds a renderer from a resolved run configuration. out
// is where progress goes and must not be stdout; a nil out discards everything
// rather than panicking, because a command that produces no progress still
// constructs a renderer.
func NewProgressRenderer(opts Options, catalog *i18n.Catalog, out io.Writer) *ProgressRenderer {
	if out == nil {
		out = io.Discard
	}
	return &ProgressRenderer{
		mode:    opts.Mode,
		color:   opts.Color,
		quiet:   opts.Quiet,
		catalog: catalog,
		out:     out,
	}
}

// Render reports one progress event. It is safe to call on a nil renderer, so
// a command that has not built one yet cannot crash a service callback.
func (r *ProgressRenderer) Render(p sdk.InstallProgress) {
	if r == nil || r.quiet {
		return
	}
	switch r.mode {
	case ModeJSON:
		r.renderJSON(p)
	case ModeTTY:
		r.renderTTY(p)
	default:
		r.renderPlain(p)
	}
}

// Close finishes the output. In tty mode a partial line is on screen and needs
// its newline; in the other modes each write already ended with one, so Close
// is a no-op and calling it twice is harmless.
func (r *ProgressRenderer) Close() {
	if r == nil || r.quiet || r.mode != ModeTTY || !r.dirty {
		return
	}
	r.dirty = false
	r.lastText = ""
	r.lastWidth = 0
	_, _ = io.WriteString(r.out, "\n")
}

// renderJSON writes one line per event, verbatim.
//
// The payload is sdk.InstallProgress marshalled as-is rather than a CLI-shaped
// envelope: it is exactly the object the GUI receives over the Wails event bus,
// so a consumer that understands one understands the other. Progress lines live
// on stderr and results on stdout, so the two streams are already separable
// without a discriminator field.
func (r *ProgressRenderer) renderJSON(p sdk.InstallProgress) {
	encoded, err := json.Marshal(p)
	if err != nil {
		return
	}
	_, _ = r.out.Write(append(encoded, '\n'))
}

// renderTTY redraws a single line in place. Identical consecutive frames are
// dropped, which is most of them: the backend emits a progress event per read
// chunk, and a 40 MB archive produces hundreds of events that differ only in
// the byte counter's last digit.
func (r *ProgressRenderer) renderTTY(p sdk.InstallProgress) {
	line := r.line(p, BarWidth)
	text := line.String()
	if r.dirty && text == r.lastText {
		return
	}
	width := line.plainWidth()
	pad := r.lastWidth - width
	if pad < 0 {
		pad = 0
	}
	if width > r.lastWidth {
		r.lastWidth = width
	}
	_, _ = io.WriteString(r.out, "\r"+text+strings.Repeat(" ", pad))
	r.dirty = true
	r.lastText = text
}

// renderPlain writes one line per milestone.
//
// A pipe is usually a log file or a CI console, where a hundred near-identical
// lines bury the thing the user came for. So: one line per stage change, and
// during a download one line per ten percent. A second error stage with a
// different message is also written, because dropping one would hide the reason
// a retry failed differently from the first attempt.
func (r *ProgressRenderer) renderPlain(p sdk.InstallProgress) {
	decile := DownloadPercent(p.DownloadedBytes, p.TotalBytes) / 10
	write := false
	switch {
	case !r.plainSeen || p.Stage != r.plainStage:
		write = true
	case p.Stage == StageDownloading && decile != r.plainDecile:
		write = true
	case p.Stage == StageError && p.Message != r.plainMessage:
		write = true
	}
	if !write {
		return
	}
	r.plainSeen = true
	r.plainStage = p.Stage
	r.plainDecile = decile
	r.plainMessage = p.Message
	_, _ = io.WriteString(r.out, r.line(p, 0).String()+"\n")
}

// line assembles the ProgressLine for an event. A bar is only drawn when the
// caller asks for one and the transfer has a known total, since a bar of an
// unknown-length download is a lie.
func (r *ProgressRenderer) line(p sdk.InstallProgress, barWidth int) ProgressLine {
	line := ProgressLine{
		Text:  ProgressText(r.catalog, p),
		Bytes: BytesText(p),
		Color: r.color,
		Tone:  ToneForStage(p.Stage),
	}
	if barWidth > 0 && p.Stage == StageDownloading && p.TotalBytes > 0 {
		line.Bar = ProgressBar(DownloadPercent(p.DownloadedBytes, p.TotalBytes), barWidth)
	}
	return line
}
