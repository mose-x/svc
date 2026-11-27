package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"svc/internal/update"
)

// `svc self` is the group about the svc binary rather than about an SDK: what
// version is running, whether a newer one is published, and swapping one binary
// for the other in both directions.
//
// The parent is named `self` and not `update` so that `svc update` stays free.
// An SDK-less verb at the top level would read as "update the SDKs", which is
// `svc reinstall` and `svc pkg update`; putting the binary's own lifecycle under
// one noun is what keeps the two apart in `svc --help`.

// SelfCheckResult is what `svc self check` reports through --json.
//
// Update is update.UpdateInfo verbatim -- the same object the CheckUpdate
// binding hands SettingsPage -- so a consumer that understands the window's
// update modal understands this document without a second schema. Current sits
// beside it because hasUpdate is only meaningful next to the version it was
// computed from, and the GUI reads that from a different binding (GetAppInfo)
// that this command has already resolved anyway.
type SelfCheckResult struct {
	// Current is the running build's version, without the "v" prefix, exactly
	// as about.json records it and as update.UpdateInfo compares against it.
	Current string `json:"current"`
	// Update is the service's answer verbatim.
	Update update.UpdateInfo `json:"update"`
}

func (r *runner) newSelfCommand() *cobra.Command {
	self := &cobra.Command{
		Use:   "self",
		Short: "Inspect the svc binary itself",
	}
	self.AddCommand(r.newSelfCheckCommand())
	self.AddCommand(r.newSelfUpdateCommand())
	self.AddCommand(r.newSelfRollbackCommand())
	return self
}

func (r *runner) newSelfCheckCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Check whether a newer svc release is published",
		Long: "Ask the release feed whether a newer svc exists, and report what it found.\n" +
			"\n" +
			"The feed is the GitHub releases list recorded in about.json, filtered to pure\n" +
			"X.Y.Z tags: an -rc1 or -beta release is never offered, whether or not GitHub\n" +
			"calls it a pre-release. When a newer stable exists, the asset matching this\n" +
			"platform is named and its download URL printed, along with the release notes.\n" +
			"\n" +
			"Checking only reads. It downloads nothing and replaces nothing, so it is safe\n" +
			"to run from a cron job or a CI step that just wants to know.\n" +
			"\n" +
			"The saved proxy and GitHub mirror are honoured, and a stored GitHub token is\n" +
			"sent -- which raises the release-API rate limit from 60 to 5000 requests an\n" +
			"hour. Neither is printed.\n" +
			"\n" +
			"Exit code is 0 when the check itself succeeded, whatever it found; a feed that\n" +
			"could not be reached exits 6, and a release with no asset for this platform\n" +
			"exits 3.",
		Args:              r.noArgs(),
		ValidArgsFunction: r.noFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSelfCheck()
		},
	}
}

func (r *runner) runSelfCheck() error {
	// LevelRead, not LevelMeta: the Updater is assembled by Bootstrap, and
	// re-wiring update.NewUpdater here would create a second place that has to
	// know its five arguments. The read-level startup it costs is the same one
	// `svc versions` already pays for reaching the network.
	core, err := r.readCore()
	if err != nil {
		return err
	}
	info, err := core.Updater.CheckUpdate()
	if err != nil {
		return err
	}
	result := SelfCheckResult{Current: core.Home.AppInfo.Version, Update: info}
	if err := r.printer.JSON(result); err != nil {
		return err
	}
	if !info.HasUpdate {
		// The GUI toasts exactly this sentence and shows nothing else; the
		// latest version it compared against is in --json for anyone who cares.
		r.printLine("%s", r.printer.T("about.upToDate", nil))
		return nil
	}
	r.printSelfUpdate(result)
	return nil
}

