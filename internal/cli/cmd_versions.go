package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"svc/internal/apperr"
	"svc/internal/detach"
	"svc/internal/logger"
	"svc/internal/sdk"
	"svc/internal/svclock"
)

// refreshCommandName is the hidden carrier `svc versions` spawns to refresh the
// cached version list from a process that outlives it.
//
// The double underscore follows cobra's own convention for internal commands
// (`__complete`) and keeps the name out of the way of anything a user or an SDK
// would type; dispatch_test.go pins that it collides with neither an SDK binary
// nor one of the legacy spellings. argv[0] stays the svc executable itself,
// which shim.IsShimMode already excludes via isAppName, so the child never
// mistakes itself for a shim.
const refreshCommandName = "__refresh"

// errNegativeLimit is the refusal of a --limit that cannot mean anything. It is
// a usage mistake rather than a service failure, so it exits 2 like a bad flag.
var errNegativeLimit = errors.New("--limit must not be negative")

// ltsBadge is the LTS marker. Hardcoded rather than translated because the GUI
// hardcodes it too: DetailPanel.tsx renders the literal string "LTS" in its
// badge, and an acronym is not a sentence there is a wording to share.
const ltsBadge = "LTS"

// VersionSource is where `svc versions` takes its list from.
type VersionSource int

const (
	// SourceCache prints the list that is already on disk.
	SourceCache VersionSource = iota
	// SourceNetwork fetches the list before printing it.
	SourceNetwork
	// SourceNone means there is nothing to print and the command must fail.
	SourceNone
)

// String names the source for logs and test failures.
func (s VersionSource) String() string {
	switch s {
	case SourceCache:
		return "cache"
	case SourceNetwork:
		return "network"
	case SourceNone:
		return "none"
	default:
		return "VersionSource(" + strconv.Itoa(int(s)) + ")"
	}
}

// versionsFlags are the flags `svc versions` owns. They live in a struct rather
// than as four closures over locals so the decision functions below can take
// the whole set and be tested without building a cobra command.
type versionsFlags struct {
	lts     bool
	limit   int
	offline bool
	refresh bool
}

func (r *runner) newVersionsCommand() *cobra.Command {
	var flags versionsFlags
	command := &cobra.Command{
		Use:   "versions <sdk>",
		Short: "List the versions of an SDK that can be installed",
		Long: "List the versions of an SDK that svc can install, newest first, marking the newest\n" +
			"one, the ones already installed, the active one, and the LTS releases -- the same\n" +
			"badges the version panel in the GUI draws.\n" +
			"\n" +
			"The list comes from the cache when there is one, and a detached background process\n" +
			"refreshes it so the next run sees fresher data. Nothing is printed on stdout while\n" +
			"that happens, and its outcome cannot change this command's exit code.\n" +
			"\n" +
			"--refresh fetches the list now instead of printing the cache. --offline never\n" +
			"touches the network and exits 3 when nothing is cached; it also wins over\n" +
			"--refresh, because \"no network\" is the stronger promise of the two.",
		Args:              r.exactArgs(1),
		ValidArgsFunction: r.sdkCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runVersions(cmd, flags, args[0])
		},
	}
	command.Flags().BoolVar(&flags.lts, "lts", false, "list only long-term-support versions")
	command.Flags().IntVar(&flags.limit, "limit", 0, "print at most N versions (0 prints all of them)")
	command.Flags().BoolVar(&flags.offline, "offline", false, "never touch the network; print the cached list only")
	command.Flags().BoolVar(&flags.refresh, "refresh", false, "fetch the list now instead of printing the cache")
	return command
}

// runVersions is the command body, split out from the cobra literal so the
// ordering of its decisions is readable: resolve, decide the source, get the
// list, print it.
func (r *runner) runVersions(cmd *cobra.Command, flags versionsFlags, input string) error {
	if flags.limit < 0 {
		return r.usageError(cmd, errNegativeLimit)
	}
	core, err := r.readCore()
	if err != nil {
		return err
	}
	kind, err := r.resolveSdk(core, input)
	if err != nil {
		return err
	}

	cached, cacheHit := sdk.GetCachedVersions(kind)
	// The cooldown predicate is a thunk because sdk.ShouldRefreshVersions both
	// decides and RECORDS: asking it on a path that cannot use the answer would
	// burn the window for the run that genuinely needs it.
	source, spawn := planVersionSource(cacheHit, flags.offline, flags.refresh, func() bool {
		return sdk.ShouldRefreshVersions(kind)
	})
	if source == SourceNone {
		return apperr.New(apperr.NoCachedVersions, map[string]string{"sdk": string(kind)})
	}

	status, err := core.Installer.GetSdkStatus(string(kind))
	if err != nil {
		return err
	}

	versions := cached
	if source == SourceNetwork {
		if versions, err = core.Installer.RefreshRemoteVersions(string(kind)); err != nil {
			return err
		}
	} else if spawn {
		// The freshness note is promised only when the child really started:
		// telling a user a refresh is running when it is not would send them
		// looking for data that will never arrive.
		if err := r.spawnRefresh(kind); err != nil {
			logger.Warn("Cannot start the background version refresh of %s: %v", kind, err)
		} else {
			r.printer.Note("%s", r.printer.T("cli.versionsFromCache", nil))
		}
	}

	matching := versions
	if flags.lts {
		matching = filterLts(versions)
	}
	shown := limitVersions(matching, flags.limit)
	if len(shown) < len(matching) {
		r.printer.Note("%s", r.printer.T("cli.versionsTruncated", map[string]string{
			"shown": strconv.Itoa(len(shown)),
			"total": strconv.Itoa(len(matching)),
		}))
	}

	if err := r.printer.JSON(emptyIfNilVersions(shown)); err != nil {
		return err
	}
	r.printVersions(shown, latestVersionOf(versions), status)
	return nil
}

