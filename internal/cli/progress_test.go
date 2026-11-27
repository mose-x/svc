package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"svc/internal/cli/i18n"
	"svc/internal/sdk"
	"svc/internal/update"
)

// progressZh and progressEn mirror the shipped catalogs' progress block. The
// guards in progress_guard_test.go check the real files still have every key
// these tests rely on; keeping the behavioural tests on a local copy means a
// wording change in the GUI cannot break them.
const (
	progressZh = `{
	  "progress": {
	    "downloading": "下载中...",
	    "downloadingPercent": "下载中... {{percent}}%",
	    "extracting": "正在解压...",
	    "verifying": "验证完整性中...",
	    "configuring_path": "正在配置环境变量...",
	    "done": "安装配置完成！",
	    "error": "下载失败: {{error}}",
	    "updateDownloading": "下载中...",
	    "updateDone": "下载完成"
	  }
	}`
	progressEn = `{
	  "progress": {
	    "downloading": "Downloading...",
	    "downloadingPercent": "Downloading... {{percent}}%",
	    "extracting": "Extracting...",
	    "verifying": "Verifying integrity...",
	    "configuring_path": "Configuring environment...",
	    "done": "Installation complete!",
	    "error": "Download failed: {{error}}",
	    "updateDownloading": "Downloading...",
	    "updateDone": "Download complete"
	  }
	}`
)

func progressCatalog(t *testing.T, lang string) *i18n.Catalog {
	t.Helper()
	catalog, err := i18n.New(map[string][]byte{
		i18n.DefaultLang:  []byte(progressZh),
		i18n.FallbackLang: []byte(progressEn),
	}, lang)
	if err != nil {
		t.Fatalf("i18n.New(%q): %v", lang, err)
	}
	return catalog
}

func event(stage string, downloaded, total int64) sdk.InstallProgress {
	return sdk.InstallProgress{
		SdkType:         sdk.SdkType("go"),
		Version:         "1.23.0",
		Stage:           stage,
		DownloadedBytes: downloaded,
		TotalBytes:      total,
	}
}

func TestProgressText(t *testing.T) {
	cases := []struct {
		name     string
		progress sdk.InstallProgress
		wantZh   string
		wantEn   string
	}{
		{
			name:     "downloading with a known total",
			progress: event(StageDownloading, 42, 100),
			wantZh:   "下载中... 42%",
			wantEn:   "Downloading... 42%",
		},
		{
			name:     "downloading with an unknown total",
			progress: event(StageDownloading, 42, 0),
			wantZh:   "下载中...",
			wantEn:   "Downloading...",
		},
		{
			name:     "downloading nothing yet",
			progress: event(StageDownloading, 0, 100),
			wantZh:   "下载中... 0%",
			wantEn:   "Downloading... 0%",
		},
		{
			name:     "extracting",
			progress: event(StageExtracting, 0, 0),
			wantZh:   "正在解压...",
			wantEn:   "Extracting...",
		},
		{
			name:     "verifying",
			progress: event(StageVerifying, 0, 0),
			wantZh:   "验证完整性中...",
			wantEn:   "Verifying integrity...",
		},
		{
			name:     "configuring the path",
			progress: event(StageConfiguringPath, 0, 0),
			wantZh:   "正在配置环境变量...",
			wantEn:   "Configuring environment...",
		},
		{
			name:     "done",
			progress: event(StageDone, 0, 0),
			wantZh:   "安装配置完成！",
			wantEn:   "Installation complete!",
		},
		{
			name: "error carries the backend message",
			progress: func() sdk.InstallProgress {
				p := event(StageError, 0, 0)
				p.Message = "connection reset"
				return p
			}(),
			wantZh: "下载失败: connection reset",
			wantEn: "Download failed: connection reset",
		},
		{
			name: "an unknown stage falls back to the message",
			progress: func() sdk.InstallProgress {
				p := event("rebooting", 0, 0)
				p.Message = "waiting for the package manager"
				return p
			}(),
			wantZh: "waiting for the package manager",
			wantEn: "waiting for the package manager",
		},
		{
			name:     "an unknown stage with no message renders empty, as the GUI does",
			progress: event("rebooting", 0, 0),
			wantZh:   "",
			wantEn:   "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ProgressText(progressCatalog(t, "zh"), c.progress); got != c.wantZh {
				t.Errorf("zh: ProgressText = %q, want %q", got, c.wantZh)
			}
			if got := ProgressText(progressCatalog(t, "en"), c.progress); got != c.wantEn {
				t.Errorf("en: ProgressText = %q, want %q", got, c.wantEn)
			}
		})
	}
}

