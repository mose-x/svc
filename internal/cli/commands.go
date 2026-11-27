package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"svc/internal/appcore"
	"svc/internal/apperr"
	"svc/internal/sdk"
	"svc/internal/svclock"
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
		r.newVersionsCommand(),
		r.newInstallCommand(),
		r.newReinstallCommand(),
		r.newSwitchCommand(),
		r.newUninstallCommand(),
		r.newImportCommand(),
		r.newURLCommand(),
		r.newConflictsCommand(),
		r.newPathCommand(),
		r.newStorageCommand(),
		r.newCleanCommand(),
		r.newLogsCommand(),
		r.newPkgCommand(),
		r.newSettingsCommand(),
		r.newEndpointsCommand(),
		r.newDoctorCommand(),
		// Hidden: the background carrier `svc versions` spawns. Registered last
		// because it is the one command no user is ever meant to type.
		r.newRefreshCommand(),
	)
}

// readCore bootstraps the read-only service graph. Every command in this phase
// is a read, so none of them may reach LevelWrite: that is the level which
// edits the shell rc file or the Windows registry PATH, and a command that
// only reports state has no business touching either.
func (r *runner) readCore() (*appcore.Core, error) {
	return r.bootstrap(appcore.LevelRead)
}

// writeCore bootstraps the mutating service graph. It is the only path by which
// a command reaches appcore.LevelWrite, the level permitted to edit the shell rc
// file and the Windows registry PATH; keeping the call in one place is what makes
// "these commands write" a property a reviewer can check by reading addCommands.
func (r *runner) writeCore() (*appcore.Core, error) {
	return r.bootstrap(appcore.LevelWrite)
}

// withOpLock runs fn while holding the SVC dir's cross-process operation lock.
//
// config.json, shims.json and the shell rc are all read-modify-write, and the
// in-process mutex inside config.Config cannot see another process. This is the
// CLI's half of the fix: two terminals can no longer clobber each other, and a
// terminal next to the GUI is serialized as soon as the GUI's bindings take the
// same lock. Wiring that half is a separate change, and deliberately so -- the
// lock is not re-entrant, so every one of the GUI's mutating bindings has to be
// checked for calls into another binding before any of them can take it.
//
// For the same reason the lock is taken here, by the command layer, and never by
// a service: installer calls storage, so a lock taken inside either would contend
// with itself. Each mutating command takes it once, at the outermost point of one
// user-visible operation.
//
// Contention is a pause rather than a failure unless --no-wait says otherwise:
// the other holder is doing the same kind of work and will finish. The wait is
// announced through Note, so a pipe and a --json consumer both see nothing.
func (r *runner) withOpLock(core *appcore.Core, fn func() error) error {
	dir := core.Home.Cfg.SvcDir()
	if r.opts.NoWait {
		lock, err := svclock.TryAcquire(dir)
		if err != nil {
			if svclock.IsBusy(err) {
				return apperr.New(apperr.OperationInProgress, nil)
			}
			return err
		}
		defer lock.Release()
		return fn()
	}
	return svclock.With(dir, func() {
		r.printer.Note("%s", r.printer.T("cli.waitingForLock", nil))
	}, fn)
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

// atLeastArgs is the same wrapper over cobra's MinimumNArgs, for the commands
// whose arity has a floor and no ceiling.
func (r *runner) atLeastArgs(count int) cobra.PositionalArgs {
	return r.usageArgs(cobra.MinimumNArgs(count))
}

// usageError reports a mistake in how the command was written, recording cmd as
// the usage target so the usage printed alongside it is the subcommand's own
// rather than the root's. A command that validates its own flags needs this:
// cobra sets the target for the parse failures it produces, but not for the ones
// a RunE discovers after parsing succeeded.
func (r *runner) usageError(cmd *cobra.Command, err error) error {
	r.usageTarget = cmd
	return &UsageError{err}
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

// sdkThenNothing completes the first positional argument of a `<sdk> <version>`
// command and offers nothing for the second. A version can only be completed
// from a remote list, and a completion function that reached for the network
// would hang the shell on every press of Tab -- which is worse than no
// candidates, because the shell is blocked while it waits.
func (r *runner) sdkThenNothing(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return r.sdkCompletion(cmd, args, toComplete)
}

// sdkThenFile is sdkThenNothing for a `<sdk> <path>` command: the first
// argument is still an SDK identifier, but the second is a path, and a path is
// the one thing the shell itself completes better than svc could. Returning
// ShellCompDirectiveDefault is what hands the line back to the shell's own
// filename completion instead of suppressing it.
func (r *runner) sdkThenFile(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveDefault
	}
	return r.sdkCompletion(cmd, args, toComplete)
}

// noFileCompletion offers nothing and, more importantly, suppresses the
// shell's own filename completion. It is what a command uses for an argument
// that names neither a file nor an SDK -- a package manager, an npm package --
// where the default of handing the line back to the shell would fill the
// argument with paths from the working directory. Listing the directories
// around a user who typed `svc pkg install ` is not a worse answer than no
// candidates, it is a wrong one.
func (r *runner) noFileCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
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