// printSelfUpdate renders the found release in the shape of the window's update
// modal: the title sentence, the "current → latest" line under it, the asset,
// and the release notes as a bare block -- the modal shows them unlabelled too,
// which is why the changelog gets no key of its own.
func (r *runner) printSelfUpdate(result SelfCheckResult) {
	info := result.Update
	r.printLine("%s", r.printer.T("about.newVersion",
		map[string]string{"version": info.LatestVersion}))
	r.printLine("%s", versionArrow(r.printer, result.Current, info.LatestVersion))

	// Both rows are conditional because a release whose asset did not match
	// this platform never gets here, but one published without the checksum
	// manifest or with a source-only archive can leave either empty -- and a
	// label with nothing after it is worse than no label.
	var pairs []Pair
	if name := strings.TrimSpace(info.Filename); name != "" {
		pairs = append(pairs, Pair{Key: r.printer.T("settings.filename", nil), Value: name})
	}
	if url := strings.TrimSpace(info.DownloadURL); url != "" {
		pairs = append(pairs, Pair{Key: r.printer.T("cli.downloadURL", nil), Value: url})
	}
	r.printer.KeyValue(pairs)

	if changelog := strings.TrimSpace(info.Changelog); changelog != "" {
		r.printer.Blank()
		r.printLine("%s", changelog)
	}
}

// versionArrow renders the update modal's body line: the current-version
// sentence, an arrow, and the version on offer. `self check` prints it as a
// result and `self update` prints it as the explanation under its
// confirmation, and the two have to be the same sentence -- it is the one place
// a terminal user is told what is about to replace what.
func versionArrow(printer *Printer, current, latest string) string {
	return fmt.Sprintf("%s → v%s",
		printer.T("about.currentVersion", map[string]string{"version": current}),
		latest)
}

// SwapAction names the platform binary swap a `svc self` command asked for. The
// two tokens double as SelfApplyResult.Action, so what a caller reads out of
// --json is the same vocabulary the command layer dispatches on.
type SwapAction string

const (
	// SwapUpdate replaces the running binary with a downloaded release.
	SwapUpdate SwapAction = "update"
	// SwapRollback restores the .bak the last update left behind.
	SwapRollback SwapAction = "rollback"
)

// SwapFunc performs one platform binary swap. ExecuteInput.Swap replaces it.
type SwapFunc func(updater *update.Updater, action SwapAction, opts update.ApplyOptions) error

// performSwap is the production SwapFunc: the Updater's own two methods,
// selected by the action. An unknown action is an error rather than a default
// branch that guesses, because guessing here means replacing a binary.
func performSwap(updater *update.Updater, action SwapAction, opts update.ApplyOptions) error {
	switch action {
	case SwapUpdate:
		return updater.ApplyUpdate(opts)
	case SwapRollback:
		return updater.RollbackUpdate(opts)
	default:
		return fmt.Errorf("svc: no binary swap named %q", string(action))
	}
}

// swapBinary hands the running binary over to the platform swap script.
//
// Relaunch is false and stays false for every command in this package, for the
// reason ApplyOptions gives: `svc` with no arguments launches the GUI, so a
// script that restarted the binary would open a window at the end of a terminal
// command. Leaving it off is also what sends the script's stdio to the null
// device, and that matters twice over here -- the script outlives this process,
// so its echo lines would otherwise land on a stdout that has already been
// closed, or in --json mode after the document that is meant to be the whole of
// the stream.
func (r *runner) swapBinary(updater *update.Updater, action SwapAction) error {
	return r.swap(updater, action, update.ApplyOptions{Relaunch: false})
}