// TestProgressTextIgnoresThePercentField pins the port of the GUI's quirk:
// DetailPanel recomputes the percentage from the byte counters and never reads
// progress.percent. If the two disagree -- and the backend does send a rounded
// Percent -- the CLI must show the byte-derived one, because that is the number
// the bar next to it is drawn from and the number the window shows.
func TestProgressTextIgnoresThePercentField(t *testing.T) {
	progress := event(StageDownloading, 42, 100)
	progress.Percent = 99
	if got := ProgressText(progressCatalog(t, "zh"), progress); got != "下载中... 42%" {
		t.Errorf("ProgressText = %q, want the byte-derived percentage", got)
	}
}

// TestProgressTextWithoutACatalog must degrade to the backend message instead
// of panicking: a renderer built before the catalog loaded should still be
// usable by a service callback that fires early.
func TestProgressTextWithoutACatalog(t *testing.T) {
	progress := event(StageDone, 0, 0)
	progress.Message = "finished"
	if got := ProgressText(nil, progress); got != "finished" {
		t.Errorf("ProgressText(nil, ...) = %q, want %q", got, "finished")
	}
}

func TestDownloadPercent(t *testing.T) {
	cases := []struct {
		name              string
		downloaded, total int64
		want              int
	}{
		{"nothing yet", 0, 1000, 0},
		{"a tenth", 100, 1000, 10},
		{"floor, not round", 199, 1000, 19},
		{"complete", 1000, 1000, 100},
		// No clamping, exactly like the GUI: a server that sends more bytes
		// than Content-Length shows the truth rather than a pretty 100%.
		{"more than the total", 1500, 1000, 150},
		{"unknown total", 500, 0, 0},
		{"negative total", 500, -1, 0},
		{"negative downloaded", -500, 1000, 0},
		{"one byte of a terabyte", 1, 1 << 40, 0},
		{"whole terabyte", 1 << 40, 1 << 40, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DownloadPercent(c.downloaded, c.total); got != c.want {
				t.Errorf("DownloadPercent(%d, %d) = %d, want %d", c.downloaded, c.total, got, c.want)
			}
		})
	}
}

func TestBytesText(t *testing.T) {
	withSpeed := event(StageDownloading, 19_200_000, 45_700_000)
	withSpeed.SpeedBytesPerSec = 2_200_000

	noSpeed := event(StageDownloading, 19_200_000, 45_700_000)
	noTotal := event(StageDownloading, 19_200_000, 0)
	noTotal.SpeedBytesPerSec = 2_200_000

	// A stage that is not downloading shows no counters at all, even when the
	// event still carries them: the GUI hides the same span.
	extracting := withSpeed
	extracting.Stage = StageExtracting

	cases := []struct {
		name     string
		progress sdk.InstallProgress
		want     string
	}{
		{
			name:     "downloaded, total and speed",
			progress: withSpeed,
			want:     "18.3 MB / 43.6 MB  2.1 MB/s",
		},
		{
			name:     "no speed reported",
			progress: noSpeed,
			want:     "18.3 MB / 43.6 MB",
		},
		{
			name:     "no total reported",
			progress: noTotal,
			want:     "18.3 MB  2.1 MB/s",
		},
		{
			name:     "nothing downloaded yet",
			progress: event(StageDownloading, 0, 45_700_000),
			want:     "",
		},
		{
			name:     "a non-download stage shows no counters",
			progress: extracting,
			want:     "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BytesText(c.progress); got != c.want {
				t.Errorf("BytesText = %q, want %q", got, c.want)
			}
		})
	}
}

func TestProgressBar(t *testing.T) {
	cases := []struct {
		percent    int
		width      int
		wantFilled int
		wantEmpty  int
	}{
		{0, 16, 0, 16},
		{6, 16, 0, 16}, // 6% of 16 is 0.96, truncated: an empty bar until 1/16
		{50, 16, 8, 8},
		{99, 16, 15, 1}, // never full before it is finished
		{100, 16, 16, 0},
		{-20, 16, 0, 16}, // clamped
		{250, 16, 16, 0}, // clamped
		{50, 0, 0, 0},    // no bar at all
		{50, -4, 0, 0},
		{50, 1, 0, 1},
	}
	for _, c := range cases {
		bar := ProgressBar(c.percent, c.width)
		filled := strings.Count(bar, barFilled)
		empty := strings.Count(bar, barEmpty)
		if filled != c.wantFilled || empty != c.wantEmpty {
			t.Errorf("ProgressBar(%d, %d) = %q (%d filled, %d empty), want %d filled and %d empty",
				c.percent, c.width, bar, filled, empty, c.wantFilled, c.wantEmpty)
		}
		if c.width > 0 && utf8.RuneCountInString(bar) != c.width {
			t.Errorf("ProgressBar(%d, %d) is %d cells wide, want %d",
				c.percent, c.width, utf8.RuneCountInString(bar), c.width)
		}
		if c.width <= 0 && bar != "" {
			t.Errorf("ProgressBar(%d, %d) = %q, want an empty string", c.percent, c.width, bar)
		}
	}
}

