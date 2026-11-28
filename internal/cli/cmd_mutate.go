package cli

import (
	"slices"

	"github.com/spf13/cobra"

	"svc/internal/appcore"
	"svc/internal/apperr"
	"svc/internal/logger"
	"svc/internal/sdk"
	"svc/internal/storage"
)

// The mutating half of the SDK lifecycle: install, reinstall, switch and
// uninstall.
//
// Each one is a port of a handler in frontend/src/components/Panel/DetailPanel.tsx
// -- handleInstall, handleReinstall, handleSwitchVersion, handleUninstallVersion
// -- and the port is deliberate down to the order of the steps: check, warn,
// confirm, call the same service the GUI calls, report the same sentence. The
// two shells must not merely reach the same on-disk state (the parity test in
// cmd_mutate_test.go pins that) but must also tell the user the same thing
// about it, because a warning the GUI shows and the CLI swallows is a warning
// half the users never see.
//
// Three differences from the GUI are intentional and documented where they are
// made: a confirmation the user declines is an exit code rather than a closed
// modal; the operation lock is taken after the confirmation rather than not at
// all; and the "you deleted the version you were on" case prints both the
// success and the warning instead of only the warning.
//
// The lock is only half of the concurrency story until the GUI takes it too.
// See runner.withOpLock for why that half is a separate change.

// MutationResult is what a mutating command reports through --json.
//
// The envelope is the CLI's own -- there is no service struct that means "one
// install happened" -- but every field is a fact about the run rather than a
// restatement of the arguments: Active says what is active afterwards, and
// Warning carries the one thing a caller acting on the exit code alone would
// otherwise miss.
type MutationResult struct {
	// Action is the command that ran: install, reinstall, switch or uninstall.
	Action string `json:"action"`
	// SdkType is the resolved identifier, never the spelling the user typed.
	SdkType string `json:"sdkType"`
	// Version is the version the command was given.
	Version string `json:"version"`
	// Active reports whether Version is the active one when the command
	// returns. InstallSdk and SwitchVersion both activate what they touch; an
	// uninstall never leaves the version it removed active.
	Active bool `json:"active"`
	// Warning is a stable machine token for a success that is not a plain
	// success. Empty when there is nothing to add. The tokens are part of the
	// --json contract, so they are constants below rather than prose.
	Warning string `json:"warning,omitempty"`
}

// The Action values MutationResult reports.
const (
	actionInstall   = "install"
	actionReinstall = "reinstall"
	actionSwitch    = "switch"
	actionUninstall = "uninstall"
)

// The Warning tokens MutationResult reports.
const (
	// warningAlreadyActive means `switch` was asked for the version that is
	// already active, so nothing was written. The GUI is silent about this --
	// its handler returns before opening a modal -- and a silent success is the
	// one thing a terminal cannot afford to be.
	warningAlreadyActive = "already-active"
	// warningActiveVersionDeleted means `uninstall` removed the active version,
	// so the SDK now has no active version and its shims resolve to nothing
	// until the user picks another. It is the CLI's form of the
	// storage.ActiveVersionDeletedPrefix sentinel the GUI matches on.
	warningActiveVersionDeleted = "active-version-deleted"
)

// MutationReport is everything one mutating command outputs, split by channel
// the way the GUI splits its feedback.
type MutationReport struct {
	// Result is the --json document.
	Result MutationResult
	// Summary is the outcome sentence, the CLI's form of the GUI's success
	// toast. Empty when the command changed nothing.
	Summary string
	// Warning is a second outcome sentence. It goes to stdout next to Summary
	// rather than to stderr, because a caller that redirected the result must
	// still learn that the run left the SDK without an active version.
	Warning string
	// Hint is advice about the environment rather than about the operation, and
	// goes through Printer.Note: an interactive terminal sees it, a pipe and a
	// --json consumer do not.
	Hint string
}

// reportMutation writes one MutationReport. Exactly one of the two shapes
// reaches stdout in any given mode: the document in JSON mode, the sentences in
// the text modes.
func (r *runner) reportMutation(report MutationReport) error {
	if err := r.printer.JSON(report.Result); err != nil {
		return err
	}
	if report.Summary != "" {
		r.printLine("%s", report.Summary)
	}
	if report.Warning != "" {
		r.printLine("%s", report.Warning)
	}
	if report.Hint != "" {
		r.printer.Note("%s", report.Hint)
	}
	return nil
}

