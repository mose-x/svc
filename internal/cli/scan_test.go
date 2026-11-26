package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"svc/internal/appcore"
	"svc/internal/cli/i18n"
	"svc/internal/sdk"
)

// scanZh and scanEn mirror the shipped catalogs' sidebar block, the same way
// progressZh and progressEn mirror the progress block: a wording change in the
// GUI must not break the behavioural tests, and output_guard_test.go pins the
// real key separately.
const (
	scanZh = `{
	  "sidebar": {
	    "loadingSdkDetail": "正在检查 {{name}}（{{index}}/{{total}}）…"
	  }
	}`
	scanEn = `{
	  "sidebar": {
	    "loadingSdkDetail": "Checking {{name}} ({{index}}/{{total}})..."
	  }
	}`
)

func scanCatalog(t *testing.T, lang string) *i18n.Catalog {
	t.Helper()
	catalog, err := i18n.New(map[string][]byte{
		i18n.DefaultLang:  []byte(scanZh),
		i18n.FallbackLang: []byte(scanEn),
	}, lang)
	if err != nil {
		t.Fatalf("i18n.New(%q): %v", lang, err)
	}
	return catalog
}

func scanEvent(kind sdk.SdkType, index, total int) sdk.ScanProgress {
	return sdk.ScanProgress{
		SdkType:     kind,
		DisplayName: sdk.SdkDisplayName(kind),
		Index:       index,
		Total:       total,
	}
}

func TestScanText(t *testing.T) {
	cases := []struct {
		name     string
		progress sdk.ScanProgress
		wantZh   string
		wantEn   string
	}{
		{
			name:     "the first of fourteen",
			progress: scanEvent(sdk.NodeJS, 1, 14),
			wantZh:   "正在检查 Node.js（1/14）…",
			wantEn:   "Checking Node.js (1/14)...",
		},
		{
			name:     "the last of fourteen",
			progress: scanEvent(sdk.Dart, 14, 14),
			wantZh:   "正在检查 Dart（14/14）…",
			wantEn:   "Checking Dart (14/14)...",
		},
		{
			name:     "a name the catalog cannot supply falls back to the identifier",
			progress: sdk.ScanProgress{SdkType: sdk.Python, Index: 4, Total: 14},
			wantZh:   "正在检查 python（4/14）…",
			wantEn:   "Checking python (4/14)...",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ScanText(scanCatalog(t, "zh"), c.progress); got != c.wantZh {
				t.Errorf("ScanText(zh) = %q, want %q", got, c.wantZh)
			}
			if got := ScanText(scanCatalog(t, "en"), c.progress); got != c.wantEn {
				t.Errorf("ScanText(en) = %q, want %q", got, c.wantEn)
			}
		})
	}

	t.Run("no catalog renders the name alone", func(t *testing.T) {
		if got := ScanText(nil, scanEvent(sdk.Rust, 5, 14)); got != "Rust" {
			t.Errorf("ScanText(nil) = %q, want the display name", got)
		}
		if got := ScanText(nil, sdk.ScanProgress{SdkType: sdk.Rust}); got != "" {
			t.Errorf("ScanText(nil) with no name = %q, want it empty", got)
		}
	})
}

// TestScanTextWithTheShippedCatalogs is the guard half: the key must exist in
// the real catalogs and interpolate all three parameters, or `svc list` would
// print "sidebar.loadingSdkDetail" for seconds while it scans.
func TestScanTextWithTheShippedCatalogs(t *testing.T) {
	for _, lang := range []string{"zh", "en"} {
		catalog := loadShippedCatalog(t, lang)
		text := ScanText(catalog, scanEvent(sdk.Golang, 3, 14))
		if strings.Contains(text, "sidebar.") {
			t.Errorf("%s: ScanText leaked a catalog key: %q", lang, text)
		}
		if strings.Contains(text, "{{") {
			t.Errorf("%s: ScanText left a placeholder unfilled: %q", lang, text)
		}
		for _, want := range []string{"Go", "3", "14"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: ScanText = %q, want it to carry %q", lang, text, want)
			}
		}
	}
}