func TestToneForStage(t *testing.T) {
	cases := map[string]Tone{
		StageDownloading:     TonePlain,
		StageExtracting:      TonePlain,
		StageVerifying:       TonePlain,
		StageConfiguringPath: TonePlain,
		StageDone:            ToneOK,
		StageError:           ToneError,
		"something-new":      TonePlain,
	}
	for stage, want := range cases {
		if got := ToneForStage(stage); got != want {
			t.Errorf("ToneForStage(%q) = %d, want %d", stage, got, want)
		}
	}
}

func TestProgressLineString(t *testing.T) {
	t.Run("every part present", func(t *testing.T) {
		line := ProgressLine{Text: "下载中... 42%", Bar: ProgressBar(42, 4), Bytes: "18.3 MB / 43.6 MB"}
		want := "下载中... 42%  " + ProgressBar(42, 4) + "  18.3 MB / 43.6 MB"
		if got := line.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	})
	t.Run("empty parts are dropped with their separator", func(t *testing.T) {
		if got := (ProgressLine{Text: "正在解压..."}).String(); got != "正在解压..." {
			t.Errorf("String() = %q, want no trailing separators", got)
		}
		if got := (ProgressLine{Text: "下载中...", Bytes: "1.0 KB"}).String(); got != "下载中...  1.0 KB" {
			t.Errorf("String() = %q, want the text and the counters two spaces apart", got)
		}
		if got := (ProgressLine{}).String(); got != "" {
			t.Errorf("String() = %q, want empty", got)
		}
	})
	t.Run("colour wraps the whole line", func(t *testing.T) {
		cases := []struct {
			tone Tone
			code string
		}{
			{TonePlain, ansiDim},
			{ToneOK, ansiGreen},
			{ToneError, ansiRed},
		}
		for _, c := range cases {
			line := ProgressLine{Text: "text", Color: true, Tone: c.tone}
			want := c.code + "text" + ansiReset
			if got := line.String(); got != want {
				t.Errorf("tone %d: String() = %q, want %q", c.tone, got, want)
			}
		}
	})
	t.Run("plainWidth ignores the colour codes", func(t *testing.T) {
		colored := ProgressLine{Text: "text", Bar: "bar", Color: true, Tone: ToneError}
		plain := ProgressLine{Text: "text", Bar: "bar"}
		if colored.plainWidth() != plain.plainWidth() {
			t.Errorf("plainWidth with colour = %d, without = %d", colored.plainWidth(), plain.plainWidth())
		}
		if got := plain.plainWidth(); got != len("text  bar") {
			t.Errorf("plainWidth = %d, want %d", got, len("text  bar"))
		}
		if got := (ProgressLine{}).plainWidth(); got != 0 {
			t.Errorf("plainWidth of an empty line = %d, want 0", got)
		}
	})
}

