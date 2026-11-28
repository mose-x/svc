package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"svc/internal/appcore"
	"svc/internal/apperr"
	"svc/internal/cli/clirt"
	"svc/internal/config"
	"svc/internal/sdk"
)

// closedHome is an OpenHome that fails the test if the command layer reaches
// it. Every usage failure below is asserted through it, which is how "a parse
// failure never opens the SVC home, never reads settings.json and never
// initialises the logger" is turned into an executable claim rather than a
// comment.
func closedHome(t *testing.T) func([]byte) (*appcore.Home, error) {
	t.Helper()
	return func([]byte) (*appcore.Home, error) {
		t.Error("the run opened the SVC home for an invocation that should have failed while parsing")
		return nil, errors.New("the SVC home must not be opened here")
	}
}

func TestExecuteWithoutCatalogsFailsLoudly(t *testing.T) {
	s := newStreams()
	code := Execute(ExecuteInput{
		Args:     []string{"list"},
		Stdin:    s.stdin,
		Stdout:   s.stdout,
		Stderr:   s.stderr,
		OpenHome: closedHome(t),
	})

	if code != ExitError {
		t.Errorf("Execute without catalogs = %d, want %d", code, ExitError)
	}
	if !strings.Contains(s.stderr.String(), "no i18n catalogs") {
		t.Errorf("stderr = %q, want it to name the missing catalogs", s.stderr)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("the result stream got %q, want nothing", s.stdout)
	}
}

// TestExecuteUsageFailuresNeverOpenTheHome covers the three ways an invocation
// can be wrong before any work starts: no command, an unknown command, an
// unknown or truncated flag. All three are exit 2 with usage on stderr and
// nothing at all on stdout -- a half-written result stream is what the output
// contract forbids.
func TestExecuteUsageFailuresNeverOpenTheHome(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"flags but no command", []string{"--json"}, "no command given"},
		{"a misspelled command", []string{"instal"}, `unknown command "instal"`},
		{"an unknown flag first", []string{"--jsonn", "list"}, "unknown flag"},
		{"an unknown flag after the command", []string{"list", "--jsonn"}, "unknown flag"},
		{"a flag missing its value", []string{"--lang"}, "flag needs an argument"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStreams()
			code := Execute(ExecuteInput{
				Args:     c.args,
				Catalogs: shippedCatalogs(t),
				Stdin:    s.stdin,
				Stdout:   s.stdout,
				Stderr:   s.stderr,
				OpenHome: closedHome(t),
				Env:      &Environment{},
			})

			if code != ExitUsage {
				t.Errorf("Execute(%q) = %d, want %d", c.args, code, ExitUsage)
			}
			stderr := s.stderr.String()
			if !strings.Contains(stderr, c.want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, c.want)
			}
			if !strings.HasPrefix(stderr, "svc: ") {
				t.Errorf("stderr = %q, want the failure to carry the 'svc: ' prefix", stderr)
			}
			if !strings.Contains(stderr, "Usage:") {
				t.Errorf("stderr = %q, want a usage failure to print usage", stderr)
			}
			if s.stdout.Len() != 0 {
				t.Errorf("the result stream got %q, want nothing", s.stdout)
			}
		})
	}
}

// TestExecuteHelpIsAResultNotAnError pins the stream split for the one output
// the root command produces today. Help is what the user asked for, so it is a
// result: exit 0, stdout, and stderr left empty.
func TestExecuteHelpIsAResultNotAnError(t *testing.T) {
	s := newStreams()
	code := Execute(ExecuteInput{
		Args:     []string{"--help"},
		Catalogs: shippedCatalogs(t),
		Stdin:    s.stdin,
		Stdout:   s.stdout,
		Stderr:   s.stderr,
		OpenHome: closedHome(t),
		Env:      &Environment{},
	})

	if code != ExitOK {
		t.Errorf("Execute(--help) = %d, want %d", code, ExitOK)
	}
	// cobra prints Long when it is set and never falls back to Short, so the
	// assertion is on the long description and on the usage block.
	if !strings.Contains(s.stdout.String(), "svc installs, switches and manages") {
		t.Errorf("stdout = %q, want the root command's help text", s.stdout)
	}
	if !strings.Contains(s.stdout.String(), "Usage:") {
		t.Errorf("stdout = %q, want the usage block", s.stdout)
	}
	if !strings.Contains(s.stdout.String(), "--install-path") {
		t.Errorf("stdout = %q, want the global flags to be listed", s.stdout)
	}
	if s.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want it empty for a successful help", s.stderr)
	}
}

