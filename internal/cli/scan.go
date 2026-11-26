package cli

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"svc/internal/cli/i18n"
	"svc/internal/sdk"
)

// ScanRenderer reports which SDK a full status scan is currently checking.
//
// `svc list` is not the instant command it looks like: for every SDK the scan
// reads its config and, when a copy is on PATH but not managed, runs that
// copy's version command to identify it. Fourteen of those can take seconds,
// and a terminal that prints nothing for seconds reads as a hang. The GUI
// shows the same thing from the same event (sidebar.loadingSdkDetail), so the
// wording is shared rather than invented.
//
// It mirrors ProgressRenderer's three modes and its rule that progress never
// touches stdout.
type ScanRenderer struct {
	mode    Mode
	color   bool
	quiet   bool
	catalog *i18n.Catalog
	out     io.Writer

	// tty redraw state: the line is rewritten in place, so a shorter message
	// has to erase the tail of the longer one it replaces.
	dirty     bool
	lastWidth int
}

// NewScanRenderer builds a renderer for a resolved run. out must not be
// stdout; nil discards, so a command that produces no scan still constructs
// one without a nil check at every call site.
func NewScanRenderer(opts Options, catalog *i18n.Catalog, out io.Writer) *ScanRenderer {
	if out == nil {
		out = io.Discard
	}
	return &ScanRenderer{
		mode:    opts.Mode,
		color:   opts.Color,
		quiet:   opts.Quiet,
		catalog: catalog,
		out:     out,
	}
}

// ScanText renders one scan event. It is the CLI counterpart of the GUI's
// sidebar.loadingSdkDetail, including the 1-based index and total.
func ScanText(catalog *i18n.Catalog, progress sdk.ScanProgress) string {
	if catalog == nil {
		return progress.DisplayName
	}
	name := progress.DisplayName
	if name == "" {
		name = string(progress.SdkType)
	}
	return catalog.T("sidebar.loadingSdkDetail", map[string]string{
		"name":  name,
		"index": strconv.Itoa(progress.Index),
		"total": strconv.Itoa(progress.Total),
	})
}

// Render reports one scan event. Safe on a nil renderer.
func (r *ScanRenderer) Render(progress sdk.ScanProgress) {
	if r == nil || r.quiet {
		return
	}
	switch r.mode {
	case ModeJSON:
		r.renderJSON(progress)
	case ModeTTY:
		r.renderTTY(progress)
	default:
		_, _ = io.WriteString(r.out, ScanText(r.catalog, progress)+"\n")
	}
}

// Close finishes the output: in tty mode a partial line is on screen and needs
// its newline. In the other modes every write already ended with one, so Close
// is a no-op and calling it twice is harmless.
func (r *ScanRenderer) Close() {
	if r == nil || r.quiet || r.mode != ModeTTY || !r.dirty {
		return
	}
	r.dirty = false
	r.lastWidth = 0
	_, _ = io.WriteString(r.out, "\n")
}

// renderJSON writes one line per event, the payload verbatim -- the same
// object the GUI receives, so a consumer that understands one understands the
// other.
func (r *ScanRenderer) renderJSON(progress sdk.ScanProgress) {
	encoded, err := json.Marshal(progress)
	if err != nil {
		return
	}
	_, _ = r.out.Write(append(encoded, '\n'))
}

// renderTTY redraws one line in place, padding a shorter message out to the
// width of the longest one so no residue is left behind. Padding is used
// instead of the ANSI erase-to-end-of-line sequence because padding works on
// every console, including a legacy Windows conhost with virtual terminal
// processing disabled.
func (r *ScanRenderer) renderTTY(progress sdk.ScanProgress) {
	text := ScanText(r.catalog, progress)
	if r.color {
		text = ansiDim + text + ansiReset
	}
	width := DisplayWidth(text)
	pad := r.lastWidth - width
	if pad < 0 {
		pad = 0
	}
	_, _ = io.WriteString(r.out, "\r"+text+strings.Repeat(" ", pad))
	r.dirty = true
	if width > r.lastWidth {
		r.lastWidth = width
	}
}
