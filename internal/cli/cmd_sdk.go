package cli

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"svc/internal/sdk"
)

// placeholderNone is what a table cell shows when there is nothing to show. A
// dash rather than an empty string: an empty cell in a sparse column reads as
// a rendering bug, and it makes the columns visually line up.
const placeholderNone = "-"

// newListCommand builds `svc list`.
//
// The result is []sdk.SdkStatus verbatim -- the exact value GetAllSdkStatus
// returns to the GUI -- so there is no second schema to keep in sync. That is
// also why the table has no conflict column: conflicts come from a second
// call, and folding them in would mean either inventing a CLI-only envelope or
// making --json and the table disagree about what the command reports.
// `svc conflicts <sdk>` and `svc status <sdk>` are where conflicts live.
func (r *runner) newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List every SDK and its local state",
		Long: "List every SDK svc knows about: the active version, how many versions are\n" +
			"installed, and whether a copy exists on PATH.\n" +
			"\n" +
			"This is a purely local scan. Nothing is downloaded and no remote version\n" +
			"list is fetched; use 'svc versions <sdk>' for that.",
		Args: r.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			statuses := core.Installer.GetAllSdkStatus()
			if err := r.printer.JSON(statuses); err != nil {
				return err
			}
			r.printStatusTable(statuses)
			return nil
		},
	}
}

// printStatusTable renders the shared SDK/status table used by `list` and by
// `current` with no argument.
func (r *runner) printStatusTable(statuses []sdk.SdkStatus) {
	table := Table{
		Columns: []Column{
			{Header: r.printer.T("path.sdkColumn", nil)},
			{Header: r.printer.T("detail.currentVersion", nil)},
			{Header: r.printer.T("detail.installed", nil), Right: true},
			{Header: r.printer.T("cli.columnPath", nil)},
		},
	}
	for _, status := range statuses {
		current := status.CurrentVersion
		if !status.Configured || current == "" {
			current = placeholderNone
		}
		table.Rows = append(table.Rows, []string{
			status.DisplayName,
			current,
			strconv.Itoa(len(status.InstalledVersions)),
			r.pathState(status),
		})
		// A dangling active version is the one state a reader must not miss:
		// every shim for that SDK is currently broken.
		if status.NeedsSwitch {
			table.RowTones = append(table.RowTones, ToneError)
			continue
		}
		table.RowTones = append(table.RowTones, TonePlain)
	}
	r.printer.Table(table)

	// The two conditions the GUI raises a banner for. They go to stderr as
	// notes, so a piped table stays clean and a script never has to filter
	// prose out of its result.
	for _, status := range statuses {
		if status.NeedsSwitch {
			r.printer.Note("%s: %s", status.DisplayName, r.printer.T("detail.needsSwitch", nil))
		}
		if status.ExternalManager != "" {
			r.printer.Note("%s: %s", status.DisplayName,
				r.printer.T("detail.externalManagerHint", map[string]string{"manager": status.ExternalManager}))
		}
	}
}

// pathState names how a copy of the SDK relates to PATH, using the GUI's own
// four words. The precedence matches the home page's chip label, with
// system-managed and externally-managed checked first because both are reasons
// svc will refuse to import the copy -- the more specific answer is the useful
// one.
func (r *runner) pathState(status sdk.SdkStatus) string {
	switch {
	case status.Configured:
		return r.printer.T("app.configured", nil)
	case status.SystemProtected:
		return r.printer.T("app.systemManaged", nil)
	case status.ExternalManager != "":
		return r.printer.T("app.externalManager", map[string]string{"manager": status.ExternalManager})
	case status.PathConfigured:
		return r.printer.T("app.inPathOnly", nil)
	default:
		return r.printer.T("app.notConfigured", nil)
	}
}

// StatusResult is what `svc status` reports.
//
// This one is a composite, and deliberately so: the GUI's detail panel makes
// three separate binding calls (GetSdkStatus, CheckSystemConflicts,
// DetectPathVersion) and assembles them in React. A CLI command answers once,
// so the assembly has to happen here. Each field is still the service value
// verbatim; only the envelope is new.
type StatusResult struct {
	// Status is GetSdkStatus, the same object the GUI renders.
	Status *sdk.SdkStatus `json:"status"`
	// Conflicts is CheckSystemConflicts. Always an array, never null, so a
	// consumer can take its length without a nil check.
	Conflicts []string `json:"conflicts"`
	// PathVersion is DetectPathVersion: what the copy on PATH reports itself
	// as, which is empty when there is no copy or it does not answer.
	PathVersion string `json:"pathVersion"`
	// InstallDir is GetInstallDir.
	InstallDir string `json:"installDir"`
}

