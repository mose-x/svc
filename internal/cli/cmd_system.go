package cli

import (
	"strconv"

	"github.com/spf13/cobra"

	"svc/internal/logmgr"
	"svc/internal/pathmgr"
	"svc/internal/sdk"
	"svc/internal/storage"
)

func (r *runner) newPathCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "List the SDK-related entries on the system PATH",
		Long: "List every PATH entry that looks like an SDK, with what svc identified it as and\n" +
			"whether svc already manages it.\n" +
			"\n" +
			"The GUI's PATH page narrows this list to the entries that can be imported; the\n" +
			"command shows all of them and marks the managed ones instead, because on a\n" +
			"terminal the interesting question is usually \"why does `node` resolve to\n" +
			"something that is not the version I switched to\".\n" +
			"\n" +
			"Entries owned by another version manager or by a protected OS path are marked\n" +
			"as such: svc will not import those.",
		Args: r.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			entries, err := core.Home.PathMgr.GetAllPathEntries()
			if err != nil {
				return err
			}
			if err := r.printer.JSON(entries); err != nil {
				return err
			}
			r.printPathEntries(entries)
			return nil
		},
	}
}

func (r *runner) printPathEntries(entries []pathmgr.PathEntry) {
	if len(entries) == 0 {
		r.printer.Line("%s", r.printer.T("path.emptyPath", nil))
		return
	}
	table := Table{
		Columns: []Column{
			{Header: r.printer.T("path.sdkColumn", nil)},
			{Header: r.printer.T("cli.columnPath", nil)},
			{Header: r.printer.T("cli.columnState", nil)},
		},
	}
	for _, entry := range entries {
		name := r.printer.T("path.noSdkDetected", nil)
		if entry.SdkType != "" {
			name = sdk.SdkDisplayName(sdk.SdkType(entry.SdkType))
		}
		table.Rows = append(table.Rows, []string{name, entry.Path, r.pathEntryState(entry)})
	}
	r.printer.Table(table)
}

// pathEntryState names what can be done with a PATH entry, in the GUI's words.
// Managed comes first because it is the only state where nothing can be done;
// protected and externally-managed follow because both are reasons svc will
// refuse an import, and only what is left is actually importable.
func (r *runner) pathEntryState(entry pathmgr.PathEntry) string {
	switch {
	case entry.IsManaged:
		return r.printer.T("app.configured", nil)
	case entry.SystemProtected:
		return r.printer.T("app.systemManaged", nil)
	case entry.ExternalManager != "":
		return r.printer.T("app.externalManager", map[string]string{"manager": ExternalManagerLabel(entry.ExternalManager)})
	case entry.SdkType != "":
		return r.printer.T("cli.stateImportable", nil)
	default:
		return placeholderNone
	}
}

// StorageResult is what `svc storage` reports: the per-SDK breakdown the GUI's
// cache panel shows, plus the two totals it computes in the component.
type StorageResult struct {
	// SDKs is GetStorageInfo. Always an array, never null -- this is a CLI
	// envelope rather than a service value, so it owes a consumer a shape it
	// can take the length of without a nil check.
	SDKs []storage.StorageInfo `json:"sdks"`
	// TmpCacheBytes is GetTmpCacheSize: download and extraction leftovers.
	TmpCacheBytes int64 `json:"tmpCacheBytes"`
	// TotalBytes is the sum of every SDK directory, excluding the temp cache.
	TotalBytes int64 `json:"totalBytes"`
}

func (r *runner) newStorageCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "storage",
		Short: "Show how much disk space each SDK takes",
		Long: "Show the disk usage of every SDK directory, how many versions each holds, which\n" +
			"one is active, and the size of the leftover download cache.\n" +
			"\n" +
			"Nothing is removed; 'svc clean tmp' and 'svc clean inactive <sdk>' do that.",
		Args: r.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			infos := core.Storage.GetStorageInfo()
			if infos == nil {
				infos = []storage.StorageInfo{}
			}
			var total int64
			for _, info := range infos {
				total += info.TotalSize
			}
			result := StorageResult{
				SDKs:          infos,
				TmpCacheBytes: core.Storage.GetTmpCacheSize(),
				TotalBytes:    total,
			}
			if err := r.printer.JSON(result); err != nil {
				return err
			}
			r.printStorage(result)
			return nil
		},
	}
}