// SelfApplyResult is what `svc self update` and `svc self rollback` report
// through --json.
//
// The envelope is the CLI's own -- no service struct means "one binary was
// handed to a swap script" -- but every field is a fact about the run. From is
// the version that was running, and To is the version the swap puts in place
// when the command knows it, which `update` does and `rollback` does not.
type SelfApplyResult struct {
	// Action is the command that ran: "update" or "rollback".
	Action string `json:"action"`
	// Applied reports whether a swap was actually handed over. False only for
	// `update` on a build that is already the newest, which is a success rather
	// than a failure -- the window toasts "you are up to date" and does nothing
	// else. A document is emitted either way so that a cron job can run
	// `svc self update --json --yes` unconditionally and always parse one.
	Applied bool `json:"applied"`
	// From is the version that was running when the command started.
	From string `json:"from"`
	// To is the version the swap puts in place. Empty when nothing was applied,
	// and for a rollback: what comes back is whatever the .bak holds, and
	// reading that would mean running the binary being restored.
	To string `json:"to,omitempty"`
	// Filename is the release asset that was downloaded and swapped in. Only
	// `update` sets it.
	Filename string `json:"filename,omitempty"`
}

// reportSelfApply writes the result of a swap. Exactly one of the two shapes
// reaches stdout in any given mode, for the reason reportMutation gives.
//
// The summary is the hand-over sentence rather than a success toast, and the
// difference is the whole point of the command: nothing has been installed yet
// when this prints. A script that went out through detach, it waits for this
// process to exit, and only then does it move anything.
func (r *runner) reportSelfApply(result SelfApplyResult, summary string) error {
	if err := r.printer.JSON(result); err != nil {
		return err
	}
	if summary != "" {
		r.printLine("%s", summary)
	}
	return nil
}

func (r *runner) newSelfUpdateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Download the newest svc release and swap it in",
		Long: "Check the release feed, download the asset for this platform, verify its\n" +
			"checksum, and hand the running binary to a swap script -- the same four steps\n" +
			"the window's update modal runs, in the same order.\n" +
			"\n" +
			"The swap cannot happen inside this process, because a running binary cannot\n" +
			"replace itself. So the script waits for svc to exit, moves the current binary\n" +
			"aside as <binary>.bak, moves the download into place, re-verifies the bytes it\n" +
			"just wrote, and deletes itself. `svc self rollback` undoes it from the .bak.\n" +
			"\n" +
			"One thing the command deliberately does not do is start the new version. The\n" +
			"window relaunches itself after a swap, because a window that closed has to\n" +
			"come back; from a terminal the same relaunch would open a window at the end of\n" +
			"a command. svc exits 0 once the script is away -- run it again in a moment.\n" +
			"\n" +
			"Already being up to date is a success rather than a failure: the command says\n" +
			"so, asks nothing, and exits 0.\n" +
			"\n" +
			"The saved proxy and GitHub mirror are honoured and a stored GitHub token is\n" +
			"sent, exactly as for `svc self check`. Neither is printed.\n" +
			"\n" +
			"Exit code is 0 when the swap was handed over or there was nothing to do. A\n" +
			"declined confirmation exits 5, a checksum that did not match exits 4, a feed\n" +
			"or download that could not be reached exits 6, and a release with no asset for\n" +
			"this platform exits 3.",
		Args:              r.noArgs(),
		ValidArgsFunction: r.noFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSelfUpdate()
		},
	}
}

func (r *runner) runSelfUpdate() error {
	// LevelRead, for the reason `self check` gives: the Updater is assembled by
	// Bootstrap, and re-wiring update.NewUpdater here would create a second place
	// that has to know its five arguments. LevelWrite would be wrong as well as
	// wasteful -- its startup runs EnsureSetup and empties the temp cache, side
	// effects no update needs.
	core, err := r.readCore()
	if err != nil {
		return err
	}
	info, err := core.Updater.CheckUpdate()
	if err != nil {
		return err
	}
	current := core.Home.AppInfo.Version
	if !info.HasUpdate {
		return r.reportSelfApply(SelfApplyResult{
			Action:  string(SwapUpdate),
			Applied: false,
			From:    current,
		}, r.printer.T("about.upToDate", nil))
	}

	// The question is the modal's own primary button and the explanation is the
	// line under the modal's title. The window asks twice -- once to download and
	// again to restart -- because its second question is really "restart now or
	// later". A terminal command has no later: the payload sits in a temp file
	// that dies with the run, so the two collapse into one.
	question := r.printer.T("about.downloadAndInstall", nil)
	explanation := versionArrow(r.printer, current, info.LatestVersion)
	if err := r.confirmMutation(question, explanation); err != nil {
		return err
	}

	// The lock is taken only now, for the reason install gives: holding it across
	// a confirmation prompt would let a user who walked away from the question
	// block every other svc process indefinitely. It covers the download and the
	// hand-off and nothing more -- the swap itself runs after this process is
	// gone, which is the entire reason there is a script.
	return r.withOpLock(core, func() error {
		if err := core.Updater.DownloadUpdate(info.DownloadURL, info.Sha256); err != nil {
			return err
		}
		if err := r.swapBinary(core.Updater, SwapUpdate); err != nil {
			return err
		}
		return r.reportSelfApply(SelfApplyResult{
			Action:   string(SwapUpdate),
			Applied:  true,
			From:     current,
			To:       info.LatestVersion,
			Filename: info.Filename,
		}, r.printer.T("cli.selfUpdateApplied", nil))
	})
}