// newRunner builds a runner whose flags are already set, so the stages below
// cobra -- option resolution, bootstrap, finish -- can each be driven on their
// own. P2b has no real subcommand yet, which is exactly why the stages are
// tested directly instead of through Execute.
func newRunner(t *testing.T, s *streams, flags globalFlags, openHome func([]byte) (*appcore.Home, error)) *runner {
	t.Helper()
	r := &runner{in: ExecuteInput{
		Catalogs: shippedCatalogs(t),
		Stdin:    s.stdin,
		Stdout:   s.stdout,
		Stderr:   s.stderr,
		OpenHome: openHome,
		// Injected so the resolved mode never depends on whether the test
		// binary happens to be attached to a terminal.
		Env: &Environment{},
	}}
	r.applyDefaults()
	r.flags = flags
	return r
}

func TestResolveOptionsLanguagePrecedence(t *testing.T) {
	cases := []struct {
		name     string
		flag     string
		envLang  string
		settings string
		want     string
	}{
		{"nothing set speaks the default", "", "", "", "zh"},
		{"settings.json alone", "", "", "en", "en"},
		{"$SVC_LANG beats settings.json", "", "en", "zh", "en"},
		{"--lang beats $SVC_LANG", "zh", "en", "en", "zh"},
		{"a region subtag is normalised", "", "zh-CN", "", "zh"},
		{"upper case is normalised", "", "EN", "", "en"},
		{"an unshipped language falls back", "", "klingon", "zh", "en"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStreams()
			r := newRunner(t, s, globalFlags{lang: c.flag}, tempHome(t, func(home string) {
				writeSettings(t, home, config.AppSettings{Language: c.settings})
			}))
			r.in.Env.EnvLang = c.envLang

			if err := r.resolveOptions(); err != nil {
				t.Fatalf("resolveOptions: %v", err)
			}
			if r.opts.Lang != c.want {
				t.Errorf("Lang = %q, want %q", r.opts.Lang, c.want)
			}
			if r.catalog == nil {
				t.Fatal("resolveOptions did not build a catalog")
			}
			if got := r.catalog.Lang(); got != c.want {
				t.Errorf("the catalog speaks %q, want %q", got, c.want)
			}
			if r.prompt == nil {
				t.Error("resolveOptions did not build a prompt")
			}
		})
	}
}

// TestResolveOptionsReportsTheOutputContract checks the mode and colour a run
// lands on when nothing claims a terminal: results are being piped, so plain
// mode with no colour, and --json overrides even that.
func TestResolveOptionsReportsTheOutputContract(t *testing.T) {
	cases := []struct {
		name      string
		flags     globalFlags
		wantMode  Mode
		wantColor bool
		wantYes   bool
	}{
		{"a pipe is plain and colourless", globalFlags{}, ModePlain, false, false},
		{"--json is json", globalFlags{json: true}, ModeJSON, false, false},
		{"--quiet carries through", globalFlags{quiet: true}, ModePlain, false, false},
		{"--no-color carries through", globalFlags{noColor: true}, ModePlain, false, false},
		{"--yes carries through", globalFlags{yes: true}, ModePlain, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRunner(t, newStreams(), c.flags, tempHome(t, nil))
			if err := r.resolveOptions(); err != nil {
				t.Fatalf("resolveOptions: %v", err)
			}
			if r.opts.Mode != c.wantMode {
				t.Errorf("Mode = %v, want %v", r.opts.Mode, c.wantMode)
			}
			if r.opts.Color != c.wantColor {
				t.Errorf("Color = %v, want %v", r.opts.Color, c.wantColor)
			}
			if r.opts.Yes != c.wantYes {
				t.Errorf("Yes = %v, want %v", r.opts.Yes, c.wantYes)
			}
			if r.opts.Quiet != c.flags.quiet {
				t.Errorf("Quiet = %v, want %v", r.opts.Quiet, c.flags.quiet)
			}
			if r.opts.StdinTTY || r.opts.StdoutTTY {
				t.Error("an injected non-terminal environment must not resolve to a TTY")
			}
		})
	}
}

