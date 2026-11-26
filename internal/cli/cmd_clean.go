package cli

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"svc/internal/helpers"
	"svc/internal/logmgr"
	"svc/internal/sdk"
)

// The destructive half of `svc storage` and `svc logs`: three things svc
// accumulates and can safely throw away, plus the selective form of the log
// cleanup.
//
// Each target answers a different question, which is why they are subcommands
// of one parent rather than flags of one command. `tmp` reclaims space from
// downloads and extractions the user never chose; `logs` reclaims space from
// svc's own diagnostics; `inactive <sdk>` reclaims space from versions the user
// did choose and no longer wants. A single `svc clean --all` would flatten that
// distinction into one confirmation for three unrelated deletions.
//
// Two of the four are ports of GUI handlers that exist (SettingsPage's
// handleCleanTmpCache, handleDeleteLog and handleCleanSelectedLogs). The other
// two -- CleanLogs and CleanInactiveVersions -- are bindings the GUI exposes but
// no component calls, so the wording for those two questions is the CLI's own
// and lives in the `cli` catalog group rather than being borrowed.

// CleanResult is what a clean command reports through --json.
//
// Like MutationResult the envelope is the CLI's own: there is no service struct
// that means "one cache was emptied". The counts are measured rather than
// assumed, so a consumer can act on them without re-walking the directory.
type CleanResult struct {
	// Target names what was deleted. One of the cleanTarget constants.
	Target string `json:"target"`
	// SdkType is the resolved identifier, and is set only by `clean inactive` --
	// the one target scoped to a single SDK.
	SdkType string `json:"sdkType,omitempty"`
	// Removed counts what disappeared: cache entries, log files, or version
	// directories. Zero means the target was already clean.
	Removed int `json:"removed"`
	// FreedBytes is the space the target occupied before it was deleted. Only
	// `clean tmp` reports it, because that is the only target whose purpose is
	// reclaiming space from files nobody chose to keep; a count of log files or
	// of versions is the number a user acts on instead.
	FreedBytes int64 `json:"freedBytes,omitempty"`
}

// The CleanResult.Target values.
const (
	cleanTargetTmp      = "tmp"
	cleanTargetLogs     = "logs"
	cleanTargetInactive = "inactive"
	// cleanTargetLogFiles is `logs rm`, the selective form. It is a distinct
	// target from cleanTargetLogs so a consumer can tell "every log file went"
	// from "the three I named went".
	cleanTargetLogFiles = "log-files"
)

// CleanReport is everything one clean command outputs, split by channel the way
// MutationReport is.
type CleanReport struct {
	// Result is the --json document.
	Result CleanResult
	// Summary is the outcome sentence, the CLI's form of the GUI's success
	// toast.
	Summary string
	// Detail is a second result line. It is a fact about the run rather than
	// advice, so it goes to stdout next to Summary and not through Note.
	Detail string
}

// reportClean writes one CleanReport. Exactly one of the two shapes reaches
// stdout in any given mode, for the reason reportMutation gives.
func (r *runner) reportClean(report CleanReport) error {
	if err := r.printer.JSON(report.Result); err != nil {
		return err
	}
	if report.Summary != "" {
		r.printLine("%s", report.Summary)
	}
	if report.Detail != "" {
		r.printLine("%s", report.Detail)
	}
	return nil
}

// countDirEntries reports how many top-level entries a directory holds. A
// directory that cannot be read yields 0 rather than an error: this is a report
// about what is going to be deleted, and the service that deletes it produces
// its own error if the directory turns out to be unusable. A count that failed
// the command would turn "nothing to clean" into "broken".
func countDirEntries(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	return len(entries)
}

// countInactiveVersions reports how many version directories under an SDK
// directory are not the active one. It is the number `clean inactive` puts in
// its question, and it mirrors storage.Manager.CleanInactiveVersions entry for
// entry -- directories only, the active name skipped -- so what the user
// approves is what the service deletes. An empty active version makes every
// directory inactive, which is also what the service does.
func countInactiveVersions(sdkDir, active string) (int, error) {
	entries, err := os.ReadDir(sdkDir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == active {
			continue
		}
		count++
	}
	return count, nil
}