func TestProgressRendererTTY(t *testing.T) {
	var out bytes.Buffer
	renderer := NewProgressRenderer(Options{Mode: ModeTTY}, progressCatalog(t, "zh"), &out)

	renderer.Render(event(StageDownloading, 42, 100))
	renderer.Render(event(StageDownloading, 42, 100)) // identical frame: dropped
	renderer.Render(event(StageDownloading, 50, 100))
	renderer.Render(event(StageExtracting, 0, 0)) // much shorter: must be padded
	renderer.Close()
	renderer.Close() // a second Close must not add a second newline

	written := out.String()
	if strings.Count(written, "\n") != 1 {
		t.Fatalf("tty output holds %d newlines, want exactly one from Close:\n%q", strings.Count(written, "\n"), written)
	}
	if !strings.HasSuffix(written, "\n") {
		t.Error("Close must terminate the redrawn line")
	}

	body := strings.TrimSuffix(written, "\n")
	frames := strings.Split(body, "\r")
	if frames[0] != "" {
		t.Errorf("tty output must start with a carriage return, got %q", frames[0])
	}
	frames = frames[1:]
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3 (the identical redraw is dropped):\n%q", len(frames), frames)
	}
	if !strings.Contains(frames[0], "42%") || !strings.Contains(frames[1], "50%") {
		t.Errorf("frames do not carry the percentages: %q", frames)
	}
	if !strings.Contains(frames[0], barFilled) {
		t.Errorf("the tty frame has no bar: %q", frames[0])
	}
	if strings.Contains(frames[2], barFilled) {
		t.Errorf("a stage with no total must not draw a bar: %q", frames[2])
	}

	// The shorter frame is padded out to the width of the widest one, so no
	// characters from the previous redraw survive on the line.
	if !strings.HasSuffix(frames[2], " ") {
		t.Errorf("the shorter frame was not padded: %q", frames[2])
	}
	if got, widest := utf8.RuneCountInString(frames[2]), utf8.RuneCountInString(frames[1]); got < widest {
		t.Errorf("the padded frame is %d cells wide, shorter than the previous %d", got, widest)
	}
}

// TestProgressRendererTTYPaddingUsesNoANSI pins the redraw mechanism: padding
// with spaces rather than the erase-to-end-of-line escape, because a legacy
// Windows conhost without virtual terminal processing prints that escape as
// garbage. Colour may add escapes; the redraw itself must not.
func TestProgressRendererTTYPaddingUsesNoANSI(t *testing.T) {
	var out bytes.Buffer
	renderer := NewProgressRenderer(Options{Mode: ModeTTY}, progressCatalog(t, "zh"), &out)
	renderer.Render(event(StageDownloading, 42, 100))
	renderer.Render(event(StageExtracting, 0, 0))
	renderer.Close()

	for _, frame := range strings.Split(out.String(), "\r")[1:] {
		if strings.Contains(frame, "\x1b[K") {
			t.Errorf("frame uses an ANSI erase sequence: %q", frame)
		}
	}
}

func TestProgressRendererPlain(t *testing.T) {
	var out bytes.Buffer
	renderer := NewProgressRenderer(Options{Mode: ModePlain}, progressCatalog(t, "zh"), &out)

	// 26 download events from 0% to 100% in steps of 4: one line for the stage
	// change plus one per decile, i.e. 11 lines instead of 26.
	for percent := int64(0); percent <= 100; percent += 4 {
		renderer.Render(event(StageDownloading, percent*10, 1000))
	}
	renderer.Render(event(StageExtracting, 0, 0))
	renderer.Render(event(StageExtracting, 0, 0)) // same stage: dropped

	failed := event(StageError, 0, 0)
	failed.Message = "connection reset"
	renderer.Render(failed)
	retried := event(StageError, 0, 0)
	retried.Message = "checksum mismatch"
	renderer.Render(retried) // a different failure: not dropped
	renderer.Close()         // plain lines are already terminated

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 14 {
		t.Fatalf("plain mode wrote %d lines, want 14:\n%s", len(lines), out.String())
	}
	if lines[0] != "下载中... 0%" {
		t.Errorf("first line = %q", lines[0])
	}
	for i, line := range lines[:11] {
		if !strings.HasPrefix(line, "下载中... ") {
			t.Errorf("line %d = %q, want a download line", i, line)
		}
		if strings.Contains(line, barFilled) || strings.Contains(line, barEmpty) {
			t.Errorf("line %d contains a bar; plain mode must not draw one: %q", i, line)
		}
	}
	// Every decile is represented exactly once, in order. The step is 4%, so
	// the line that opens a decile is not necessarily the one on its boundary.
	for decile := 0; decile <= 10; decile++ {
		percent := downloadPercentInLine(t, lines[decile])
		if percent/10 != decile {
			t.Errorf("line %d reports %d%%, which is not in decile %d", decile, percent, decile)
		}
		// The very first event has downloaded nothing yet, and the GUI shows
		// no counters for it either, so only the later lines must carry them.
		if decile > 0 && !strings.Contains(lines[decile], " B / 1000 B") {
			t.Errorf("line %d lost its byte counters: %q", decile, lines[decile])
		}
	}
	if lines[11] != "正在解压..." {
		t.Errorf("line 11 = %q, want the extracting stage", lines[11])
	}
	if lines[12] != "下载失败: connection reset" || lines[13] != "下载失败: checksum mismatch" {
		t.Errorf("distinct errors were not both written: %q, %q", lines[12], lines[13])
	}
	if strings.Contains(out.String(), "\r") {
		t.Error("plain mode must not emit carriage returns; a log file would fill with them")
	}
}