// TestScanRendererWritesOneLinePerEventInPlainMode covers the piped case: a
// line per SDK, newline-terminated, and no carriage returns that would show up
// as ^M in a log file.
func TestScanRendererWritesOneLinePerEventInPlainMode(t *testing.T) {
	out := &bytes.Buffer{}
	r := NewScanRenderer(Options{Mode: ModePlain}, scanCatalog(t, "en"), out)

	r.Render(scanEvent(sdk.NodeJS, 1, 2))
	r.Render(scanEvent(sdk.Dart, 2, 2))
	r.Close()

	if got, want := out.String(), "Checking Node.js (1/2)...\nChecking Dart (2/2)...\n"; got != want {
		t.Errorf("out = %q, want %q", got, want)
	}
	if strings.Contains(out.String(), "\r") {
		t.Errorf("out = %q carries a carriage return, which is a tty-only trick", out)
	}
}

// TestScanRendererRedrawsOneLineInTTYMode pins the in-place redraw, including
// the padding that erases the tail of a longer message: without it, scanning
// from "Checking Flutter" to "Checking Go" would leave "Checking Gorutter" on
// screen.
func TestScanRendererRedrawsOneLineInTTYMode(t *testing.T) {
	t.Run("a shorter message is padded out", func(t *testing.T) {
		out := &bytes.Buffer{}
		r := NewScanRenderer(Options{Mode: ModeTTY}, nil, out)

		r.Render(sdk.ScanProgress{SdkType: sdk.Python, DisplayName: "Python"})
		r.Render(sdk.ScanProgress{SdkType: sdk.Golang, DisplayName: "Go"})
		r.Close()

		if got, want := out.String(), "\rPython\rGo    \n"; got != want {
			t.Errorf("out = %q, want %q", got, want)
		}
		if got := strings.Count(out.String(), "\n"); got != 1 {
			t.Errorf("out = %q has %d newlines, want the single one Close adds", out, got)
		}
	})

	t.Run("a longer message needs no padding", func(t *testing.T) {
		out := &bytes.Buffer{}
		r := NewScanRenderer(Options{Mode: ModeTTY}, nil, out)

		r.Render(sdk.ScanProgress{DisplayName: "Go"})
		r.Render(sdk.ScanProgress{DisplayName: "Python"})
		r.Close()

		if got, want := out.String(), "\rGo\rPython\n"; got != want {
			t.Errorf("out = %q, want %q", got, want)
		}
	})

	t.Run("colour does not change the measured width", func(t *testing.T) {
		out := &bytes.Buffer{}
		r := NewScanRenderer(Options{Mode: ModeTTY, Color: true}, nil, out)

		r.Render(sdk.ScanProgress{DisplayName: "Python"})
		r.Render(sdk.ScanProgress{DisplayName: "Go"})
		r.Close()

		// The padding is four cells either way: escape bytes occupy none, which
		// is what DisplayWidth exists to say.
		if got, want := stripANSI(out.String()), "\rPython\rGo    \n"; got != want {
			t.Errorf("stripped out = %q, want %q", got, want)
		}
		if !strings.Contains(out.String(), ansiDim) || !strings.Contains(out.String(), ansiReset) {
			t.Errorf("out = %q, want the line dimmed like the progress line", out)
		}
	})
}

// TestScanRendererEmitsThePayloadVerbatimInJSONMode is the machine half: one
// JSON object per line, and the same object the GUI receives over the Wails
// bus, so a consumer that understands one understands the other.
func TestScanRendererEmitsThePayloadVerbatimInJSONMode(t *testing.T) {
	out := &bytes.Buffer{}
	r := NewScanRenderer(Options{Mode: ModeJSON}, scanCatalog(t, "zh"), out)

	events := []sdk.ScanProgress{scanEvent(sdk.NodeJS, 1, 14), scanEvent(sdk.Rust, 5, 14)}
	for _, event := range events {
		r.Render(event)
	}
	r.Close()

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != len(events) {
		t.Fatalf("got %d lines, want one per event: %q", len(lines), out)
	}
	for i, line := range lines {
		var decoded sdk.ScanProgress
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("line %d = %q is not JSON: %v", i, line, err)
		}
		if decoded != events[i] {
			t.Errorf("line %d decoded to %+v, want %+v", i, decoded, events[i])
		}
	}
	// The localized sentence is decoration; a JSON consumer asked for fields.
	if strings.Contains(out.String(), "正在检查") {
		t.Errorf("out = %q carries prose in JSON mode", out)
	}
}