// confirmMutation asks the one question every mutating command owes the user
// before it changes the system. question is the GUI modal's title and
// explanation its body, both already rendered from the catalog.
//
// --yes answers it in advance, and without a terminal on stdin the prompt refuses
// to guess (ErrConfirmationRequired) -- which is what makes an uninstall inside a
// script impossible by accident. In both cases the explanation is not printed:
// it exists to inform a decision, and printing it for a decision that was already
// made, or that cannot be made at all, is text with nothing attached to it.
//
// A "no" comes back as ErrConfirmationDeclined rather than nil. The command then
// did nothing, and a caller that sees exit 0 cannot tell that apart from
// success -- so declining is reported as a cancellation, exit 5.
func (r *runner) confirmMutation(question, explanation string) error {
	if !r.opts.Yes && r.opts.StdinTTY && explanation != "" {
		r.printer.Note("%s", explanation)
	}
	accepted, err := r.prompt.Confirm(ConfirmRequest{
		Question: question,
		Yes:      r.opts.Yes,
		StdinTTY: r.opts.StdinTTY,
	})
	if err != nil {
		return err
	}
	if !accepted {
		return ErrConfirmationDeclined
	}
	return nil
}

// noteSystemConflicts reports the system-level entries competing with this
// SDK's shims, which is what the GUI's conflict modal does when the detail panel
// opens.
//
// It is advice and never a result, so it goes through Note: an install into a
// machine that also has nvm-managed Node still installs, and the conflicts belong
// to `svc conflicts <sdk>` for anyone who wants them as data. A failed check is
// logged and ignored, mirroring the GUI's `.catch(() => {})` -- a diagnostic that
// cannot be computed must not fail the operation it was annotating.
func (r *runner) noteSystemConflicts(core *appcore.Core, sdkType string) {
	conflicts, err := core.Installer.CheckSystemConflicts(sdkType)
	if err != nil {
		logger.Warn("System conflict check for %s failed: %v", sdkType, err)
		return
	}
	if len(conflicts) == 0 {
		return
	}
	r.printer.Note("%s", r.printer.T("detail.systemConflictTitle", nil))
	r.printer.Note("%s", r.printer.T("detail.systemConflictMsg", nil))
	for _, conflict := range conflicts {
		r.printer.Note("  %s", conflict)
	}
}

// requireInstalled refuses a command that only makes sense for a version already
// on disk, with the same "not there" marker the services use so the exit code is
// 3 and the message is the one the GUI shows.
//
// Checking here rather than letting the service refuse is what keeps the check
// BEFORE the confirmation prompt: asking a user to approve an uninstall of
// something that does not exist, and only then failing, is a worse answer than
// not asking.
func requireInstalled(status *sdk.SdkStatus, version string) error {
	if status != nil && slices.Contains(status.InstalledVersions, version) {
		return nil
	}
	return apperr.New(apperr.VersionDirMissing, map[string]string{"version": version})
}

func (r *runner) newInstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "install <sdk> <version>",
		Short: "Download, install and activate one SDK version",
		Long: "Download one version of an SDK into the SVC directory, verify it, and make\n" +
			"it the active version -- the same three steps the GUI's install button runs,\n" +
			"in the same order.\n" +
			"\n" +
			"Activation is not optional: installing a version also points the shims at it,\n" +
			"which is why there is nothing to undo after a successful install. A shell that\n" +
			"is already running keeps the environment it started with, so reopen it if the\n" +
			"shims were never on PATH; 'svc init' sets them up.\n" +
			"\n" +
			"Available versions come from 'svc versions <sdk>'. The download honours the\n" +
			"mirrors in ~/.svc/mirrors.json and the proxy saved in settings.json.",
		Args:              r.exactArgs(2),
		ValidArgsFunction: r.sdkThenNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runInstall(actionInstall, args[0], args[1])
		},
	}
}