func TestProgressRendererJSON(t *testing.T) {
	events := []sdk.InstallProgress{
		event(StageDownloading, 4000, 10000),
		event(StageDownloading, 4001, 10000), // no decimation in json mode
		event(StageExtracting, 0, 0),
		event(StageDone, 0, 0),
	}
	var out bytes.Buffer
	renderer := NewProgressRenderer(
		Options{Mode: ModeJSON, Color: true}, // colour must never reach the stream
		progressCatalog(t, "zh"), &out)
	for _, progress := range events {
		renderer.Render(progress)
	}
	renderer.Close()

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != len(events) {
		t.Fatalf("json mode wrote %d lines, want one per event:\n%s", len(lines), out.String())
	}
	for i, line := range lines {
		var decoded sdk.InstallProgress
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("line %d is not valid JSON (%v): %q", i, err, line)
		}
		if decoded != events[i] {
			t.Errorf("line %d round-tripped to %+v, want %+v", i, decoded, events[i])
		}
		if strings.Contains(line, "\x1b") {
			t.Errorf("line %d contains an ANSI escape: %q", i, line)
		}
	}
}

func TestProgressRendererQuiet(t *testing.T) {
	for _, mode := range []Mode{ModeTTY, ModePlain, ModeJSON} {
		var out bytes.Buffer
		renderer := NewProgressRenderer(Options{Mode: mode, Quiet: true}, progressCatalog(t, "zh"), &out)
		renderer.Render(event(StageDownloading, 42, 100))
		renderer.Render(event(StageDone, 0, 0))
		renderer.Close()
		if out.Len() != 0 {
			t.Errorf("%v: --quiet wrote %q, want nothing", mode, out.String())
		}
	}
}

// TestProgressRendererSurvivesHostileWriters covers the three ways a renderer
// gets built without a usable stream. Progress is decoration: a nil writer, a
// broken pipe or a renderer nobody built at all must never fail the command.
func TestProgressRendererSurvivesHostileWriters(t *testing.T) {
	catalog := progressCatalog(t, "zh")

	t.Run("nil writer", func(t *testing.T) {
		renderer := NewProgressRenderer(Options{Mode: ModeTTY}, catalog, nil)
		renderer.Render(event(StageDownloading, 42, 100))
		renderer.Close()
	})
	t.Run("nil renderer", func(t *testing.T) {
		var renderer *ProgressRenderer
		renderer.Render(event(StageDownloading, 42, 100))
		renderer.Close()
	})
	t.Run("every write fails", func(t *testing.T) {
		broken := &failingWriter{}
		for _, mode := range []Mode{ModeTTY, ModePlain, ModeJSON} {
			renderer := NewProgressRenderer(Options{Mode: mode}, catalog, broken)
			renderer.Render(event(StageDownloading, 42, 100))
			renderer.Render(event(StageDone, 0, 0))
			renderer.Close()
		}
		if broken.calls == 0 {
			t.Error("the failing writer was never used; the test proves nothing")
		}
	})
}

type failingWriter struct{ calls int }

func (w *failingWriter) Write(payload []byte) (int, error) {
	w.calls++
	return 0, errors.New("broken pipe")
}

// TestProgressRendererNeverWritesResults pins the stdout/stderr split from the
// package contract: the renderer only ever touches the writer it was given, so
// a command that passes os.Stderr cannot leak progress into a piped result.
func TestProgressRendererNeverWritesResults(t *testing.T) {
	progressStream := &bytes.Buffer{}
	resultStream := &bytes.Buffer{}
	renderer := NewProgressRenderer(Options{Mode: ModePlain}, progressCatalog(t, "zh"), progressStream)
	renderer.Render(event(StageDownloading, 42, 100))
	renderer.Close()

	if progressStream.Len() == 0 {
		t.Fatal("the progress writer received nothing")
	}
	if resultStream.Len() != 0 {
		t.Errorf("the renderer wrote %q to a stream it was never given", resultStream.String())
	}
}

// downloadPercentInLine reads the percentage back out of a rendered Chinese
// download line ("下载中... 42%  ...").
func downloadPercentInLine(t *testing.T, line string) int {
	t.Helper()
	const prefix = "下载中... "
	rest, found := strings.CutPrefix(line, prefix)
	if !found {
		t.Fatalf("%q does not start with %q", line, prefix)
	}
	text, _, found := strings.Cut(rest, "%")
	if !found {
		t.Fatalf("%q has no percentage", line)
	}
	percent, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		t.Fatalf("%q does not carry a numeric percentage: %v", line, err)
	}
	return percent
}