// printVersions renders the table. The result stream is a table in the text
// modes and nothing at all in JSON mode, where JSON already wrote the document.
func (r *runner) printVersions(shown []sdk.VersionInfo, latest string, status *sdk.SdkStatus) {
	if len(shown) == 0 {
		r.printer.Line("%s", r.printer.T("cli.noVersionsMatch", nil))
		return
	}
	installed := make(map[string]bool, len(status.InstalledVersions))
	for _, version := range status.InstalledVersions {
		installed[version] = true
	}
	table := Table{
		Columns: []Column{
			{Header: r.printer.T("cli.columnVersion", nil)},
			{Header: r.printer.T("cli.columnTags", nil)},
			{Header: r.printer.T("cli.columnReleased", nil)},
		},
	}
	for _, version := range shown {
		table.Rows = append(table.Rows, []string{
			version.Version,
			r.versionTags(version, latest, installed, status.CurrentVersion),
			orNone(version.ReleaseDate),
		})
	}
	r.printer.Table(table)
}

// versionTags renders the badges of one version row in the GUI's order and the
// GUI's words: newest first, then installed, then active, then LTS. The three
// translated ones come from the same detail.* keys DetailPanel.tsx uses, so a
// wording change in the window shows up here without anyone editing this file.
func (r *runner) versionTags(version sdk.VersionInfo, latest string, installed map[string]bool, current string) string {
	var tags []string
	if latest != "" && version.Version == latest {
		tags = append(tags, r.printer.T("detail.latest", nil))
	}
	if installed[version.Version] {
		tags = append(tags, r.printer.T("detail.installed", nil))
	}
	if current != "" && version.Version == current {
		tags = append(tags, r.printer.T("detail.currentConfig", nil))
	}
	if version.IsLTS {
		tags = append(tags, ltsBadge)
	}
	if len(tags) == 0 {
		return placeholderNone
	}
	return strings.Join(tags, ", ")
}

// spawnRefresh starts the detached background refresher and reports whether it
// got as far as starting.
//
// The caller logs a failure and carries on: the user asked for a version list
// and already has one, so a refresh that could not start is a missed
// optimisation rather than a failed command. Reporting rather than swallowing
// here is what lets the caller withhold the "refreshing in the background" note
// instead of promising something that is not happening.
func (r *runner) spawnRefresh(kind sdk.SdkType) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve the svc executable: %w", err)
	}
	if err := r.spawn(exe, refreshArgv(kind, r.opts.InstallPath), ""); err != nil {
		return fmt.Errorf("start %s %s: %w", exe, kind, err)
	}
	return nil
}

// spawnDetached is the production spawn. The empty log file is the point:
// detach.Spawn sends the child's stdout and stderr to the null device instead of
// letting it inherit the parent's, so a background refresh can never write into
// the pipe a foreground `svc versions --json` is being read from.
func spawnDetached(exe string, argv []string, logFile string) error {
	return detach.Spawn(exe, argv, detach.Options{LogFile: logFile})
}

// refreshArgv builds the argument vector of the detached child.
//
// --install-path is forwarded when the parent had one, so the child warms the
// cache of the SAME home the parent printed from; without it, `svc
// --install-path X versions go` would refresh the default home and X would stay
// stale forever. The kind is the resolved canonical identifier, never what the
// user typed, so an alias such as `node` reaches the child as `nodejs`.
func refreshArgv(kind sdk.SdkType, installPath string) []string {
	argv := []string{refreshCommandName, string(kind)}
	if installPath != "" {
		argv = append(argv, "--install-path", installPath)
	}
	return argv
}

// planVersionSource decides where the list comes from and whether a detached
// refresh should be started. Pure -- the cooldown arrives as a thunk -- so the
// whole policy is a truth table rather than something that needs a cache, a
// network and a subprocess to check.
//
// Two rules are worth stating:
//
//   - --offline beats --refresh. Both cannot be honoured, and "never touch the
//     network" is the one a script relies on for determinism.
//   - A cache MISS never spawns. It fetches synchronously instead: the first run
//     has to show a real list, and spawning a child to fetch what the parent is
//     about to fetch itself would double the request for no benefit.
func planVersionSource(cacheHit, offline, forceRefresh bool, due func() bool) (VersionSource, bool) {
	if offline {
		if cacheHit {
			return SourceCache, false
		}
		return SourceNone, false
	}
	if cacheHit && !forceRefresh {
		return SourceCache, due != nil && due()
	}
	return SourceNetwork, false
}

