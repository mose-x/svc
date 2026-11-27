package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"svc/internal/update"
)

// `svc self` is the group about the svc binary rather than about an SDK: what
// version is running, whether a newer one is published, and -- once the writing
// half lands -- swapping one for the other.
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
	r.printLine("%s → v%s",
		r.printer.T("about.currentVersion", map[string]string{"version": result.Current}),
		info.LatestVersion)

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
