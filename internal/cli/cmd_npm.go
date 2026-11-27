package cli

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"svc/internal/apperr"
	"svc/internal/pkgmgr"
	"svc/internal/sdk"
)

// The npm-scoped half of `svc pkg`: the registry the active Node.js installs
// from, and the packages installed globally into it. These are the two
// sections of the GUI's Node.js settings modal, and like that modal every
// command here needs an active Node.js -- the service says so with its own
// need-sdk marker, which is why none of them checks for one first.

// RegistryResult is what `svc pkg registry` reports through --json, both when
// it reads the registry and when it has just set one.
//
// One shape for the two is the point: NpmRegistrySection re-reads the registry
// after a successful apply, so the window always ends up looking at the value
// it just wrote. A CLI that reported only "ok" after a set would leave a script
// to guess what the value became.
type RegistryResult struct {
	// Registry is the URL, without a trailing slash: GetNpmRegistry trims the
	// one npm's own default prints.
	Registry string `json:"registry"`
	// Mode classifies Registry as one of the three choices the GUI's radio
	// group offers. It is the component's own derivedMode, tokens included, so
	// a consumer and the window agree on what "official" means. Empty when
	// Registry is, which is what the component derives for a value it has not
	// loaded.
	Mode string `json:"mode"`
}

// The RegistryResult.Mode values, spelled as NpmRegistrySection spells them.
const (
	registryModeOfficial = "official"
	registryModeChina    = "china"
	registryModeCustom   = "custom"
)

// The two aliases `svc pkg registry set` accepts in place of a URL. They exist
// because the GUI offers exactly these two as one-click choices and a third
// (Custom) as a text field: on a terminal, making a user paste the URL of a
// mirror they were just told is one keystroke away is the worse trade. The
// aliases resolve to pkgmgr's exported constants, which are the same two URLs
// the component's radio buttons carry.
const (
	registryAliasOfficial = "official"
	registryAliasChina    = "china"
)

// errRegistryNoURL is `svc pkg registry set` with a blank argument. A usage
// failure rather than the service's own "invalid registry URL: empty": nothing
// was attempted, and the fix is to the command line.
var errRegistryNoURL = errors.New("give a registry URL, or one of the aliases: official, china")

// registryMode classifies a registry URL the way NpmRegistrySection's
// derivedMode does, trailing slashes and all -- the component strips them
// before comparing, so `https://registry.npmjs.org/` is the official registry
// here too and not a custom one.
func registryMode(rawURL string) string {
	switch strings.TrimRight(strings.TrimSpace(rawURL), "/") {
	case "":
		return ""
	case pkgmgr.RegistryOfficial:
		return registryModeOfficial
	case pkgmgr.RegistryChina:
		return registryModeChina
	default:
		return registryModeCustom
	}
}

// registryModeLabel is the GUI's own word for a mode. An unclassified mode has
// no word, and gets the placeholder every other empty cell in this CLI gets.
func (r *runner) registryModeLabel(mode string) string {
	switch mode {
	case registryModeOfficial:
		return r.printer.T("detail.registryOfficial", nil)
	case registryModeChina:
		return r.printer.T("detail.registryChinaMirror", nil)
	case registryModeCustom:
		return r.printer.T("detail.registryCustom", nil)
	default:
		return placeholderNone
	}
}

// resolveRegistryTarget maps what the user typed onto the URL to apply. The two
// aliases are matched case-insensitively because they are words rather than
// URLs, and a URL is returned trimmed, which is the exact form the service
// hands to `npm config set` -- so the sentence reporting the switch quotes the
// value that was written and not the value that was typed.
func resolveRegistryTarget(input string) string {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case registryAliasOfficial:
		return pkgmgr.RegistryOfficial
	case registryAliasChina:
		return pkgmgr.RegistryChina
	default:
		return strings.TrimSpace(input)
	}
}

// newRegistryCommand builds `svc pkg registry`, which shows the current
// registry, and its `set` subcommand, which changes it. The parent is runnable
// rather than a bare group because reading is the common case and
// `svc pkg registry` is the command line that says it; `set` is a subcommand
// because writing is the case that takes an argument.
func (r *runner) newRegistryCommand() *cobra.Command {
	registry := &cobra.Command{
		Use:   "registry",
		Short: "Show the npm registry of the active Node.js",
		Long: "Show the registry the active Node.js installs from, and which of the three\n" +
			"choices the GUI offers it is.\n" +
			"\n" +
			"The value is read live from 'npm config get registry', not from svc's own\n" +
			"config: svc keeps no copy, so a registry set by anything else is reported\n" +
			"as it is. Use 'svc pkg registry set' to change it.",
		Args: r.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			url, err := core.PkgMgr.GetNpmRegistry()
			if err != nil {
				return err
			}
			result := RegistryResult{Registry: url, Mode: registryMode(url)}
			if err := r.printer.JSON(result); err != nil {
				return err
			}
			value := result.Registry
			if value == "" {
				value = placeholderNone
			}
			r.printer.KeyValue([]Pair{
				{Key: r.printer.T("detail.npmRegistry", nil), Value: value},
				{Key: r.printer.T("cli.registryModeLabel", nil), Value: r.registryModeLabel(result.Mode)},
			})
			// The scope line the GUI prints under the radio group. It matters
			// more on a terminal than in the window, because the command that
			// follows it -- `set` -- has no per-version form and a user who
			// expects one is about to be surprised.
			r.printer.Note("%s", r.printer.T("detail.registryScopeHint", nil))
			return nil
		},
	}
	registry.AddCommand(r.newRegistrySetCommand())
	return registry
}