// validateLogNames refuses every argument that is not a bare file name, before
// any question is asked and before anything is deleted.
//
// logmgr validates each name again as it deletes it; this is the check that
// keeps the prompt honest rather than a second implementation of the rule. The
// reason it matters more here than it does for `logs show` is the set form: a
// `svc logs rm ../x svc-2026-01-01.log` that validated lazily would delete the
// real file and then report the typo as a partial failure, turning one
// mistyped argument into a destructive run. `logs show` reads one file and
// changes nothing, so it can afford to let logmgr be the only gate.
func validateLogNames(names []string) error {
	for _, name := range names {
		if err := helpers.ValidatePathSegment(name); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) newCleanCommand() *cobra.Command {
	clean := &cobra.Command{
		Use:   "clean",
		Short: "Delete what svc no longer needs",
		Long: "Delete leftover downloads, svc's own log files, or every version of an SDK\n" +
			"except the active one.\n" +
			"\n" +
			"'svc clean' on its own lists these and deletes nothing; a target has to be\n" +
			"named. 'svc storage' reports what each target would reclaim, also without\n" +
			"deleting it.",
	}
	clean.AddCommand(r.newCleanTmpCommand(), r.newCleanLogsCommand(), r.newCleanInactiveCommand())
	return clean
}

func (r *runner) newCleanTmpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "tmp",
		Short: "Empty the download and extraction cache",
		Long: "Delete everything under the SVC temp directory: archives a download left\n" +
			"behind and trees an interrupted extraction did not clean up.\n" +
			"\n" +
			"Installed versions live outside this directory and are never touched. The GUI\n" +
			"asks no question before doing this and neither does svc, and a write-level\n" +
			"startup empties the same directory as a matter of course.\n" +
			"\n" +
			"The command bootstraps read-only on purpose. A write-level startup would\n" +
			"empty the cache before this command could measure it, so every invocation\n" +
			"would report 'Freed 0 B'. Nothing here edits the shell rc or the registry\n" +
			"PATH, so read-only is also the honest level -- the operation lock, which is\n" +
			"what actually keeps a concurrent install from losing its download, is taken\n" +
			"either way.",
		Args: r.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			freed := core.Storage.GetTmpCacheSize()
			entries := countDirEntries(core.Home.Cfg.TmpDir())
			if err := r.withOpLock(core, core.Storage.CleanTmpCache); err != nil {
				return err
			}
			return r.reportClean(CleanReport{
				Result: CleanResult{
					Target:     cleanTargetTmp,
					Removed:    entries,
					FreedBytes: freed,
				},
				Summary: r.printer.T("settings.cleanSuccess", nil),
				Detail:  r.printer.T("cli.freedBytes", map[string]string{"size": FormatBytes(freed)}),
			})
		},
	}
}

func (r *runner) newCleanLogsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "Delete every svc log file",
		Long: "Delete all of svc's own log files and reopen today's as an empty file, so\n" +
			"the next run starts from a clean slate.\n" +
			"\n" +
			"Use 'svc logs rm <file>...' to delete some of them instead.\n" +
			"\n" +
			"Neither command takes the operation lock and neither bootstraps the services:\n" +
			"they touch the log directory and nothing else, every svc process writes to it\n" +
			"independently, and a file that is deleted under a running process is simply\n" +
			"recreated.",
		Args: r.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			files, err := logmgr.GetLogFiles()
			if err != nil {
				return err
			}
			if len(files) == 0 {
				// Nothing to delete is a result rather than a failure, and a
				// no-op is not worth a question -- the rule `switch` already
				// applies to the version that is active.
				return r.reportClean(CleanReport{
					Result:  CleanResult{Target: cleanTargetLogs},
					Summary: r.printer.T("logs.cleanSuccess", nil),
				})
			}
			if err := r.confirmMutation(
				r.printer.T("cli.cleanLogsConfirm", map[string]string{"count": strconv.Itoa(len(files))}),
				r.printer.T("cli.cleanLogsDesc", nil),
			); err != nil {
				return err
			}
			if err := logmgr.CleanLogs(); err != nil {
				return err
			}
			return r.reportClean(CleanReport{
				Result:  CleanResult{Target: cleanTargetLogs, Removed: len(files)},
				Summary: r.printer.T("logs.cleanSuccess", nil),
			})
		},
	}
}

