package cli

import (
	"github.com/spf13/cobra"

	"svc/internal/sdk"
)

// The package-manager half of `svc pkg`: the cards the GUI's detail panel
// renders for one SDK, and the two operations each card offers.
//
// Everything in this file and in cmd_npm.go is a port of DetailPanel's
// package-managers section (fetchPackageManagers, handlePmInstall,
// handlePmUpdate) plus the Node.js settings modal it opens, whose two halves --
// NpmRegistrySection and GlobalPackagesSection -- are the `registry` and
// `global` subcommands.
//
// One deliberate departure from the command table in note/cli-design.md §4.2:
// the npm-scoped commands are `svc pkg registry` and `svc pkg global`, not
// `svc npm ...`. §4.6 forbids a subcommand named after a binary an SDK
// installs, and `npm` is exactly that -- a Node.js release ships npm, the shim
// manager therefore creates a shim by that name, and
// TestNoCommandNameCollidesWithAnSdkBinary fails the build if a root command
// shadows one. Keeping the npm-scoped commands under `pkg` also answers the
// question two group names would split: `svc pkg --help` lists the managers,
// the registry and the global packages together, which is the one panel the
// GUI shows them in.
//
// The whole group bootstraps at LevelRead, the mutating commands included.
// LevelWrite is the level permitted to edit the shell rc file and the Windows
// registry PATH, and nothing in internal/pkgmgr does either, nor does it write
// any other svc file: it runs package managers against the active SDK and lets
// them write where they have always written (~/.npmrc and the node prefix).
// LevelWrite would also run EnsureSetup and empty the download cache on the way
// in, which is a side effect no command here has a reason to want.
//
// The four mutations still take the operation lock. `npm install -g` writes
// inside <svc>/nodejs/<version>/, which is the same tree `svc uninstall`
// deletes and `svc switch` repoints, and a lock that only covered svc's own
// config files would leave that race open.

// PackageResult is what a mutating `svc pkg` command reports through --json.
//
// Neither of the two envelopes this package already has fits. MutationResult's
// SdkType/Version/Active fields describe one version of an SDK becoming the
// active one, and nothing here changes what is active; CleanResult counts what
// disappeared, and nothing here is counted. So the group has its own
// three-field envelope: which operation ran, which kind of thing it ran on,
// and the name of that thing.
type PackageResult struct {
	// Action is the operation: install, update or uninstall.
	Action string `json:"action"`
	// Target says which of the two kinds of package the action was about, so a
	// consumer never has to infer it from the command line it happened to run.
	Target string `json:"target"`
	// Name is the package manager or the npm package, spelled the way the user
	// spelled it: the service is what decides whether that name means
	// anything, and echoing it back is how a script confirms what was acted on.
	Name string `json:"name"`
}

// actionUpdate is the one Action value this group adds to the set
// MutationResult defined -- no SDK-lifecycle command updates in place, so the
// word was not taken. install and uninstall reuse those constants rather than
// defining identical ones.
const actionUpdate = "update"

// The PackageResult.Target values.
const (
	// pkgTargetManager is a package manager: npm, yarn, composer, pip.
	pkgTargetManager = "package-manager"
	// pkgTargetGlobal is one globally installed npm package.
	pkgTargetGlobal = "global-package"
)

// reportPackage writes one PackageResult and the sentence that belongs with
// it. Every caller passes a `detail.*` key for that sentence, because each of
// them is a toast the GUI already shows; a CLI that grew its own wording for
// "yarn installed successfully" is a CLI that can drift from the window.
func (r *runner) reportPackage(result PackageResult, summary string) error {
	if err := r.printer.JSON(result); err != nil {
		return err
	}
	r.printLine("%s", summary)
	return nil
}

// emptyManagers is emptyIfNil for the package-manager list. GetPackageManagers
// returns nil both for an SDK it has no cards for and for one with no active
// version, and `null` on the result stream is a shape every consumer would
// have to defend against for no reason.
func emptyManagers(managers []sdk.PackageManagerInfo) []sdk.PackageManagerInfo {
	if managers == nil {
		return []sdk.PackageManagerInfo{}
	}
	return managers
}

func (r *runner) newPkgCommand() *cobra.Command {
	pkg := &cobra.Command{
		Use:   "pkg",
		Short: "Manage package managers, the npm registry and global npm packages",
		Long: "Manage the package managers that belong to an SDK, and the npm-scoped\n" +
			"settings of the active Node.js.\n" +
			"\n" +
			"  svc pkg list <sdk>              the managers of one SDK and their versions\n" +
			"  svc pkg install|update <name>   install or update one of them\n" +
			"  svc pkg registry [set <url>]    show or switch the npm registry\n" +
			"  svc pkg global ...              the globally installed npm packages\n" +
			"\n" +
			"The name is `pkg` rather than `npm` for the last two: a subcommand may not\n" +
			"share a name with a binary an SDK installs, and every Node.js ships npm.",
	}
	pkg.AddCommand(
		r.newPkgListCommand(),
		r.newPkgInstallCommand(),
		r.newPkgUpdateCommand(),
		r.newRegistryCommand(),
		r.newGlobalCommand(),
	)
	return pkg
}