// TestScanRendererIsSilentWhenAskedToBe covers --quiet, where nothing at all
// may reach stderr: a quiet run is the one a caller wraps in another program's
// output, and a stray scan line would land in the middle of it.
func TestScanRendererIsSilentWhenAskedToBe(t *testing.T) {
	for _, mode := range []Mode{ModeTTY, ModePlain, ModeJSON} {
		out := &bytes.Buffer{}
		r := NewScanRenderer(Options{Mode: mode, Quiet: true}, scanCatalog(t, "zh"), out)

		r.Render(scanEvent(sdk.NodeJS, 1, 14))
		r.Close()

		if out.Len() != 0 {
			t.Errorf("mode %v: out = %q, want nothing under --quiet", mode, out)
		}
	}
}

func TestScanRendererCloseIsIdempotentAndLazy(t *testing.T) {
	t.Run("nothing rendered, nothing closed", func(t *testing.T) {
		out := &bytes.Buffer{}
		r := NewScanRenderer(Options{Mode: ModeTTY}, nil, out)
		r.Close()
		if out.Len() != 0 {
			t.Errorf("out = %q, want no newline for a scan that never drew", out)
		}
	})

	t.Run("closing twice writes one newline", func(t *testing.T) {
		out := &bytes.Buffer{}
		r := NewScanRenderer(Options{Mode: ModeTTY}, nil, out)
		r.Render(sdk.ScanProgress{DisplayName: "Go"})
		r.Close()
		r.Close()
		if got := strings.Count(out.String(), "\n"); got != 1 {
			t.Errorf("out = %q has %d newlines, want 1", out, got)
		}
	})

	t.Run("the other modes already ended their lines", func(t *testing.T) {
		for _, mode := range []Mode{ModePlain, ModeJSON} {
			out := &bytes.Buffer{}
			r := NewScanRenderer(Options{Mode: mode}, scanCatalog(t, "en"), out)
			r.Render(scanEvent(sdk.Golang, 1, 1))
			r.Close()
			if got := strings.Count(out.String(), "\n"); got != 1 {
				t.Errorf("mode %v: out = %q has %d newlines, want 1", mode, out, got)
			}
		}
	})
}

// TestScanRendererIsSafeUnbuilt covers the nil receiver, which is what a
// command that never bootstrapped holds. A read-only command with no scan
// still routes events through the sink, so a nil renderer must drop them
// quietly rather than panic inside a service callback.
func TestScanRendererIsSafeUnbuilt(t *testing.T) {
	var unbuilt *ScanRenderer
	unbuilt.Render(scanEvent(sdk.NodeJS, 1, 14))
	unbuilt.Close()

	// A nil writer is replaced rather than dereferenced, so a command that
	// produces no scan still constructs its renderer without a nil check at
	// every call site.
	discarding := NewScanRenderer(Options{Mode: ModePlain}, scanCatalog(t, "en"), nil)
	if discarding == nil {
		t.Fatal("NewScanRenderer returned nil, want a renderer that discards")
	}
	discarding.Render(scanEvent(sdk.NodeJS, 1, 1))
	discarding.Close()
}

// TestBootstrapRoutesScanEventsToStderr pins the wiring end to end: an event a
// service emits through the runtime bootstrap built has to reach the scan
// renderer, on stderr, and never the result stream. This is the assertion that
// keeps `svc list --json | jq` working while the scan runs.
//
// The event goes through r.runtime rather than through a freshly built
// r.eventSink(). That is the whole point: a sink captured before the renderer
// existed would hold nil forever and drop every scan silently, and a test that
// rebuilds the sink cannot see it.
func TestBootstrapRoutesScanEventsToStderr(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if _, err := r.bootstrap(appcore.LevelRead); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer r.shutdown()

	if r.scan == nil {
		t.Fatal("bootstrap did not build a scan renderer")
	}
	if r.runtime == nil {
		t.Fatal("bootstrap did not build a runtime")
	}
	if r.scan.out != io.Writer(s.stderr) {
		t.Error("the scan renderer does not write to stderr")
	}

	r.runtime.EventsEmit(EventScanProgress, scanEvent(sdk.NodeJS, 1, 14))
	if s.stdout.Len() != 0 {
		t.Errorf("the result stream got %q, want nothing while a scan runs", s.stdout)
	}
	if s.stderr.Len() == 0 {
		t.Fatal("stderr is empty: the runtime bootstrap wired dropped the scan event")
	}
	if strings.Contains(s.stderr.String(), "sidebar.") {
		t.Errorf("stderr = %q leaked a catalog key", s.stderr)
	}
	if !strings.Contains(s.stderr.String(), "Node.js") {
		t.Errorf("stderr = %q, want the scan line for the SDK that was emitted", s.stderr)
	}
}