func (r *runner) newSelfRollbackCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback",
		Short: "Restore the svc binary the previous update replaced",
		Long: "Put back the binary the last update moved aside as <binary>.bak, through the\n" +
			"same swap script `svc self update` uses: svc exits, the script restores the\n" +
			"backup over the current binary, and deletes itself.\n" +
			"\n" +
			"A backup exists only after an update this installation performed itself. A\n" +
			"fresh install, a package-manager upgrade and a binary copied in by hand all\n" +
			"leave nothing to roll back to; the command then exits 3 and names the path it\n" +
			"looked at. The window disables its Rollback button in that state instead,\n" +
			"because a button has to say something before it is clicked and a terminal\n" +
			"does not -- which is also why the question comes first here: only the service\n" +
			"knows which of the two backup names it looked for.\n" +
			"\n" +
			"The version that comes back is whatever the .bak holds, and is not reported\n" +
			"beforehand: reading it would mean running the old binary, which is the thing\n" +
			"being restored.\n" +
			"\n" +
			"As with `svc self update`, nothing is relaunched. svc exits 0 once the script\n" +
			"is away -- run it again in a moment.\n" +
			"\n" +
			"Exit code is 0 once the rollback was handed over, 3 when there is no backup to\n" +
			"restore, and 5 when the confirmation was declined.",
		Args:              r.noArgs(),
		ValidArgsFunction: r.noFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSelfRollback()
		},
	}
}

// runSelfRollback asks first and lets the service find the backup afterwards,
// which inverts the window's order -- handleRollback re-checks HasUpdateBackup
// before opening its modal so a missing backup produces a notice rather than a
// question. The inversion is deliberate. HasBackup answers with a bool, while
// RollbackUpdate's own stat is the authoritative one and its error names the
// exact path it looked at, including the old-named .bak a rename migration can
// leave behind; reporting that is worth asking one question that can still be
// answered no. --yes, the scripted case, never sees a question at all.
func (r *runner) runSelfRollback() error {
	core, err := r.readCore()
	if err != nil {
		return err
	}
	current := core.Home.AppInfo.Version

	// The question is the window's own, but its explanation is not: the GUI's
	// settings.rollbackConfirmDesc ends by promising the app will restart, which
	// is exactly what a terminal swap must not do. Confirming against a sentence
	// the command then declines to honour would be worse than wording it afresh.
	question := r.printer.T("settings.rollbackConfirm", nil)
	explanation := r.printer.T("cli.selfRollbackDesc", nil)
	if err := r.confirmMutation(question, explanation); err != nil {
		return err
	}

	return r.withOpLock(core, func() error {
		if err := r.swapBinary(core.Updater, SwapRollback); err != nil {
			return err
		}
		return r.reportSelfApply(SelfApplyResult{
			Action:  string(SwapRollback),
			Applied: true,
			From:    current,
		}, r.printer.T("cli.selfRollbackApplied", nil))
	})
}