func (r *runner) newRegistrySetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set <url|official|china>",
		Short: "Switch the npm registry of the active Node.js",
		Long: "Switch the registry the active Node.js installs from.\n" +
			"\n" +
			"The argument is a URL, or one of two aliases for the registries the GUI\n" +
			"offers as one-click choices: `official` for registry.npmjs.org and `china`\n" +
			"for registry.npmmirror.com. Only http and https URLs are accepted.\n" +
			"\n" +
			"npm persists the choice in the user-level ~/.npmrc, so it applies to every\n" +
			"Node.js version svc manages and survives switching between them. Nothing is\n" +
			"asked before the write, which is what the GUI's radio buttons do.",
		Args:              r.exactArgs(1),
		ValidArgsFunction: r.registryCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			target := resolveRegistryTarget(args[0])
			if target == "" {
				return r.usageError(cmd, errRegistryNoURL)
			}
			core, err := r.readCore()
			if err != nil {
				return err
			}
			err = r.withOpLock(core, func() error {
				return core.PkgMgr.SetNpmRegistry(target)
			})
			if err != nil {
				return err
			}
			if err := r.printer.JSON(RegistryResult{Registry: target, Mode: registryMode(target)}); err != nil {
				return err
			}
			r.printLine("%s", r.printer.T("detail.registrySwitchSuccess", map[string]string{"url": target}))
			return nil
		},
	}
}

// registryCompletion offers the two aliases and then stops. A registry is a
// URL, and the shell's filename completion is the one thing that must not be
// offered for one.
func (r *runner) registryCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	prefix := strings.ToLower(toComplete)
	var matches []string
	for _, alias := range []string{registryAliasOfficial, registryAliasChina} {
		if strings.HasPrefix(alias, prefix) {
			matches = append(matches, alias)
		}
	}
	return matches, cobra.ShellCompDirectiveNoFileComp
}

// newGlobalCommand builds `svc pkg global` and its four subcommands: the
// globally installed npm packages of the active Node.js.
//
// The group takes no SDK argument, and neither does `list` -- the binding it
// calls has one, but the component passes the literal 'nodejs' and the service
// returns an empty list for every other value, so the parameter has exactly
// one useful setting. A command that accepts an argument with one useful value
// is a command that has to explain itself; the group name already did.
func (r *runner) newGlobalCommand() *cobra.Command {
	global := &cobra.Command{
		Use:   "global",
		Short: "Manage the globally installed npm packages of the active Node.js",
		Long: "List, install, update and remove the packages installed globally into the\n" +
			"active Node.js -- the ones 'npm install -g' puts there.\n" +
			"\n" +
			"These are the GUI's Node.js settings modal, so every command here needs an\n" +
			"active Node.js version and says so when there is none.",
	}
	global.AddCommand(
		r.newGlobalListCommand(),
		r.newGlobalInstallCommand(),
		r.newGlobalUpdateCommand(),
		r.newGlobalUninstallCommand(),
	)
	return global
}

func (r *runner) newGlobalListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the globally installed npm packages",
		Long: "List the packages installed globally into the active Node.js, by name and\n" +
			"version.\n" +
			"\n" +
			"The list is 'npm ls -g --depth=0', so it includes npm itself and corepack:\n" +
			"they are global packages like any other, and one of them is protected --\n" +
			"'svc pkg global uninstall npm' is refused.",
		Args: r.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			packages, err := core.PkgMgr.GetGlobalPackages(string(sdk.NodeJS))
			if err != nil {
				return err
			}
			if err := r.printer.JSON(packages); err != nil {
				return err
			}
			r.printGlobalPackages(packages)
			// An empty list has two causes and only one of them is "nothing is
			// installed": with no active Node.js there was nothing to ask. The
			// service answers both with an empty list and no error, exactly as
			// it does for the GUI, so the distinction is made here.
			if len(packages) == 0 && core.Home.Cfg.GetActiveVersion(string(sdk.NodeJS)) == "" {
				r.printer.Note("%s", r.printer.T("cli.noActiveVersion",
					map[string]string{"sdk": sdk.SdkDisplayName(sdk.NodeJS)}))
			}
			return nil
		},
	}
}