func (r *runner) newCleanInactiveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "inactive <sdk>",
		Short: "Delete every version of an SDK except the active one",
		Long: "Delete each installed version of one SDK except the active one, and report how\n" +
			"many went.\n" +
			"\n" +
			"Removing the last version of an SDK also removes its shims, so a 'go' that\n" +
			"resolved through svc stops resolving at all -- which is why the active version\n" +
			"is always kept and why the count is quoted in the question.\n" +
			"\n" +
			"'svc storage' lists how many versions each SDK holds, and 'svc versions <sdk>'\n" +
			"marks which of them are installed.",
		Args:              r.exactArgs(1),
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
			sdkDir := core.Home.Cfg.SdkDir(string(kind))
			active := core.Home.Cfg.GetActiveVersion(string(kind))
			pending, err := countInactiveVersions(sdkDir, active)
			if err != nil {
				return err
			}
			if pending == 0 {
				return r.reportClean(CleanReport{
					Result:  CleanResult{Target: cleanTargetInactive, SdkType: string(kind)},
					Summary: r.printer.T("cli.removedVersions", map[string]string{"count": "0"}),
				})
			}
			if err := r.confirmMutation(
				r.printer.T("cli.cleanInactiveConfirm", map[string]string{
					"sdk":   sdk.SdkDisplayName(kind),
					"count": strconv.Itoa(pending),
				}),
				r.printer.T("cli.cleanInactiveDesc", nil),
			); err != nil {
				return err
			}

			removed := 0
			err = r.withOpLock(core, func() error {
				// Counted again inside the lock. The number in the question was
				// a promise made before it, and another svc process may have
				// installed or uninstalled a version in between; this one is
				// the fact the report owes.
				count, err := countInactiveVersions(sdkDir, active)
				if err != nil {
					return err
				}
				if err := core.Storage.CleanInactiveVersions(string(kind)); err != nil {
					return err
				}
				removed = count
				return nil
			})
			if err != nil {
				return err
			}
			return r.reportClean(CleanReport{
				Result: CleanResult{
					Target:  cleanTargetInactive,
					SdkType: string(kind),
					Removed: removed,
				},
				Summary: r.printer.T("cli.removedVersions", map[string]string{"count": strconv.Itoa(removed)}),
			})
		},
	}
}

// newLogsRemoveCommand builds the destructive half of `svc logs`. It is
// attached to the logs parent in cmd_system.go rather than to `clean`, because
// the name a user needs is the one `logs list` just printed.
func (r *runner) newLogsRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <file>...",
		Short: "Delete log files",
		Long: "Delete one or more of svc's own log files, by name.\n" +
			"\n" +
			"The names are bare file names from 'svc logs list', not paths: each is\n" +
			"validated as a single path segment, so neither ../ nor an absolute path is\n" +
			"accepted. Deleting today's active log is allowed and reopens it empty. A\n" +
			"name that is already gone counts as deleted, so the command is safe to\n" +
			"repeat.\n" +
			"\n" +
			"Several names are asked about once, as a set. If some of them fail the\n" +
			"command reports the names that failed and exits non-zero, because it was\n" +
			"asked to do more than it managed -- the deletions that did happen are not\n" +
			"undone.",
		Args: r.atLeastArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateLogNames(args); err != nil {
				return err
			}
			if len(args) == 1 {
				return r.runRemoveLog(args[0])
			}
			return r.runRemoveLogs(args)
		},
	}
}

// runRemoveLog deletes one named log file. This is SettingsPage's
// handleDeleteLog, including its wording.
func (r *runner) runRemoveLog(name string) error {
	if err := r.confirmMutation(
		r.printer.T("logs.deleteConfirm", nil),
		r.printer.T("logs.deleteConfirmDesc", map[string]string{"filename": name}),
	); err != nil {
		return err
	}
	if err := logmgr.DeleteLogFile(name); err != nil {
		// The GUI wraps the reason in its own sentence and shows that; the
		// logmgr errors are plain text rather than apperr markers, so nothing is
		// lost by rendering them into the parameter.
		return errors.New(r.printer.T("logs.deleteFail", map[string]string{"error": err.Error()}))
	}
	return r.reportClean(CleanReport{
		Result:  CleanResult{Target: cleanTargetLogFiles, Removed: 1},
		Summary: r.printer.T("logs.deleteSuccess", nil),
	})
}

// runRemoveLogs deletes a set of named log files. This is SettingsPage's
// handleCleanSelectedLogs, including the way it collects failures into one
// message instead of stopping at the first.
func (r *runner) runRemoveLogs(names []string) error {
	if err := r.confirmMutation(
		r.printer.T("logs.cleanSelectedConfirm", map[string]string{"count": strconv.Itoa(len(names))}),
		r.printer.T("logs.cleanSelectedDesc", nil),
	); err != nil {
		return err
	}
	removed := 0
	var failed []string
	for _, name := range names {
		if err := logmgr.DeleteLogFile(name); err != nil {
			failed = append(failed, name+": "+err.Error())
			continue
		}
		removed++
	}
	if len(failed) > 0 {
		return errors.New(r.printer.T("logs.cleanFail", map[string]string{"error": strings.Join(failed, "; ")}))
	}
	return r.reportClean(CleanReport{
		Result:  CleanResult{Target: cleanTargetLogFiles, Removed: removed},
		Summary: r.printer.T("logs.cleanSuccess", nil),
	})
}