// TestResolveOptionsAppliesTheInstallPathOverride is the CLI half of
// --install-path: the flag has to reach both the resolved options and the Home,
// because Bootstrap reads the override off the Home and not off the options.
// appcore_test.go asserts the other half -- that Bootstrap then really moves
// SvcDir and prefers the override over settings.json.
func TestResolveOptionsAppliesTheInstallPathOverride(t *testing.T) {
	t.Run("the flag reaches the home", func(t *testing.T) {
		override := t.TempDir()
		r := newRunner(t, newStreams(), globalFlags{installPath: override}, tempHome(t, func(home string) {
			writeSettings(t, home, config.AppSettings{InstallPath: t.TempDir()})
		}))

		if err := r.resolveOptions(); err != nil {
			t.Fatalf("resolveOptions: %v", err)
		}
		if r.opts.InstallPath != override {
			t.Errorf("opts.InstallPath = %q, want %q", r.opts.InstallPath, override)
		}
		if r.home.InstallPathOverride != override {
			t.Errorf("home.InstallPathOverride = %q, want %q; Bootstrap would ignore the flag", r.home.InstallPathOverride, override)
		}
	})

	t.Run("without the flag nothing is overridden", func(t *testing.T) {
		r := newRunner(t, newStreams(), globalFlags{}, tempHome(t, nil))
		if err := r.resolveOptions(); err != nil {
			t.Fatalf("resolveOptions: %v", err)
		}
		if r.opts.InstallPath != "" {
			t.Errorf("opts.InstallPath = %q, want it empty", r.opts.InstallPath)
		}
		if r.home.InstallPathOverride != "" {
			t.Errorf("home.InstallPathOverride = %q, want it empty", r.home.InstallPathOverride)
		}
	})
}

func TestResolveOptionsReportsAnUnopenableHome(t *testing.T) {
	want := errors.New("initialize config: no home directory")
	r := newRunner(t, newStreams(), globalFlags{}, func([]byte) (*appcore.Home, error) { return nil, want })

	err := r.resolveOptions()
	if !errors.Is(err, want) {
		t.Fatalf("resolveOptions = %v, want %v", err, want)
	}
	if r.home != nil || r.catalog != nil || r.opts.Lang != "" {
		t.Error("a failed resolution must leave the runner untouched")
	}
}

// TestBootstrapRoutesServiceEventsToTheRenderer is the wiring test: the whole
// point of clirt is that a service emitting over wailsrt.Runtime reaches a
// terminal renderer instead of a Wails bus. It drives the real path --
// Bootstrap, the returned Core's runtime, EventsEmit -- and asserts translated
// progress landed on stderr.
func TestBootstrapRoutesServiceEventsToTheRenderer(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	defer r.shutdown()

	core, err := r.bootstrap(appcore.LevelRead)
	if err != nil {
		t.Fatalf("bootstrap(LevelRead): %v", err)
	}
	if core.RT == nil {
		t.Fatal("the bootstrapped Core has no runtime")
	}

	core.RT.EventsEmit(EventInstallProgress, sdk.InstallProgress{
		Stage:           StageDownloading,
		DownloadedBytes: 42,
		TotalBytes:      100,
	})

	stderr := s.stderr.String()
	// zh is the default catalog, so the shipped wording is the assertion: a
	// rename in the GUI shows up here rather than as a silent terminal.
	if !strings.Contains(stderr, "下载中") || !strings.Contains(stderr, "42%") {
		t.Errorf("stderr = %q, want the translated download line with its percentage", stderr)
	}
	if strings.Contains(stderr, "progress.") {
		t.Errorf("stderr = %q leaked a catalog key", stderr)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("progress reached stdout (%q); it belongs on stderr", s.stdout)
	}

	// An event the CLI deliberately does not render must not blow up the
	// service callback either.
	core.RT.EventsEmit(EventVersionsRefreshed, map[string]any{"sdkType": "go"})
	core.RT.EventsEmit("a brand new event", nil)
}