func (r *runner) newReinstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reinstall <sdk> <version>",
		Short: "Download an installed SDK version again, replacing its files",
		Long: "Re-download a version that is already installed and overwrite its directory.\n" +
			"This is the GUI's reinstall button: the same call the install button makes,\n" +
			"with different confirmation wording.\n" +
			"\n" +
			"The version must already be installed; use 'svc install' for one that is not.",
		Args:              r.exactArgs(2),
		ValidArgsFunction: r.sdkThenNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runInstall(actionReinstall, args[0], args[1])
		},
	}
}

// runInstall is the body of both install and reinstall. The GUI routes both to
// one doInstall (DetailPanel.tsx) and so does this; only the confirmation text
// and the extra warnings differ.
func (r *runner) runInstall(action, sdkInput, version string) error {
	core, err := r.writeCore()
	if err != nil {
		return err
	}
	kind, err := r.resolveSdk(core, sdkInput)
	if err != nil {
		return err
	}
	// Refused before the status is even read. An SDK an external version manager
	// owns is not something svc may install, and asking the user to confirm an
	// operation that is already declined wastes their answer -- the service
	// checks the same rule again, so this is a courtesy rather than the
	// enforcement.
	if err := core.Installer.ExternalManagerRefusal(string(kind)); err != nil {
		return err
	}
	status, err := core.Installer.GetSdkStatus(string(kind))
	if err != nil {
		return err
	}
	if action == actionReinstall {
		if err := requireInstalled(status, version); err != nil {
			return err
		}
	}

	// The conflict modal the GUI panel raises on open. Not shown for a
	// reinstall, because the GUI's reinstall modal has no such paragraph. The
	// external-manager paragraph that used to stand beside it is gone: the
	// install it warned about is now refused outright, above.
	r.noteSystemConflicts(core, string(kind))

	question := r.printer.T("detail.confirmInstallSdk", map[string]string{
		"sdk":     status.DisplayName,
		"version": version,
	})
	explanation := r.printer.T("detail.confirmInstallDesc", map[string]string{"version": version})
	if action == actionReinstall {
		question = r.printer.T("detail.reinstallConfirm", map[string]string{"version": version})
		explanation = r.printer.T("detail.reinstallConfirmDesc", nil)
	}
	if err := r.confirmMutation(question, explanation); err != nil {
		return err
	}

	// The lock is taken only now. Holding it across a confirmation prompt would
	// let a user who walked away from the question block every other svc process
	// indefinitely.
	err = r.withOpLock(core, func() error {
		return core.Installer.InstallSdk(string(kind), version)
	})
	if err != nil {
		return err
	}

	// Active is true unconditionally: InstallSdk ends with SetActiveVersion and
	// ConfigureSdk, so a version that installed and did not become active is a
	// failure and came back through the error above. There is deliberately no
	// --no-switch here -- the GUI has no such behaviour to match.
	return r.reportMutation(MutationReport{
		Result: MutationResult{
			Action:  action,
			SdkType: string(kind),
			Version: version,
			Active:  true,
		},
		Summary: r.printer.T("detail.installSuccess", map[string]string{
			"sdk":     status.DisplayName,
			"version": version,
		}),
	})
}

