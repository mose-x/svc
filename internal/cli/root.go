package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"svc/internal/appcore"
	"svc/internal/cli/clirt"
	"svc/internal/cli/i18n"
)

// UsageError marks a mistake in how the command was written -- a missing
// argument, an unknown flag, a refusal to confirm without --yes -- as opposed
// to a failure while doing what was asked. It is what makes cobra's own parse
// errors exit 2 instead of 1 without string-matching their messages.
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }

func (e *UsageError) Unwrap() error { return e.Err }

// errNoCommand is reported when the tree is entered with flags but no command.
// `svc` with no arguments at all never gets here: main routes that to the GUI.
var errNoCommand = errors.New("no command given; run 'svc' with no arguments to launch the GUI, or 'svc --help' to list the commands")

// ExecuteInput is everything the command tree needs from the process. The
// streams are injected rather than reached for so the whole tree is testable
// with buffers, and OpenHome is injectable so a test can point the run at a
// temporary SVC home instead of the user's.
type ExecuteInput struct {
	// Args is argv without the program name.
	Args []string
	// Catalogs are the embedded frontend i18n catalogs keyed by language.
	// Only the root package can embed them.
	Catalogs map[string][]byte
	// AboutJSON is the embedded about.json the version info is parsed from.
	AboutJSON []byte
	// Stdin, Stdout and Stderr are the process streams. Stdout carries results
	// only; everything a human reads while waiting goes to Stderr.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// OpenHome builds the SVC home view. Nil means appcore.OpenHome.
	OpenHome func(aboutJSON []byte) (*appcore.Home, error)
	// Env overrides the environment the run resolves against. Nil means
	// CollectEnvironment, which probes the real descriptors; tests set it so
	// the resolved mode does not depend on whether the test binary happens to
	// be attached to a terminal.
	Env *Environment
}

// Execute runs one CLI invocation and returns the process exit code. It never
// calls os.Exit itself, so a test can assert the code a command would have
// produced.
func Execute(in ExecuteInput) int {
	r := &runner{in: in}
	if len(r.in.Catalogs) == 0 {
		// A caller that forgot to wire cliCatalogs() has no way to speak to the
		// user; fail loudly rather than printing raw catalog keys.
		stderr := r.in.Stderr
		if stderr == nil {
			stderr = io.Discard
		}
		fmt.Fprintln(stderr, "svc: no i18n catalogs were supplied")
		return ExitError
	}
	r.applyDefaults()
	defer r.shutdown()

	r.root = r.newRootCommand()
	r.usageTarget = r.root
	r.root.SetArgs(in.Args)
	return r.finish(r.root.Execute())
}

// runner carries the resolved state of one invocation. Fields are filled in
// order -- flags, then options, then the home, then the service graph -- and
// each stage is reachable on its own so it can be tested on its own.
type runner struct {
	in    ExecuteInput
	flags globalFlags

	opts        Options
	catalog     *i18n.Catalog
	printer     *Printer
	home        *appcore.Home
	core        *appcore.Core
	runtime     *clirt.Runtime
	progress    *ProgressRenderer
	scan        *ScanRenderer
	prompt      *Prompt
	stopSignals context.CancelFunc

	// root is the command tree, kept so a usage failure can print the usage of
	// the command that was actually being parsed.
	root *cobra.Command
	// usageTarget is that command; it defaults to root.
	usageTarget *cobra.Command
}

// globalFlags mirrors the persistent flags on the root command.
type globalFlags struct {
	json        bool
	quiet       bool
	noColor     bool
	yes         bool
	lang        string
	installPath string
}

func (r *runner) applyDefaults() {
	if r.in.OpenHome == nil {
		r.in.OpenHome = appcore.OpenHome
	}
	if r.in.Stdout == nil {
		r.in.Stdout = io.Discard
	}
	if r.in.Stderr == nil {
		r.in.Stderr = io.Discard
	}
	if r.in.Stdin == nil {
		r.in.Stdin = strings.NewReader("")
	}
}