func (r *runner) printStorage(result StorageResult) {
	if len(result.SDKs) == 0 {
		r.printer.Line("%s", r.printer.T("cli.noStorage", nil))
		return
	}
	table := Table{
		Columns: []Column{
			{Header: r.printer.T("path.sdkColumn", nil)},
			{Header: r.printer.T("cli.columnVersions", nil), Right: true},
			{Header: r.printer.T("cli.columnActive", nil)},
			{Header: r.printer.T("cli.columnSize", nil), Right: true},
			{Header: r.printer.T("cli.columnDirectory", nil)},
		},
	}
	for _, info := range result.SDKs {
		table.Rows = append(table.Rows, []string{
			info.DisplayName,
			strconv.Itoa(info.VersionCount),
			orNone(info.ActiveVer),
			FormatBytes(info.TotalSize),
			info.SdkDir,
		})
	}
	r.printer.Table(table)

	r.printer.Blank()
	r.printer.KeyValue([]Pair{
		{Key: r.printer.T("cli.totalSize", nil), Value: FormatBytes(result.TotalBytes)},
		{Key: r.printer.T("settings.tmpCache", nil), Value: FormatBytes(result.TmpCacheBytes)},
	})
}

// newLogsCommand builds `svc logs` and its three read-only subcommands. The
// destructive half of the GUI's log panel (`clean`, `rm`) is a change and
// belongs to P4, where it gets a confirmation prompt.
func (r *runner) newLogsCommand() *cobra.Command {
	logs := &cobra.Command{
		Use:   "logs",
		Short: "Inspect svc's own log files",
	}
	logs.AddCommand(r.newLogsListCommand(), r.newLogsShowCommand(), r.newLogsDirCommand())
	return logs
}

func (r *runner) newLogsListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the log files",
		Args:  r.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			files, err := logmgr.GetLogFiles()
			if err != nil {
				return err
			}
			if err := r.printer.JSON(files); err != nil {
				return err
			}
			if len(files) == 0 {
				r.printer.Line("%s", r.printer.T("logs.noLogs", nil))
				return nil
			}
			table := Table{
				Columns: []Column{
					{Header: r.printer.T("settings.filename", nil)},
					{Header: r.printer.T("logs.size", nil), Right: true},
					{Header: r.printer.T("logs.modified", nil)},
				},
			}
			for _, file := range files {
				table.Rows = append(table.Rows, []string{file.Name, FormatBytes(file.Size), file.ModTime})
			}
			r.printer.Table(table)
			return nil
		},
	}
}

func (r *runner) newLogsShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <file>",
		Short: "Print one log file",
		Long: "Print a log file verbatim, so it can be piped into grep or tail.\n" +
			"\n" +
			"The name is a bare file name from 'svc logs list', not a path: logmgr validates\n" +
			"it as a single path segment, so neither ../ nor an absolute path is accepted.",
		Args: r.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := logmgr.GetLogContent(args[0])
			if err != nil {
				return err
			}
			if err := r.printer.JSON(content); err != nil {
				return err
			}
			if content == "" {
				// Nothing to print is a result, not a failure -- but a human
				// staring at an empty terminal deserves to be told the file is
				// empty rather than left wondering whether the command ran.
				r.printer.Note("%s", r.printer.T("settings.noContent", nil))
				return nil
			}
			r.printer.Line("%s", content)
			return nil
		},
	}
}

func (r *runner) newLogsDirCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "dir",
		Short: "Print the log directory",
		Args:  r.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := logmgr.GetLogDir()
			if err := r.printer.JSON(dir); err != nil {
				return err
			}
			r.printLine("%s", dir)
			return nil
		},
	}
}