// --- the self-update stream --------------------------------------------------
//
// update:progress carries a different struct than install:progress and the same
// stage tokens map to different sentences, so it has its own entry point into
// the renderer. What it shares is everything after that: the three modes, the
// tty redraw and dedup, the plain-mode decimation, and the rule that progress
// never reaches stdout.

// updateEvent builds one update:progress payload. The byte counters are set to
// values a renderer could show, so a test that asserts they are NOT shown is
// asserting a decision rather than an absence of data.
func updateEvent(stage string, percent int) update.UpdateProgress {
	return update.UpdateProgress{
		Stage:            stage,
		Percent:          percent,
		DownloadedBytes:  19_200_000,
		TotalBytes:       45_700_000,
		SpeedBytesPerSec: 2_200_000,
		Message:          "backend detail",
	}
}

func TestUpdateProgressText(t *testing.T) {
	cases := []struct {
		stage  string
		wantZh string
		wantEn string
	}{
		// The window's ternary, branch for branch. `done` is the one that
		// differs in words and not merely in key: the install panel says
		// "Installation complete!" where the update modal says "Download
		// complete", because the swap has not happened yet at that point.
		{StageDone, "下载完成", "Download complete"},
		// The downloading cases here carry no total, which is the one shape in
		// which the window's own sentence is used verbatim: with nothing to
		// count against there is no honest percentage to print.
		{StageDownloading, "下载中...", "Downloading..."},
		{StageVerifying, "验证完整性中...", "Verifying integrity..."},
	}
	for _, c := range cases {
		progress := update.UpdateProgress{Stage: c.stage, Message: "backend detail"}
		if got := UpdateProgressText(progressCatalog(t, "zh"), progress); got != c.wantZh {
			t.Errorf("zh: stage %q = %q, want %q", c.stage, got, c.wantZh)
		}
		if got := UpdateProgressText(progressCatalog(t, "en"), progress); got != c.wantEn {
			t.Errorf("en: stage %q = %q, want %q", c.stage, got, c.wantEn)
		}
	}
}

// TestUpdateProgressTextCarriesThePercentage is the one place the CLI says more
// than the window's ternary does, and it says more because the window says it
// somewhere else: antd renders the percentage beside the Progress bar, and a
// pipe has no bar. Without the number, plain mode writes eleven identical
// "Downloading..." lines and a CI log shows a download progressing without ever
// showing how far it got.
func TestUpdateProgressTextCarriesThePercentage(t *testing.T) {
	cases := []struct {
		percent    int
		totalBytes int64
		wantZh     string
		wantEn     string
	}{
		{42, 100, "下载中... 42%", "Downloading... 42%"},
		{0, 100, "下载中... 0%", "Downloading... 0%"},
		{100, 100, "下载中... 100%", "Downloading... 100%"},
		// No total: the modal's own sentence, because 0% of an unknown size
		// would be a claim rather than a measurement.
		{0, 0, "下载中...", "Downloading..."},
	}
	for _, c := range cases {
		progress := update.UpdateProgress{
			Stage:      StageDownloading,
			Percent:    c.percent,
			TotalBytes: c.totalBytes,
			Message:    "backend detail",
		}
		if got := UpdateProgressText(progressCatalog(t, "zh"), progress); got != c.wantZh {
			t.Errorf("zh: percent %d of %d = %q, want %q", c.percent, c.totalBytes, got, c.wantZh)
		}
		if got := UpdateProgressText(progressCatalog(t, "en"), progress); got != c.wantEn {
			t.Errorf("en: percent %d of %d = %q, want %q", c.percent, c.totalBytes, got, c.wantEn)
		}
	}
	// The percentage is the event's own, not one recomputed from the byte
	// counters: the window feeds downloadProgress.percent straight to its bar,
	// and the number beside it has to agree with the bar.
	progress := update.UpdateProgress{
		Stage:           StageDownloading,
		Percent:         42,
		DownloadedBytes: 1,
		TotalBytes:      1000,
	}
	if got := UpdateProgressText(progressCatalog(t, "en"), progress); got != "Downloading... 42%" {
		t.Errorf("UpdateProgressText recomputed the percentage from the byte counters: %q", got)
	}
}