func (r *runner) newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status <sdk>",
		Short: "Show one SDK's local state, PATH copy and system conflicts",
		Long: "Show everything svc knows about one SDK locally: the active version, every\n" +
			"installed version, the install directory, what the copy on PATH reports\n" +
			"itself as, and any system-level environment configuration that competes\n" +
			"with the shims.\n" +
			"\n" +
			"Accepts the SDK identifier in any case (nodejs, NodeJS) or its display\n" +
			"name (Node.js, .NET).",
		Args:              r.exactArgs(1),
		ValidArgsFunction: r.sdkCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			kind, err := r.resolveSdk(core, args[0])
			if err != nil {
				return err
			}
			status, err := core.Installer.GetSdkStatus(string(kind))
			if err != nil {
				return err
			}
			conflicts, err := core.Installer.CheckSystemConflicts(string(kind))
			if err != nil {
				return err
			}
			result := StatusResult{
				Status:      status,
				Conflicts:   emptyIfNil(conflicts),
				PathVersion: core.Installer.DetectPathVersion(string(kind)),
				InstallDir:  core.Installer.GetInstallDir(string(kind)),
			}
			if err := r.printer.JSON(result); err != nil {
				return err
			}
			r.printStatus(result)
			return nil
		},
	}
}

func (r *runner) printStatus(result StatusResult) {
	status := result.Status
	if status == nil {
		return
	}

	current := status.CurrentVersion
	tone := TonePlain
	if !status.Configured || current == "" {
		current = placeholderNone
	}
	if status.NeedsSwitch {
		tone = ToneError
	}

	// A KeyValue block has room for words, unlike a table cell, so "nothing
	// installed" says so instead of showing a dash that reads as a gap.
	installed := r.printer.T("cli.noVersionsInstalled", nil)
	if len(status.InstalledVersions) > 0 {
		labels := make([]string, 0, len(status.InstalledVersions))
		for _, version := range status.InstalledVersions {
			if version == status.CurrentVersion {
				version += r.printer.T("cli.activeSuffix", nil)
			}
			labels = append(labels, version)
		}
		installed = strings.Join(labels, ", ")
	}

	pairs := []Pair{
		{Key: r.printer.T("path.sdkColumn", nil), Value: status.DisplayName},
		{Key: r.printer.T("detail.currentVersion", nil), Value: current, Tone: tone},
		{Key: r.printer.T("detail.installed", nil), Value: installed},
		{Key: r.printer.T("cli.installDir", nil), Value: orNone(result.InstallDir)},
		{Key: r.printer.T("cli.columnPath", nil), Value: r.pathState(*status)},
	}
	if status.PathBinary != "" {
		pairs = append(pairs, Pair{Key: r.printer.T("detail.pathBinaryTitle", nil), Value: status.PathBinary})
	}
	if result.PathVersion != "" {
		pairs = append(pairs, Pair{Key: r.printer.T("app.inPathOnly", nil), Value: result.PathVersion})
	}
	r.printer.KeyValue(pairs)

	if status.NeedsSwitch {
		r.printer.Blank()
		r.printer.Line("%s", r.printer.Paint(r.printer.T("detail.needsSwitch", nil), ToneError))
	}
	if status.ExternalManager != "" {
		r.printer.Blank()
		r.printer.Line("%s", r.printer.T("detail.externalManagerHint",
			map[string]string{"manager": status.ExternalManager}))
	}

	r.printer.Blank()
	r.printLine("%s", r.printer.T("detail.systemConflictTitle", nil))
	r.printConflicts(result.Conflicts)
}

// printConflicts renders a conflict list under its heading. Shared by `status`
// and `conflicts` so the two cannot drift into describing the same thing two
// ways.
func (r *runner) printConflicts(conflicts []string) {
	if len(conflicts) == 0 {
		r.printer.Line("%s", r.printer.T("cli.noConflicts", nil))
		return
	}
	for _, conflict := range conflicts {
		r.printer.Line("  %s", conflict)
	}
}

