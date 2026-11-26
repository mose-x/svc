package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"svc/internal/appcore"
	"svc/internal/sdk"
)

// addCommands attaches every command the CLI owns to the root.
//
// They are listed here rather than self-registering through an init() so the
// whole command surface is visible in one place: a reviewer can see what a
// release ships, and the name-collision guard in dispatch_test.go enumerates
// exactly this tree. It is also the reason P2b's cobra root had no `help`
// command -- InitDefaultHelpCmd returns early while the root has no
// subcommands.
func (r *runner) addCommands(root *cobra.Command) {
	root.AddCommand(
		r.newListCommand(),
		r.newStatusCommand(),
		r.newCurrentCommand(),
		r.newDirCommand(),
		r.newURLCommand(),
		r.newConflictsCommand(),
		r.newPathCommand(),
		r.newStorageCommand(),
		r.newLogsCommand(),
		r.newSettingsCommand(),
		r.newEndpointsCommand(),
	)
}

// readCore bootstraps the read-only service graph. Every command in this phase
// is a read, so none of them may reach LevelWrite: that is the level which
// edits the shell rc file or the Windows registry PATH, and a command that
// only reports state has no business touching either.
func (r *runner) readCore() (*appcore.Core, error) {
	return r.bootstrap(appcore.LevelRead)
}

// resolveSdk turns one positional argument into a registered SDK type. On
// failure it also emits the list of accepted spellings -- to stderr, through
// Note, so it reaches a human at a terminal and never a script or a JSON
// consumer. The error itself stays the service layer's unknown-sdk-type
// marker, which is what maps it to exit code 3.
func (r *runner) resolveSdk(core *appcore.Core, input string) (sdk.SdkType, error) {
	kind, err := ResolveSdkType(core.Registry, input)
	if err != nil {
		if hint := SdkTypeHint(core.Registry); hint != "" {
			r.printer.Note("%s", r.printer.T("cli.availableSdks", map[string]string{"list": hint}))
		}
		return "", err
	}
	return kind, nil
}

// The positional-argument validators below wrap cobra's own so that a wrong
// arity becomes a UsageError. That is not cosmetic: cobra returns a plain
// fmt.Errorf, which ExitCodeForError would classify as ExitError (1), while a
// mistyped command line is a usage mistake and must exit 2 -- the same code an
// unknown flag gets. Each wrapper also records the command as the usage
// target, so the usage printed alongside the error is the subcommand's and not
// the root's.

func (r *runner) noArgs() cobra.PositionalArgs {
	return r.usageArgs(cobra.NoArgs)
}

func (r *runner) exactArgs(count int) cobra.PositionalArgs {
	return r.usageArgs(cobra.ExactArgs(count))
}

func (r *runner) rangeArgs(minimum, maximum int) cobra.PositionalArgs {
	return r.usageArgs(cobra.RangeArgs(minimum, maximum))
}

func (r *runner) usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			r.usageTarget = cmd
			return &UsageError{err}
		}
		return nil
	}
}

// sdkCompletion offers the registered SDK identifiers to the shell.
//
// Cobra runs completion through a hidden `__complete` invocation that skips
// PersistentPreRunE, so nothing is resolved yet and the runner has to do it
// itself. It deliberately does NOT bootstrap: Bootstrap self-heals SDK layouts
// and refreshes shims, which is far too much work -- and far too many writes
// to ~/.svc -- for something that happens on every press of Tab. Building the
// registry directly is pure construction over the already-resolved config.
//
// Every failure degrades to "no candidates". A completion function that
// returned an error or printed a diagnostic would have that text inserted into
// the user's command line.
func (r *runner) sdkCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	registry := r.completionRegistry()
	if registry == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	prefix := strings.ToLower(toComplete)
	var matches []string
	for _, name := range SdkTypeNames(registry) {
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			matches = append(matches, name)
		}
	}
	return matches, cobra.ShellCompDirectiveNoFileComp
}

// completionRegistry resolves the run configuration and builds a registry
// without bootstrapping the service graph. It returns nil when the SVC home
// cannot be opened at all, which is the "no candidates" signal.
func (r *runner) completionRegistry() *sdk.Registry {
	if r.home == nil {
		if err := r.resolveOptions(); err != nil {
			return nil
		}
	}
	if r.home == nil || r.home.Cfg == nil {
		return nil
	}
	return sdk.NewRegistry(r.home.Cfg, r.home.Settings)
}