// TestBootstrapIsBuiltOnceAndLazily covers the two properties a command relies
// on: bootstrap resolves the options itself when nothing has yet, and a second
// call returns the same graph instead of re-running the startup self-heal.
func TestBootstrapIsBuiltOnceAndLazily(t *testing.T) {
	r := newRunner(t, newStreams(), globalFlags{}, tempHome(t, nil))
	defer r.shutdown()

	if r.home != nil {
		t.Fatal("a fresh runner must not have opened the home yet")
	}
	first, err := r.bootstrap(appcore.LevelRead)
	if err != nil {
		t.Fatalf("bootstrap(LevelRead): %v", err)
	}
	if r.home == nil || r.catalog == nil || r.runtime == nil || r.progress == nil {
		t.Error("bootstrap left part of the run unassembled")
	}
	if r.stopSignals == nil {
		t.Error("bootstrap did not install the signal handler")
	}

	second, err := r.bootstrap(appcore.LevelWrite)
	if err != nil {
		t.Fatalf("the second bootstrap: %v", err)
	}
	if second != first {
		t.Error("the second bootstrap rebuilt the service graph")
	}
}

// TestBootstrapFailureReleasesTheSignalHandler pins the teardown on the error
// path: a failed Bootstrap must not leave a registered signal handler behind,
// because Execute's deferred shutdown only stops what bootstrap recorded.
// LevelMeta is the one failure a command layer can provoke without a test-only
// seam -- appcore rejects it outright.
func TestBootstrapFailureReleasesTheSignalHandler(t *testing.T) {
	r := newRunner(t, newStreams(), globalFlags{}, tempHome(t, nil))
	defer r.shutdown()

	if _, err := r.bootstrap(appcore.LevelMeta); err == nil {
		t.Fatal("bootstrap(LevelMeta) = nil error, want appcore to reject it")
	}
	if r.stopSignals != nil {
		t.Error("a failed bootstrap left the signal handler registered")
	}
	if r.runtime != nil || r.core != nil {
		t.Error("a failed bootstrap left a runtime or a Core behind")
	}
}

func TestFinishReturnsOKAfterARequestedQuit(t *testing.T) {
	r := newRunner(t, newStreams(), globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}
	r.runtime = clirt.New(nil, nil)

	if got := r.finish(nil); got != ExitOK {
		t.Errorf("finish(nil) before a quit = %d, want %d", got, ExitOK)
	}

	r.runtime.Quit()
	if !r.runtime.QuitRequested() {
		t.Fatal("Quit did not record the request")
	}
	// The updater handed the process to the platform swap script: leaving now
	// is success, and an error on the way out must not renumber it.
	if got := r.finish(nil); got != ExitOK {
		t.Errorf("finish(nil) after a quit = %d, want %d", got, ExitOK)
	}
	if got := r.finish(errors.New("the swap script is running")); got != ExitOK {
		t.Errorf("finish(err) after a quit = %d, want %d", got, ExitOK)
	}
}