func (r *runner) newSwitchCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "switch <sdk> <version>",
		Aliases: []string{"use"},
		Short:   "Make an installed SDK version the active one",
		Long: "Point an SDK's shims and environment variables at a version that is already\n" +
			"installed. Nothing is downloaded.\n" +
			"\n" +
			"Reopen the terminal afterwards: a shell that is already running keeps the\n" +
			"environment it started with.\n" +
			"\n" +
			"Switching to the version that is already active changes nothing, says so, and\n" +
			"still exits 0.",
		Args:              r.exactArgs(2),
		ValidArgsFunction: r.sdkThenNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.writeCore()
			if err != nil {
				return err
			}
			kind, err := r.resolveSdk(core, args[0])
			if err != nil {
				return err
			}
			version := args[1]
			// Switching re-points the `node` shim at svc's copy, which is the
			// same fight install is refused for. Uninstall is deliberately not
			// refused: removing svc's own copy is how a user gets out.
			if err := core.Installer.ExternalManagerRefusal(string(kind)); err != nil {
				return err
			}
			status, err := core.Installer.GetSdkStatus(string(kind))
			if err != nil {
				return err
			}
			// Installed-ness is checked before the no-op shortcut, so a config
			// that points at a deleted directory is reported as the broken state
			// it is instead of as "already active".
			if err := requireInstalled(status, version); err != nil {
				return err
			}
			result := MutationResult{
				Action:  actionSwitch,
				SdkType: string(kind),
				Version: version,
				Active:  true,
			}
			if version == status.CurrentVersion {
				result.Warning = warningAlreadyActive
				return r.reportMutation(MutationReport{
					Result:  result,
					Warning: r.printer.T("cli.alreadyActive", map[string]string{"version": version}),
				})
			}

			if err := r.confirmMutation(
				r.printer.T("detail.switchConfirm", map[string]string{
					"sdk":     status.DisplayName,
					"version": version,
				}),
				r.printer.T("detail.switchConfirmDesc", nil),
			); err != nil {
				return err
			}
			if err := r.withOpLock(core, func() error {
				return core.Installer.SwitchVersion(string(kind), version)
			}); err != nil {
				return err
			}

			// The reopen hint is the GUI's second toast. It is a Hint rather
			// than part of the summary because it is about the user's shell and
			// not about the switch, and because output.go reserves Note for
			// exactly this sentence.
			return r.reportMutation(MutationReport{
				Result:  result,
				Summary: r.printer.T("detail.switchSuccess", map[string]string{"version": version}),
				Hint:    r.printer.T("detail.reopenTerminalHint", nil),
			})
		},
	}
}

func (r *runner) newUninstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "uninstall <sdk> <version>",
		Aliases: []string{"rm"},
		Short:   "Delete one installed SDK version from disk",
		Long: "Delete one version's directory. Other versions of the same SDK are left alone,\n" +
			"and so are the shims -- they route by SDK type and resolve the active version\n" +
			"at run time.\n" +
			"\n" +
			"Uninstalling the active version is allowed: the activation is cleared and the\n" +
			"command exits 0 with a warning telling you to pick another version. That is\n" +
			"the GUI's behaviour too. Removing the last version of an SDK also removes its\n" +
			"shims.",
		Args:              r.exactArgs(2),
		ValidArgsFunction: r.sdkThenNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.writeCore()
			if err != nil {
				return err
			}
			kind, err := r.resolveSdk(core, args[0])
			if err != nil {
				return err
			}
			version := args[1]
			status, err := core.Installer.GetSdkStatus(string(kind))
			if err != nil {
				return err
			}
			if err := requireInstalled(status, version); err != nil {
				return err
			}

			// The GUI asks a harder question when the version is the active one,
			// because that is the case where the user may not have meant it.
			isActive := version == status.CurrentVersion
			question := r.printer.T("detail.uninstallConfirm", map[string]string{
				"sdk":     status.DisplayName,
				"version": version,
			})
			explanation := r.printer.T("detail.uninstallConfirmDesc", nil)
			if isActive {
				question = r.printer.T("detail.uninstallActiveConfirm", map[string]string{
					"sdk":     status.DisplayName,
					"version": version,
				})
				explanation = r.printer.T("detail.uninstallActiveConfirmDesc", nil)
			}
			if err := r.confirmMutation(question, explanation); err != nil {
				return err
			}

			err = r.withOpLock(core, func() error {
				return core.Storage.UninstallVersion(string(kind), version)
			})
			report := MutationReport{
				Result: MutationResult{
					Action:  actionUninstall,
					SdkType: string(kind),
					Version: version,
				},
				Summary: r.printer.T("detail.uninstallSuccess", map[string]string{"version": version}),
			}
			// The service reports "that was the active version" as an error,
			// because that is what the Wails boundary can carry. It is not one:
			// the deletion was exactly what was asked for. The GUI turns it into
			// a warning toast and a refresh, and this turns it into a warning
			// line and exit 0.
			if _, deleted := storage.ActiveVersionDeleted(err); deleted {
				report.Result.Warning = warningActiveVersionDeleted
				report.Warning = r.printer.T("detail.activeVersionDeleted", map[string]string{
					"sdk": status.DisplayName,
				})
			} else if err != nil {
				return err
			}
			return r.reportMutation(report)
		},
	}
}
