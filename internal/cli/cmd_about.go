package cli

import (
	"github.com/spf13/cobra"
)

// `svc about` is the GUI's About tab as a result stream: the four facts
// about.json carries, under the labels that tab already uses.
//
// It is the only command that needs no service graph at all. update.ParseAppInfo
// ran when the home was opened during option resolution, so the values are
// already sitting on r.home.AppInfo, and bootstrapping to read them would
// self-heal fourteen SDK layouts and rewrite shims.json to answer a question
// about the binary that is running. This is appcore.LevelMeta in the design's
// vocabulary -- the level that needs the config and nothing built on top of it
// -- and LevelMeta is spelled "do not bootstrap" because Bootstrap refuses
// anything below LevelRead.
//
// The JSON shape is update.AppInfo verbatim rather than an envelope around it,
// because that is precisely what the GetAppInfo binding hands the window: one
// schema, two shells.
func (r *runner) newAboutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "about",
		Short: "Print svc's own version, license and repository",
		Long: "Print what this svc build says about itself: its version, the Go version it was\n" +
			"built with, its license and its repository.\n" +
			"\n" +
			"These are the four rows of the window's About tab, read from the about.json\n" +
			"embedded in the binary -- not from a file on disk, so they cannot disagree with\n" +
			"the executable that is answering.\n" +
			"\n" +
			"'svc version' keeps printing the single legacy line it has always printed; this\n" +
			"command is the one that reports the rest, and 'svc self check' is the one that\n" +
			"asks whether a newer build exists.",
		Args:              r.noArgs(),
		ValidArgsFunction: r.noFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runAbout()
		},
	}
}

func (r *runner) runAbout() error {
	if r.home == nil {
		if err := r.resolveOptions(); err != nil {
			return err
		}
	}
	info := r.home.AppInfo
	if err := r.printer.JSON(info); err != nil {
		return err
	}
	// The version carries its "v" the way the About tab renders it. The other
	// three go through orNone: the GUI would show an empty row where a
	// hand-edited about.json dropped a field, and an empty cell at the end of
	// an aligned block reads as a rendering bug rather than as a missing fact.
	r.printer.KeyValue([]Pair{
		{Key: r.printer.T("about.version", nil), Value: "v" + info.Version},
		{Key: r.printer.T("about.goVersion", nil), Value: orNone(info.GoVersion)},
		{Key: r.printer.T("about.license", nil), Value: orNone(info.License)},
		{Key: r.printer.T("about.repo", nil), Value: orNone(info.RepoURL)},
	})
	return nil
}