// TestUpdateProgressTextUnknownStage pins the ternary's else branch: a stage the
// modal does not name shows the backend's own message rather than a key, so a
// stage added server-side degrades into readable text.
func TestUpdateProgressTextUnknownStage(t *testing.T) {
	progress := update.UpdateProgress{Stage: "unpacking", Message: "waiting for the antivirus"}
	if got := UpdateProgressText(progressCatalog(t, "zh"), progress); got != "waiting for the antivirus" {
		t.Errorf("UpdateProgressText for an unlisted stage = %q, want the backend message", got)
	}
}

// TestUpdateProgressTextWithoutACatalog: a renderer built before the catalog was
// resolved must degrade to the backend message instead of panicking or printing
// a key.
func TestUpdateProgressTextWithoutACatalog(t *testing.T) {
	progress := update.UpdateProgress{Stage: StageDownloading, Message: "Downloading 12.0MB / 43.6MB"}
	if got := UpdateProgressText(nil, progress); got != progress.Message {
		t.Errorf("UpdateProgressText with no catalog = %q, want the backend message", got)
	}
}

// TestRenderUpdateShowsNoByteCounters pins the parity decision. The event
// carries downloaded, total and speed, and the update modal shows none of them
// -- it renders a bar and one sentence. A terminal that added the counters would
// be reporting more than the window does, in the one place the two are supposed
// to read the same.
func TestRenderUpdateShowsNoByteCounters(t *testing.T) {
	var out bytes.Buffer
	renderer := NewProgressRenderer(Options{Mode: ModeTTY}, progressCatalog(t, "zh"), &out)
	renderer.RenderUpdate(updateEvent(StageDownloading, 42))
	renderer.Close()

	written := out.String()
	for _, absent := range []string{
		FormatBytes(19_200_000),
		FormatBytes(45_700_000),
		FormatBytes(2_200_000) + "/s",
	} {
		if strings.Contains(written, absent) {
			t.Errorf("the update line carries the byte counter %q the window does not show: %q", absent, written)
		}
	}
	// The percentage is still there: the modal feeds downloadProgress.percent
	// straight to its bar, and so does this.
	if !strings.Contains(written, strings.Repeat(barFilled, BarWidth*42/100)) {
		t.Errorf("the update line has no bar at 42%%: %q", written)
	}
}

// TestRenderUpdateJSON writes one line per event, verbatim: the payload is
// update.UpdateProgress as the service emitted it, which is the same object the
// window receives over the event bus.
func TestRenderUpdateJSON(t *testing.T) {
	var out bytes.Buffer
	renderer := NewProgressRenderer(Options{Mode: ModeJSON}, progressCatalog(t, "zh"), &out)
	renderer.RenderUpdate(updateEvent(StageDownloading, 42))
	renderer.RenderUpdate(updateEvent(StageDone, 100))
	renderer.Close()

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("json mode wrote %d lines, want one per event:\n%s", len(lines), out.String())
	}
	var first update.UpdateProgress
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("the first line is not an update.UpdateProgress document: %v\n%s", err, lines[0])
	}
	if first.Stage != StageDownloading || first.Percent != 42 || first.DownloadedBytes != 19_200_000 {
		t.Errorf("the json line does not carry the event verbatim: %+v", first)
	}
	// No envelope and no discriminator field: the two progress streams are
	// separable by the streams they are on, and by their own shapes.
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[1]), &document); err != nil {
		t.Fatalf("the second line is not an object: %v", err)
	}
	if got, want := topLevelKeys(document), jsonKeysOf(t, update.UpdateProgress{}); !slices.Equal(got, want) {
		t.Errorf("the json line marshals to %v, want the service struct's %v", got, want)
	}
}

// TestRenderUpdateTTY covers the redraw and the bar rules. Verifying keeps its
// bar because the window keeps its Progress up until the done event arrives, and
// done has none because that event swaps the modal for the "update ready" one.
func TestRenderUpdateTTY(t *testing.T) {
	var out bytes.Buffer
	renderer := NewProgressRenderer(Options{Mode: ModeTTY}, progressCatalog(t, "zh"), &out)

	renderer.RenderUpdate(updateEvent(StageDownloading, 42))
	renderer.RenderUpdate(updateEvent(StageDownloading, 42)) // identical frame: dropped
	renderer.RenderUpdate(updateEvent(StageDownloading, 50))
	renderer.RenderUpdate(updateEvent(StageVerifying, 100))
	renderer.RenderUpdate(updateEvent(StageDone, 100))
	renderer.Close()

	body := strings.TrimSuffix(out.String(), "\n")
	frames := strings.Split(body, "\r")[1:]
	if len(frames) != 4 {
		t.Fatalf("got %d frames, want 4 (the identical redraw is dropped):\n%q", len(frames), frames)
	}
	if !strings.Contains(frames[0], barFilled) || !strings.Contains(frames[1], barFilled) {
		t.Errorf("the downloading frames have no bar: %q", frames[:2])
	}
	if !strings.Contains(frames[2], barFilled) {
		t.Errorf("the verifying frame lost its bar, which the window still shows: %q", frames[2])
	}
	if strings.Contains(frames[3], barFilled) {
		t.Errorf("the done frame draws a bar the window has already replaced: %q", frames[3])
	}
	if want := progressCatalog(t, "zh").T("progress.updateDone", nil); !strings.Contains(frames[3], want) {
		t.Errorf("the done frame = %q, want it to carry %q", frames[3], want)
	}
}