// newRootCommand builds the command tree. Subcommands are attached by the
// packages that implement them; the root only owns the global flags and the
// option resolution every command shares.
//
// SilenceErrors and SilenceUsage are both set because this file renders errors
// itself: the message goes through the catalog, the exit code comes from the
// error's own type, and the usage text is printed to stderr only for a usage
// failure. Leaving SilenceUsage off would send cobra's usage dump to stdout --
// it writes to OutOrStderr, which is the writer set for help -- and a
// half-written result stream is exactly what the output contract forbids.
func (r *runner) newRootCommand() *cobra.Command {
	// Declared before its own literal because PersistentPreRunE has to compare
	// the command it was given against the root, and a closure cannot refer to
	// a variable that is still being initialised.
	var root *cobra.Command
	root = &cobra.Command{
		Use:   "svc",
		Short: "SDK version control",
		Long: "svc installs, switches and manages multiple versions of 14 SDKs.\n" +
			"Running svc with no arguments launches the graphical application.\n" +
			"\n" +
			"The commands that predate the CLI keep their original behaviour and\n" +
			"output, and are therefore not listed below:\n" +
			"\n" +
			"  svc init       One-time shim setup (creates ~/.svc/shims and .svc.rc)\n" +
			"  svc version    Print the version line\n" +
			"\n" +
			"After installing an SDK, its commands (node, go, java, ...) are\n" +
			"available in every new shell via the shims in ~/.svc/shims.",
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd == root {
				// Flags but no command: RunE reports the usage error, and
				// there is nothing to resolve options for yet.
				return nil
			}
			return r.resolveOptions()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return &UsageError{errNoCommand}
		},
	}
	// Help goes to stdout -- it is the result the user asked for -- while
	// errors and usage go to stderr.
	root.SetOut(r.in.Stdout)
	root.SetErr(r.in.Stderr)
	root.SetIn(r.in.Stdin)
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		r.usageTarget = cmd
		return &UsageError{err}
	})
	// Replaces cobra's legacyArgs. Setting Args at all stops Find from
	// producing its own untyped "unknown command" error, so the failure is
	// validated inside execute() instead -- where it can carry UsageError, exit
	// 2, and do so before PersistentPreRunE opens the SVC home.
	root.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		return &UsageError{fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())}
	}

	flags := root.PersistentFlags()
	flags.BoolVar(&r.flags.json, "json", false, "print machine-readable JSON on stdout")
	flags.BoolVarP(&r.flags.quiet, "quiet", "q", false, "print only results and errors")
	flags.BoolVar(&r.flags.noColor, "no-color", false, "never print ANSI colour sequences")
	flags.BoolVarP(&r.flags.yes, "yes", "y", false, "answer yes to every confirmation prompt")
	flags.StringVar(&r.flags.lang, "lang", "", "message language, zh or en (default: $SVC_LANG, then settings.json)")
	flags.StringVar(&r.flags.installPath, "install-path", "", "operate on this SVC home instead of the configured one; not saved")

	r.addCommands(root)
	return root
}

// resolveOptions reads the saved language, collects the environment and turns
// both plus the flags into the run configuration. It runs in
// PersistentPreRunE, not at construction time, so a parse failure never opens
// the SVC home or initialises the logger.
func (r *runner) resolveOptions() error {
	home, err := r.in.OpenHome(r.in.AboutJSON)
	if err != nil {
		return err
	}

	// settings.json lives at the fixed home location regardless of any install
	// path, so its language is read before the override is applied.
	settings := home.Settings.Get()
	env := r.in.Env
	if env == nil {
		collected := CollectEnvironment(settings.Language)
		env = &collected
	} else if env.SettingsLang == "" {
		env.SettingsLang = settings.Language
	}
	r.opts = Resolve(Inputs{
		JSON:        r.flags.json,
		Quiet:       r.flags.quiet,
		NoColor:     r.flags.noColor,
		Yes:         r.flags.yes,
		Lang:        r.flags.lang,
		InstallPath: r.flags.installPath,
		Environment: *env,
	})
	// Bootstrap prefers this over the saved InstallPath, so --install-path
	// works for a user who has a custom path configured in the GUI.
	home.InstallPathOverride = r.opts.InstallPath

	catalog, err := r.opts.Catalog(r.in.Catalogs)
	if err != nil {
		return err
	}
	r.home = home
	r.catalog = catalog
	r.printer = NewPrinter(r.opts, catalog, r.in.Stdout, r.in.Stderr)
	r.prompt = NewPrompt(r.in.Stdin, r.in.Stderr)
	return nil
}