func (r *runner) printGlobalPackages(packages []sdk.GlobalPackage) {
	if len(packages) == 0 {
		r.printLine("%s", r.printer.T("detail.globalPackagesEmpty", nil))
		return
	}
	table := Table{
		Columns: []Column{
			{Header: r.printer.T("detail.globalPackages", nil)},
			{Header: r.printer.T("cli.columnVersion", nil)},
		},
	}
	for _, pkg := range packages {
		version := pkg.Version
		if version == "" {
			version = placeholderNone
		}
		table.Rows = append(table.Rows, []string{pkg.Name, version})
	}
	r.printer.Table(table)
}

func (r *runner) newGlobalInstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "install <package>",
		Short: "Install a package globally into the active Node.js",
		Long: "Install one package globally into the active Node.js: 'npm install -g\n" +
			"<package>'.\n" +
			"\n" +
			"The name is validated against npm's grammar before anything runs, so a\n" +
			"scoped package (@scope/name) is fine and a name carrying a path separator,\n" +
			"a space or a version suffix is refused. Pin a version with\n" +
			"'npm install -g' yourself; this command installs what npm resolves.",
		Args:              r.exactArgs(1),
		ValidArgsFunction: r.noFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			core, err := r.readCore()
			if err != nil {
				return err
			}
			err = r.withOpLock(core, func() error {
				return core.PkgMgr.InstallGlobalPackage(name)
			})
			if err != nil {
				return err
			}
			return r.reportPackage(
				PackageResult{Action: actionInstall, Target: pkgTargetGlobal, Name: name},
				r.printer.T("detail.gpInstallSuccess", map[string]string{"name": name}),
			)
		},
	}
}

func (r *runner) newGlobalUpdateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "update <package>",
		Short: "Update a global package to its latest version",
		Long: "Update one globally installed package to its latest published version:\n" +
			"'npm install -g <package>@latest'.\n" +
			"\n" +
			"A package that is not installed is not an error -- npm installs it, which is\n" +
			"what the GUI's update tag does to a package it was told is there. The\n" +
			"success sentence is the GUI's own for an update, which is why it reads\n" +
			"'updated successfully' rather than 'installed'.",
		Args:              r.exactArgs(1),
		ValidArgsFunction: r.noFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			core, err := r.readCore()
			if err != nil {
				return err
			}
			err = r.withOpLock(core, func() error {
				return core.PkgMgr.UpdateGlobalPackage(name)
			})
			if err != nil {
				return err
			}
			return r.reportPackage(
				PackageResult{Action: actionUpdate, Target: pkgTargetGlobal, Name: name},
				r.printer.T("detail.pmUpdateSuccess", map[string]string{"name": name}),
			)
		},
	}
}

// newGlobalUninstallCommand is the one command in this group that asks first,
// because it is the one the GUI asks first for: GlobalPackagesSection puts the
// removal behind confirmAction while every other operation here is a direct
// call. The question and its explanation are the modal's own two sentences.
func (r *runner) newGlobalUninstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall <package>",
		Short: "Remove a globally installed npm package",
		Long: "Remove one globally installed package from the active Node.js:\n" +
			"'npm uninstall -g <package>'.\n" +
			"\n" +
			"This is the one operation in the group that asks before it acts, because it\n" +
			"is the one the GUI puts behind a modal. npm is refused before the question\n" +
			"is asked: every command in this group shells out to it, so removing it\n" +
			"would break the panel that just did, and a prompt whose only possible\n" +
			"outcome is a failure is a prompt that trains users not to read.\n" +
			"\n" +
			"A package that is not installed counts as removed, which is npm's own\n" +
			"behaviour and the same rule 'svc logs rm' follows: a removal is safe to\n" +
			"repeat.",
		Args:              r.exactArgs(1),
		ValidArgsFunction: r.noFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			core, err := r.readCore()
			if err != nil {
				return err
			}
			if pkgmgr.ProtectedGlobalPackage(name) {
				return apperr.New(apperr.ProtectedPackage, map[string]string{"name": name})
			}
			if err := r.confirmMutation(
				r.printer.T("detail.gpUninstallConfirm", map[string]string{"name": name}),
				r.printer.T("detail.gpUninstallConfirmDesc", map[string]string{"name": name}),
			); err != nil {
				return err
			}
			err = r.withOpLock(core, func() error {
				return core.PkgMgr.UninstallGlobalPackage(name)
			})
			if err != nil {
				return err
			}
			return r.reportPackage(
				PackageResult{Action: actionUninstall, Target: pkgTargetGlobal, Name: name},
				r.printer.T("detail.gpUninstallSuccess", map[string]string{"name": name}),
			)
		},
	}
}