// TestRenderUpdateTTYTone mirrors how the window colours the row: completion is
// green, work in progress is quiet.
func TestRenderUpdateTTYTone(t *testing.T) {
	var out bytes.Buffer
	renderer := NewProgressRenderer(Options{Mode: ModeTTY, Color: true}, progressCatalog(t, "zh"), &out)
	renderer.RenderUpdate(updateEvent(StageDone, 100))
	renderer.Close()
	if !strings.Contains(out.String(), ansiGreen) {
		t.Errorf("the done frame is not green: %q", out.String())
	}
}

// TestRenderUpdatePlain is the decimation rule applied to the update stream: one
// line per stage change and one per ten percent of the download, so a pipe or a
// CI log gets a readable dozen lines rather than a few hundred.
func TestRenderUpdatePlain(t *testing.T) {
	var out bytes.Buffer
	renderer := NewProgressRenderer(Options{Mode: ModePlain}, progressCatalog(t, "zh"), &out)

	for percent := 0; percent <= 100; percent += 4 {
		renderer.RenderUpdate(updateEvent(StageDownloading, percent))
	}
	renderer.RenderUpdate(updateEvent(StageDownloading, 100)) // same decile: dropped
	renderer.RenderUpdate(updateEvent(StageVerifying, 100))
	renderer.RenderUpdate(updateEvent(StageVerifying, 100)) // same stage: dropped
	renderer.RenderUpdate(updateEvent(StageDone, 100))
	renderer.Close()

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	// 11 download lines (0%%, 10%%, ... 100%%) plus verifying plus done.
	if len(lines) != 13 {
		t.Fatalf("plain mode wrote %d lines, want 13:\n%s", len(lines), out.String())
	}
	// Every one of the eleven carries a different number. That is the whole
	// reason the downloading line prints the percentage at all: decimated to one
	// line per decile and worded the same way each time, the log would show a
	// download progressing without ever showing how far it had got.
	//
	// The events step by four percent, so a decile is written at the first event
	// that reaches it -- 12% opens the second, not 10%.
	for i, want := range []string{"0%", "12%", "20%", "32%", "40%", "52%", "60%", "72%", "80%", "92%", "100%"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want it to carry %q", i, lines[i], want)
		}
		if !strings.HasPrefix(lines[i], "下载中... ") {
			t.Errorf("line %d = %q, want a download line", i, lines[i])
		}
	}
	if lines[len(lines)-2] != "验证完整性中..." {
		t.Errorf("second-to-last line = %q", lines[len(lines)-2])
	}
	if lines[len(lines)-1] != "下载完成" {
		t.Errorf("last line = %q, want the update modal's own done sentence", lines[len(lines)-1])
	}
	for _, line := range lines {
		if strings.Contains(line, barFilled) {
			t.Errorf("plain mode drew a bar: %q", line)
		}
	}
}

// TestRenderUpdateRespectsQuietAndNil: --quiet suppresses progress in every
// mode, and a renderer that was never built must not crash a service callback.
func TestRenderUpdateRespectsQuietAndNil(t *testing.T) {
	for _, mode := range []Mode{ModeTTY, ModePlain, ModeJSON} {
		var out bytes.Buffer
		renderer := NewProgressRenderer(Options{Mode: mode, Quiet: true}, progressCatalog(t, "zh"), &out)
		renderer.RenderUpdate(updateEvent(StageDownloading, 42))
		renderer.Close()
		if out.Len() != 0 {
			t.Errorf("mode %v wrote %q under --quiet", mode, out.String())
		}
	}
	var missing *ProgressRenderer
	missing.RenderUpdate(updateEvent(StageDownloading, 42))
	missing.Close()
}