// filterLts narrows a list to its long-term-support releases. It always returns
// a fresh slice so a caller cannot mutate the cached list through it.
func filterLts(versions []sdk.VersionInfo) []sdk.VersionInfo {
	matching := make([]sdk.VersionInfo, 0, len(versions))
	for _, version := range versions {
		if version.IsLTS {
			matching = append(matching, version)
		}
	}
	return matching
}

// limitVersions truncates a list to at most limit entries. A limit of zero or
// less means "all of them", which is what makes `--limit` optional without a
// second flag to say so.
//
// Truncation happens AFTER filtering: `--lts --limit 5` has to mean "the five
// newest LTS releases", not "whichever LTS releases happen to sit inside the
// five newest of any kind" -- the second reading silently returns fewer than
// five rows and looks like a bug.
func limitVersions(versions []sdk.VersionInfo, limit int) []sdk.VersionInfo {
	if limit <= 0 || len(versions) <= limit {
		return versions
	}
	return versions[:limit]
}

// latestVersionOf returns the newest version of a remote list.
//
// The list is descending by contract -- sdk.VersionFetcher.FetchRemoteVersions
// documents it -- and the GUI relies on the same: DetailPanel.tsx derives its
// "latest" badge from versions[0] of the UNFILTERED list, with a comment saying
// a filtered index 0 would mislabel whatever row matched a search first. Reading
// the unfiltered head here is what makes the two shells agree about which
// version is newest when --lts or --limit is in play.
func latestVersionOf(versions []sdk.VersionInfo) string {
	if len(versions) == 0 {
		return ""
	}
	return versions[0].Version
}

// emptyIfNilVersions turns a nil slice into an empty one so --json encodes []
// rather than null. A consumer that ranges over the result should not have to
// know which of the two Go happened to produce.
func emptyIfNilVersions(versions []sdk.VersionInfo) []sdk.VersionInfo {
	if versions == nil {
		return []sdk.VersionInfo{}
	}
	return versions
}

// newRefreshCommand builds the hidden carrier that does the background work
// `svc versions` hands off. Three properties are the contract:
//
//   - Silent. It writes nothing to stdout or stderr. Its own descriptors are the
//     null device anyway (detach.Spawn never lets a child inherit them), but a
//     stray line would still land in the log or in a terminal when someone runs
//     it by hand to debug.
//   - Single-flight. It takes svclock.TryAcquire on the CACHE dir and gives up
//     when that lock is held, so twenty scripted `svc versions` calls in a row
//     cannot leave twenty processes fetching the same list. This is deliberately
//     not the operation lock in the SVC dir: a background refresh must never be
//     able to block -- or be blocked by -- an install.
//   - Always exit 0. Its exit status is unobservable by design (nothing waits for
//     it), and a failure it reported as non-zero would be a failure nobody could
//     act on. Problems go to the logger, which `svc logs show` reads.
//
// It never takes the operation lock and never mutates SDK state itself; the only
// write it performs is the version cache file.
func (r *runner) newRefreshCommand() *cobra.Command {
	return &cobra.Command{
		Use:    refreshCommandName + " <sdk>",
		Short:  "Refresh the cached version list of an SDK in the background (internal)",
		Hidden: true,
		Args:   r.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r.runRefresh(args[0])
			return nil
		},
	}
}

// runRefresh is the child's whole job. Every exit path returns without an error
// so the command exits 0 and stays silent; the reason goes to the log.
func (r *runner) runRefresh(input string) {
	core, err := r.readCore()
	if err != nil {
		logger.Warn("Background version refresh gave up on %s: %v", input, err)
		return
	}
	kind, err := ResolveSdkType(core.Registry, input)
	if err != nil {
		logger.Warn("Background version refresh cannot resolve %q: %v", input, err)
		return
	}

	// The cooldown was already consumed by the parent that decided to spawn, so
	// the child does not ask again: it only has to make sure it is the only one
	// running. TryAcquire rather than Acquire -- waiting would defeat the point,
	// since another refresh doing the same fetch is already in progress.
	cacheDir := core.Home.Cfg.CacheDir()
	lock, err := svclock.TryAcquire(cacheDir)
	if err != nil {
		if svclock.IsBusy(err) {
			logger.Info("Background version refresh of %s skipped: %s is already held", kind, svclock.LockPath(cacheDir))
			return
		}
		logger.Warn("Background version refresh of %s cannot take %s: %v", kind, svclock.LockPath(cacheDir), err)
		return
	}
	defer lock.Release()

	if _, err := core.Installer.RefreshRemoteVersions(string(kind)); err != nil {
		logger.Warn("Background version refresh failed for %s: %v", kind, err)
	}
}