// TestFinishSettlesTheProgressLineBeforeAnError is the error half of the same
// rule the Printer keeps on the success path: reportError writes to the stream
// the tty progress line is being redrawn on, so a failure raised mid-download
// would otherwise land on the last frame's own line, behind its padding.
func TestFinishSettlesTheProgressLineBeforeAnError(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	r.progress = NewProgressRenderer(Options{Mode: ModeTTY}, progressCatalog(t, "zh"), s.stderr)
	r.progress.Render(event(StageDownloading, 42, 100))

	if got := r.finish(errors.New("the download stalled")); got != ExitError {
		t.Errorf("finish(err) = %d, want %d", got, ExitError)
	}

	written := s.stderr.String()
	if !strings.Contains(written, "\nsvc: the download stalled\n") {
		t.Errorf("the error did not start a fresh line:\n%q", written)
	}
	if got := strings.Count(written, "\n"); got != 2 {
		t.Errorf("stderr holds %d newlines, want the settled frame's and the error's:\n%q", got, written)
	}

	// Execute's deferred shutdown closes the same line again and must add
	// nothing: settling early may not cost a blank line at the end.
	r.shutdown()
	if got := s.stderr.String(); got != written {
		t.Errorf("shutdown wrote %d more bytes after finish had settled the line:\n%q", len(got)-len(written), got)
	}
}

// TestReportErrorTranslatesAndSeparatesUsage covers the two shapes of failure
// text: a service failure is rendered through the catalog with no marker left
// in it and no usage dumped, while a usage failure keeps its message and adds
// the usage of the command that was being parsed.
func TestReportErrorTranslatesAndSeparatesUsage(t *testing.T) {
	// Every shape a service failure reaches the shell in. The bare marker is
	// what the GUI sees; the wrapped ones are what idiomatic Go produces, and
	// they are the shapes an anchored match would leak through untranslated.
	marker := apperr.New(apperr.UnknownSdkType, map[string]string{"sdk": "go"})
	shapes := []struct {
		name string
		err  error
		want string
	}{
		{"a bare marker", marker, "svc: 未知的 SDK 类型：go\n"},
		{"wrapped once", fmt.Errorf("install go 1.23: %w", marker), "svc: install go 1.23: 未知的 SDK 类型：go\n"},
		{"wrapped twice", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", marker)), "svc: outer: inner: 未知的 SDK 类型：go\n"},
		{"joined", errors.Join(errors.New("first"), marker), "svc: first\n未知的 SDK 类型：go\n"},
		{"a plain error is left alone", errors.New("dial tcp: refused"), "svc: dial tcp: refused\n"},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			s := newStreams()
			r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
			if err := r.resolveOptions(); err != nil {
				t.Fatalf("resolveOptions: %v", err)
			}

			r.reportError(shape.err)

			stderr := s.stderr.String()
			if stderr != shape.want {
				t.Errorf("stderr = %q, want %q", stderr, shape.want)
			}
			if strings.Contains(stderr, "[svc:") {
				t.Errorf("stderr = %q leaked an apperr marker", stderr)
			}
			if strings.Contains(stderr, "Usage:") {
				t.Errorf("stderr = %q dumped usage for a failure that was not about usage", stderr)
			}
		})
	}

	t.Run("a usage failure adds usage", func(t *testing.T) {
		s := newStreams()
		r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
		if err := r.resolveOptions(); err != nil {
			t.Fatalf("resolveOptions: %v", err)
		}
		r.root = r.newRootCommand()
		r.usageTarget = r.root

		r.reportError(&UsageError{errNoCommand})

		stderr := s.stderr.String()
		if !strings.Contains(stderr, "no command given") {
			t.Errorf("stderr = %q, want the usage message", stderr)
		}
		if !strings.Contains(stderr, "Usage:") {
			t.Errorf("stderr = %q, want usage for a usage failure", stderr)
		}
	})

	t.Run("an unresolved failure is printed as-is", func(t *testing.T) {
		s := newStreams()
		r := newRunner(t, s, globalFlags{}, closedHome(t))

		r.reportError(errors.New("dial tcp: connection refused"))

		if got := s.stderr.String(); !strings.Contains(got, "svc: dial tcp: connection refused") {
			t.Errorf("stderr = %q, want the raw message with the prefix", got)
		}
	})
}
