package cli

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"svc/internal/apperr"
	"svc/internal/sdk"
)

// `svc import` is the third way an SDK copy reaches the SVC store, next to
// `install` (download one) and whatever an earlier import already put there. It
// is a port of two GUI handlers that share one service: PathModal's
// handleImport for a path the user points at, and DetailPanel's
// handleImportPath for the copy already on the system PATH.
//
// The one GUI affordance a terminal cannot reproduce is the picker. G5 of the
// design says the CLI must never open a dialog, and clirt.Runtime enforces that
// by refusing SelectLocalFile and SelectLocalDir outright -- so the path is an
// argument here, and a command that reached for a picker would fail loudly
// instead of hanging. cmd_import_test.go pins the refusal at the source level.
//
// Both sources end in the same three service steps (detect the version, copy
// atomically, activate and configure), so they share everything after the
// confirmation and differ only in which sentence the GUI puts in front of the
// user first.

// actionImport is the MutationResult.Action value `svc import` reports. It
// lives next to the command rather than with the other four actions because the
// file that owns a command owns its vocabulary.
const actionImport = "import"

// The two ways `svc import` can be written wrong. Both are usage failures
// (exit 2) rather than errors: nothing was attempted, so there is nothing to
// report about the machine.
var (
	errImportTwoSources = errors.New("--from-path takes no path argument; give one source or the other")
	errImportNoSource   = errors.New("give the path of an archive or an SDK directory, or pass --from-path")
)

func (r *runner) newImportCommand() *cobra.Command {
	var fromPath bool
	cmd := &cobra.Command{
		Use:   "import <sdk> [<path>]",
		Short: "Copy an SDK that is already on this machine into the SVC store",
		Long: "Copy an SDK that already exists on this machine into the SVC managed\n" +
			"directory, detect its version, make that version the active one and point\n" +
			"the shims at it -- the same steps the GUI's import button runs.\n" +
			"\n" +
			"<path> is either an archive (.zip, .tar.gz, .tgz, .tar.xz, .7z) or an\n" +
			"already-extracted directory. A directory may be the SDK root or its bin\n" +
			"directory; the layout is detected either way. The GUI reaches this through a\n" +
			"file picker, which a terminal does not have, so the path is an argument.\n" +
			"\n" +
			"--from-path imports the copy the SDK's command currently resolves to on the\n" +
			"system PATH instead. SVC's own shims are skipped while looking for it, and a\n" +
			"copy owned by another version manager (nvm, nvm-rust) or living in a\n" +
			"protected OS directory is refused.\n" +
			"\n" +
			"The version is detected by running the SDK's own verify command, so a\n" +
			"directory that holds only a stub binary is rejected rather than imported.\n" +
			"Importing over an already-installed version replaces it atomically: the old\n" +
			"directory survives until the new one has been verified in place.",
		Args:              r.rangeArgs(1, 2),
		ValidArgsFunction: r.sdkThenFile,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 2 {
				path = args[1]
			}
			switch {
			case fromPath && path != "":
				return r.usageError(cmd, errImportTwoSources)
			case !fromPath && path == "":
				return r.usageError(cmd, errImportNoSource)
			}
			return r.runImport(args[0], path, fromPath)
		},
	}
	cmd.Flags().BoolVar(&fromPath, "from-path", false, "import the copy the SDK's command resolves to on the system PATH")
	return cmd
}

// runImport is the body of `svc import`. localPath is empty exactly when
// fromPath is set; newImportCommand has already rejected every other
// combination.
func (r *runner) runImport(sdkInput, localPath string, fromPath bool) error {
	core, err := r.writeCore()
	if err != nil {
		return err
	}
	kind, err := r.resolveSdk(core, sdkInput)
	if err != nil {
		return err
	}
	// The existence check belongs before the question for the same reason
	// requireInstalled does in cmd_mutate.go: asking a user to approve an import
	// of a path that is not there, and only then failing, is a worse answer than
	// not asking. ImportLocalSdk stats the path again a moment later -- this is
	// the check that keeps the prompt honest, not a second implementation of the
	// import.
	if !fromPath {
		if _, err := os.Stat(localPath); err != nil {
			return apperr.New(apperr.PathNotExist, map[string]string{"path": localPath})
		}
	}

	displayName := sdk.SdkDisplayName(kind)
	question, explanation, success := r.importWording(displayName, localPath, fromPath)
	if err := r.confirmMutation(question, explanation); err != nil {
		return err
	}

	err = r.withOpLock(core, func() error {
		if fromPath {
			return core.Importer.ImportPathSdk(string(kind))
		}
		return core.Importer.ImportLocalSdk(string(kind), localPath)
	})
	if err != nil {
		return err
	}

	// Both importers end with SetActiveVersion followed by ConfigureSdk, so the
	// copy that was imported is the one that is active. Reading the version back
	// rather than reconstructing it keeps the reported value the value on disk,
	// which matters because it came out of the SDK's own verify command and not
	// out of the argument list. The GUI shows no reopen hint after an import, so
	// neither does this.
	return r.reportMutation(MutationReport{
		Result: MutationResult{
			Action:  actionImport,
			SdkType: string(kind),
			Version: core.Home.Cfg.GetActiveVersion(string(kind)),
			Active:  true,
		},
		Summary: success,
	})
}

// importWording picks the three catalog sentences one import run uses.
//
// The GUI has two import buttons and therefore two sets of words: an explicit
// path is PathModal's handleImport, while --from-path is DetailPanel's
// handleImportPath, which borrows the sidebar's sentence exactly the way that
// handler does. The choice is a function of the source and of nothing else, so
// it is a function -- which is also what makes it assertable without a machine
// that happens to have an SDK on its PATH.
func (r *runner) importWording(displayName, localPath string, fromPath bool) (question, explanation, success string) {
	if fromPath {
		return r.printer.T("sidebar.importConfirm", map[string]string{"sdk": displayName}),
			r.printer.T("sidebar.importConfirmDesc", nil),
			r.printer.T("sidebar.importSuccess", map[string]string{"sdk": displayName})
	}
	return r.printer.T("path.importConfirm", map[string]string{"sdk": displayName}),
		r.printer.T("path.importConfirmDesc", map[string]string{"path": localPath}),
		r.printer.T("path.importSuccess", nil)
}