// bootstrap assembles the service graph every command shares, through the same
// appcore.Bootstrap the GUI calls. Level decides how much side effect the
// command is allowed to have: read-only commands pass LevelRead and never touch
// the shell rc or the registry PATH.
//
// The context is the signal context, so the first Ctrl-C cancels the running
// operation and the services unwind instead of leaving a half-written version
// directory behind.
func (r *runner) bootstrap(level appcore.Level) (*appcore.Core, error) {
	if r.core != nil {
		return r.core, nil
	}
	if r.home == nil {
		if err := r.resolveOptions(); err != nil {
			return nil, err
		}
	}

	ctx, stop := clirt.SignalContext(context.Background())
	r.stopSignals = stop
	// The renderers are built before the runtime that routes to them, so the
	// first event a service emits already has somewhere to go.
	r.progress = NewProgressRenderer(r.opts, r.catalog, r.in.Stderr)
	r.scan = NewScanRenderer(r.opts, r.catalog, r.in.Stderr)
	r.runtime = clirt.New(ctx, r.eventSink())

	core, err := r.home.Bootstrap(ctx, r.runtime, level)
	if err != nil {
		stop()
		r.stopSignals = nil
		r.runtime = nil
		return nil, err
	}
	r.core = core
	return core, nil
}

// eventSink routes service events to this run's renderers.
//
// The self-update line is not wired yet: it belongs to `svc self update`, and
// gets its renderer when that command lands. A nil handler drops the event
// quietly, which is what RouteEvent is built to tolerate.
//
// The handlers are collected per event rather than once when the sink is
// built. Capturing them early is how a renderer that does not exist yet gets
// frozen into the closure as nil, and the result is silence that looks exactly
// like a service that emitted nothing: `svc list` would scan fourteen SDKs
// without saying so. Reading the fields at call time makes the ordering of
// bootstrap's assignments unable to break the wiring again.
func (r *runner) eventSink() clirt.EventSink {
	return func(eventName string, payload any) {
		handlers := EventHandlers{}
		if r.progress != nil {
			handlers.InstallProgress = r.progress.Render
		}
		if r.scan != nil {
			handlers.ScanProgress = r.scan.Render
		}
		RouteEvent(handlers, eventName, payload)
	}
}

// shutdown releases what the run opened, in the order that matters: the
// progress line gets its newline before the signal handlers are released, so a
// Ctrl-C arriving during teardown cannot leave the terminal mid-line.
func (r *runner) shutdown() {
	if r.scan != nil {
		r.scan.Close()
	}
	if r.progress != nil {
		r.progress.Close()
	}
	if r.stopSignals != nil {
		r.stopSignals()
		r.stopSignals = nil
	}
}

// finish turns the outcome of the run into an exit code, reporting the error
// first so the code and the message can never disagree.
func (r *runner) finish(err error) int {
	if err != nil {
		r.reportError(err)
	}
	if r.runtime != nil && r.runtime.QuitRequested() {
		// The updater launched the platform swap script, which replaces the
		// running binary. Leaving now is the success case, not a failure.
		return ExitOK
	}
	return ExitCodeForError(err)
}

// reportError writes the failure to stderr, translated when a catalog was
// resolved, and follows it with the usage of the command that was being parsed
// when -- and only when -- the failure was a usage one. A failure that happens
// before resolution (an unknown flag, an unopenable SVC home) has no catalog
// yet and is printed as-is.
func (r *runner) reportError(err error) {
	text := err.Error()
	if r.catalog != nil {
		text = translateWrapped(r.catalog, err)
	}
	fmt.Fprintf(r.in.Stderr, "svc: %s\n", text)

	var usage *UsageError
	if errors.As(err, &usage) {
		target := r.usageTarget
		if target == nil {
			target = r.root
		}
		if target != nil {
			fmt.Fprint(r.in.Stderr, target.UsageString())
		}
	}
}

// translateWrapped renders the apperr marker inside err and keeps any prose the
// caller wrapped around it.
//
// The catalog's own RenderErrorText is anchored on purpose: it is the exact Go
// counterpart of errMsg() in frontend/src/utils/error.ts, and the Wails bus only
// ever carries a bare marker. A Go error chain wraps, so an anchored match here
// would hand the user the raw "[svc:...]{...}" text while ExitCodeForError --
// which walks the same tree -- still reported the right code. Walking first and
// replacing the marker in place keeps the two consistent and keeps the caller's
// context, which is the whole reason the wrap was added.
func translateWrapped(catalog *i18n.Catalog, err error) string {
	raw := err.Error()
	marker, ok := firstMarker(err)
	if !ok {
		return raw
	}
	return strings.Replace(raw, marker, catalog.RenderErrorText(marker), 1)
}