func (r *runner) newPkgListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list <sdk>",
		Short: "List the package managers of an SDK",
		Long: "List the package managers that belong to one SDK, with the version each\n" +
			"reports and whether it is there at all.\n" +
			"\n" +
			"Node.js has six (npm, yarn, pnpm, cnpm, nrm, corepack), PHP has composer and\n" +
			"Python has pip. Every other SDK has none, and says so rather than printing an\n" +
			"empty table.\n" +
			"\n" +
			"This is a probe, not a look at svc's config: each manager's own --version\n" +
			"command runs with the active version of its SDK scoped onto PATH, so a\n" +
			"manager installed by something else still shows up. It follows that an SDK\n" +
			"with no active version has nothing to probe, and the note says so.",
		Args:              r.exactArgs(1),
		ValidArgsFunction: r.sdkThenNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			kind, err := r.resolveSdk(core, args[0])
			if err != nil {
				return err
			}
			managers := core.PkgMgr.GetPackageManagers(string(kind))
			if err := r.printer.JSON(emptyManagers(managers)); err != nil {
				return err
			}
			r.printPackageManagers(kind, managers)
			// The reason an empty list is empty is worth one line, and it is a
			// note rather than a result: a pipe asked for the managers and got
			// an empty array, which is the whole answer.
			if len(managers) == 0 && core.Home.Cfg.GetActiveVersion(string(kind)) == "" {
				r.printer.Note("%s", r.printer.T("cli.noActiveVersion",
					map[string]string{"sdk": sdk.SdkDisplayName(kind)}))
			}
			return nil
		},
	}
}

func (r *runner) printPackageManagers(kind sdk.SdkType, managers []sdk.PackageManagerInfo) {
	if len(managers) == 0 {
		r.printLine("%s", r.printer.T("cli.noPackageManagers",
			map[string]string{"sdk": sdk.SdkDisplayName(kind)}))
		return
	}
	table := Table{
		Columns: []Column{
			{Header: r.printer.T("detail.packageManagers", nil)},
			{Header: r.printer.T("cli.columnVersion", nil)},
			{Header: r.printer.T("cli.columnState", nil)},
		},
	}
	for _, manager := range managers {
		version, state := placeholderNone, r.printer.T("cli.stateNotInstalled", nil)
		if manager.Installed {
			version, state = manager.Version, r.printer.T("detail.installed", nil)
			if version == "" {
				version = placeholderNone
			}
		}
		table.Rows = append(table.Rows, []string{manager.Name, version, state})
	}
	r.printer.Table(table)
}

func (r *runner) newPkgInstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "install <name>",
		Short: "Install a package manager",
		Long: "Install one of the package managers svc knows: yarn, pnpm, cnpm, nrm or\n" +
			"corepack for Node.js.\n" +
			"\n" +
			"Three names are refused, and the reason is the service's rather than this\n" +
			"command's: npm ships inside every Node.js and pip inside every Python, so\n" +
			"there is nothing to install, and composer is a manual download whose URL the\n" +
			"error carries.\n" +
			"\n" +
			"yarn and pnpm go through corepack when the active Node.js has it, and fall\n" +
			"back to 'npm install -g' when it does not or it fails. Nothing is asked\n" +
			"before installing -- the GUI's card does not ask either -- and whether the\n" +
			"manager is already there is not checked first: finding out means running up\n" +
			"to six --version probes, and the answer would change nothing, because\n" +
			"installing over a manager that exists replaces it.",
		Args:              r.exactArgs(1),
		ValidArgsFunction: r.noFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			core, err := r.readCore()
			if err != nil {
				return err
			}
			err = r.withOpLock(core, func() error {
				return core.PkgMgr.InstallPackageManager(name)
			})
			if err != nil {
				return err
			}
			return r.reportPackage(
				PackageResult{Action: actionInstall, Target: pkgTargetManager, Name: name},
				r.printer.T("detail.pmInstallSuccess", map[string]string{"name": name}),
			)
		},
	}
}

func (r *runner) newPkgUpdateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "update <name>",
		Short: "Update a package manager to its latest version",
		Long: "Update one package manager to the latest version: 'npm install -g\n" +
			"<name>@latest' for the Node.js ones, 'composer self-update' for composer and\n" +
			"'python -m pip install --upgrade pip' for pip.\n" +
			"\n" +
			"yarn and pnpm go through corepack first, exactly as install does. Unlike\n" +
			"install, every name svc knows is updatable -- npm and pip included, which is\n" +
			"the only way to move either of them.\n" +
			"\n" +
			"Whether the manager is installed is not checked first, for the reason\n" +
			"'svc pkg install' gives. For the npm-installed ones the distinction is\n" +
			"academic: an update of a manager that is missing installs it. composer and\n" +
			"pip have no such fallback and report their command as missing instead.",
		Args:              r.exactArgs(1),
		ValidArgsFunction: r.noFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			core, err := r.readCore()
			if err != nil {
				return err
			}
			err = r.withOpLock(core, func() error {
				return core.PkgMgr.UpdatePackageManager(name)
			})
			if err != nil {
				return err
			}
			return r.reportPackage(
				PackageResult{Action: actionUpdate, Target: pkgTargetManager, Name: name},
				r.printer.T("detail.pmUpdateSuccess", map[string]string{"name": name}),
			)
		},
	}
}