func (r *runner) newCurrentCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "current [sdk]",
		Short: "Print the active version of one SDK, or of every SDK",
		Long: "With an SDK argument, print only its active version and nothing else, so the\n" +
			"command can be used inline:\n" +
			"\n" +
			"  export NODE_DIR=$(svc dir nodejs)/$(svc current nodejs)\n" +
			"\n" +
			"An SDK with no active version prints nothing and still exits 0: the honest\n" +
			"answer to \"which version is active\" is \"none\", and failing would break\n" +
			"every script that runs under 'set -e'. An unknown SDK name is still exit 3.\n" +
			"\n" +
			"With no argument, list the active version of every SDK. --json emits an\n" +
			"array of status objects in both forms.",
		Args:              r.rangeArgs(0, 1),
		ValidArgsFunction: r.sdkCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				statuses := core.Installer.GetAllSdkStatus()
				if err := r.printer.JSON(statuses); err != nil {
					return err
				}
				r.printStatusTable(statuses)
				return nil
			}

			kind, err := r.resolveSdk(core, args[0])
			if err != nil {
				return err
			}
			status, err := core.Installer.GetSdkStatus(string(kind))
			if err != nil {
				return err
			}
			if err := r.printer.JSON([]sdk.SdkStatus{*status}); err != nil {
				return err
			}
			if status.CurrentVersion != "" {
				r.printLine("%s", status.CurrentVersion)
			}
			return nil
		},
	}
}

func (r *runner) newDirCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "dir <sdk>",
		Short:             "Print the install directory of an SDK",
		Args:              r.exactArgs(1),
		ValidArgsFunction: r.sdkCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			kind, err := r.resolveSdk(core, args[0])
			if err != nil {
				return err
			}
			dir := core.Installer.GetInstallDir(string(kind))
			if err := r.printer.JSON(dir); err != nil {
				return err
			}
			r.printLine("%s", dir)
			return nil
		},
	}
}

func (r *runner) newURLCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "url <sdk> <version>",
		Short: "Print the download URL of an SDK version",
		Long: "Print the URL svc would download for this SDK and version, with the configured\n" +
			"proxy and GitHub mirror already applied.\n" +
			"\n" +
			"This is a troubleshooting command: it exists so a failed or slow download can\n" +
			"be reproduced with curl against the exact URL svc used. It may issue a\n" +
			"network request when the version list is not cached.",
		Args: r.exactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return r.sdkCompletion(cmd, args, toComplete)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			kind, err := r.resolveSdk(core, args[0])
			if err != nil {
				return err
			}
			url, err := core.Installer.GetSdkDownloadURL(string(kind), args[1])
			if err != nil {
				return err
			}
			if err := r.printer.JSON(url); err != nil {
				return err
			}
			r.printLine("%s", url)
			return nil
		},
	}
}

func (r *runner) newConflictsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "conflicts <sdk>",
		Short: "List system environment entries that compete with an SDK's shims",
		Long: "List the system-level environment configuration that competes with the shims\n" +
			"svc installs for this SDK: PATH entries ahead of the shim directory, and\n" +
			"environment variables the SDK's own tooling reads.\n" +
			"\n" +
			"Exits 0 whether or not conflicts were found -- an empty list is a result, not\n" +
			"a failure.",
		Args:              r.exactArgs(1),
		ValidArgsFunction: r.sdkCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			kind, err := r.resolveSdk(core, args[0])
			if err != nil {
				return err
			}
			conflicts, err := core.Installer.CheckSystemConflicts(string(kind))
			if err != nil {
				return err
			}
			conflicts = emptyIfNil(conflicts)
			if err := r.printer.JSON(conflicts); err != nil {
				return err
			}
			r.printLine("%s", r.printer.T("detail.systemConflictTitle", nil))
			r.printConflicts(conflicts)
			return nil
		},
	}
}

// printLine writes one result line. It is Printer.Line with the JSON-mode
// branch already decided by the Printer itself; the alias exists only so the
// call sites in this file read as "print a result" without repeating the
// receiver.
func (r *runner) printLine(format string, args ...any) {
	if r.printer != nil {
		r.printer.Line(format, args...)
	}
}

// emptyIfNil turns a nil slice into an empty one so JSON encodes [] rather
// than null. A consumer that does `.length` or ranges over the field should not
// have to know which of the two Go happened to produce.
func emptyIfNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// orNone renders an empty value as the table placeholder, so a KeyValue block
// never shows a key with nothing after it.
func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return placeholderNone
	}
	return value
}
