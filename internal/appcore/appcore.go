// Package appcore assembles the service graph that every svc shell shares.
//
// The GUI and the CLI must produce byte-identical on-disk state for the same
// operation -- the same config.json, shims.json and shell rc. The only way to
// guarantee that is to hand both shells the same wired-up services, assembled
// in the same order, including the self-heal steps the GUI has always run at
// startup. This package is that single assembly point.
//
// It knows nothing about Wails. Shells pass in a wailsrt.Runtime: the GUI
// supplies the adapter that emits Wails events, the CLI supplies one that
// renders to stdout. Nothing else differs.
//
// The sequence here was lifted verbatim out of App.startup; when adding a
// startup step, add it here (not to a shell) so both shells get it.
package appcore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"svc/internal/config"
	"svc/internal/downloader"
	"svc/internal/importer"
	"svc/internal/installer"
	"svc/internal/logger"
	"svc/internal/pathmgr"
	"svc/internal/pkgmgr"
	"svc/internal/proxy"
	"svc/internal/sdk"
	"svc/internal/settings"
	"svc/internal/shimmanager"
	"svc/internal/storage"
	"svc/internal/update"
	"svc/internal/wailsrt"
)

// Level selects how much of the startup sequence Bootstrap runs.
//
// The split follows side effects, not cost: HealActiveVersions and
// RefreshStaleShims are cheap (only RustFetcher implements LayoutHealer, and
// the shim refresh compares pure values before touching anything), so they run
// for read-only shells too -- that is what lets `svc list` report exactly what
// the GUI shows. EnsureSetup, by contrast, edits the shell rc file or the
// Windows registry PATH, so it stays reserved for shells that are about to
// change something.
type Level int

const (
	// LevelMeta is for shells that need no services at all (svc version,
	// svc help, svc completion). Bootstrap rejects it; those paths must not
	// call Bootstrap.
	LevelMeta Level = iota
	// LevelRead assembles every service and self-heals, but performs no
	// setup side effects. For read-only commands.
	LevelRead
	// LevelWrite adds EnsureSetup, the temp-dir cleanup and the stale
	// rollback-backup cleanup. For commands that mutate state.
	LevelWrite
)

// String renders a Level for logs and test failure messages.
func (l Level) String() string {
	switch l {
	case LevelMeta:
		return "meta"
	case LevelRead:
		return "read"
	case LevelWrite:
		return "write"
	default:
		return fmt.Sprintf("Level(%d)", int(l))
	}
}

// Home holds the pieces both shells need before a wailsrt.Runtime exists.
//
// The GUI has to know the saved theme before wails.Run so the native window
// frame is theme-coloured rather than a blank flash, which is why this is
// split out from Bootstrap instead of being folded into it.
type Home struct {
	Cfg         *config.Config
	Settings    *config.SettingsManager
	ShimMgr     *shimmanager.Manager
	PathMgr     pathmgr.PathManager
	Downloader  *downloader.Downloader
	Proxy       *proxy.Service
	SettingsSvc *settings.Service
	AppInfo     update.AppInfo

	// InstallPathOverride forces the SVC data directory for this process,
	// taking precedence over the InstallPath saved in settings.json. The CLI
	// sets it from --install-path so a one-off command can operate on another
	// home; the GUI leaves it empty. Bootstrap applies it, and nothing ever
	// writes it back to settings.json.
	InstallPathOverride string
}

// Core is the fully assembled service graph handed to a shell.
type Core struct {
	Home      *Home
	RT        wailsrt.Runtime
	Registry  *sdk.Registry
	Storage   *storage.Manager
	Updater   *update.Updater
	PkgMgr    *pkgmgr.Service
	Installer *installer.Service
	Importer  *importer.Service
}

// OpenHome builds a Home against the real user environment. It exits the
// caller's error path when the platform config cannot be resolved at all.
func OpenHome(aboutJSON []byte) (*Home, error) {
	cfg, err := config.NewConfig()
	if err != nil {
		return nil, fmt.Errorf("initialize config: %w", err)
	}
	return NewHome(cfg, aboutJSON), nil
}

// NewHome builds a Home around an already-constructed Config. Tests pass a
// Config pointed at t.TempDir() via SetHomeDir/SetSvcDir so nothing ever
// touches the real ~/.svc.
func NewHome(cfg *config.Config, aboutJSON []byte) *Home {
	logger.Init(cfg.SvcDir())
	logger.Info("Application starting...")

	shimMgr := shimmanager.New(cfg)
	pathMgr := pathmgr.NewPathManager(cfg)
	pathMgr.SetShimManager(shimMgr)

	// settings.json always lives at <homeDir>/.svc/settings.json regardless of
	// any custom install path, because the shim runtime reads it from that fixed
	// location to discover the install path. So this takes the user's home dir,
	// NOT cfg.SvcDir().
	sm := config.NewSettingsManager(cfg.HomeDir())

	return &Home{
		Cfg:         cfg,
		Settings:    sm,
		ShimMgr:     shimMgr,
		PathMgr:     pathMgr,
		Downloader:  downloader.NewDownloader(),
		Proxy:       proxy.New(sm),
		SettingsSvc: settings.New(sm),
		AppInfo:     update.ParseAppInfo(aboutJSON),
	}
}

// Bootstrap wires every service and runs the startup steps for lvl. The
// ordering inside is load-bearing and mirrors what the GUI has always done:
// EnsureSetup first (RefreshStaleShims refuses to touch shims.json without the
// base shim binary), then the layout heal, then the shim refresh (so the tree
// being shimmed is the repaired one).
func (h *Home) Bootstrap(ctx context.Context, rt wailsrt.Runtime, lvl Level) (*Core, error) {
	if rt == nil {
		return nil, errors.New("appcore: a wailsrt.Runtime is required")
	}
	if lvl < LevelRead {
		return nil, fmt.Errorf("appcore: Bootstrap needs LevelRead or LevelWrite, got %s", lvl)
	}

	// Honour an install-path override. Everything below derives paths from
	// SvcDir(), so this has to come first. A shell-supplied override (--install-path)
	// beats the value saved in settings.json: an explicit command-line choice
	// is more recent and more specific than a stored preference, and letting
	// the stored one win would make the flag silently do nothing.
	installPath := h.InstallPathOverride
	if installPath == "" {
		installPath = h.Settings.Get().InstallPath
	}
	if installPath != "" {
		logger.Info("Using custom install path: %s", installPath)
		h.Cfg.SetSvcDir(installPath)
	}

	core := &Core{Home: h, RT: rt}
	core.Registry = sdk.NewRegistry(h.Cfg, h.Settings)
	logger.Info("SDK registry initialized with %d SDK types", len(core.Registry.All()))

	core.Storage = storage.NewManager(h.Cfg, core.Registry, h.PathMgr, h.ShimMgr, h.Settings)
	core.Updater = update.NewUpdater(h.AppInfo, h.Settings, h.Downloader, h.Proxy, rt)
	core.PkgMgr = pkgmgr.New(h.Cfg, core.Registry)
	core.Installer = installer.New(h.Cfg, core.Registry, h.Downloader, h.PathMgr, h.ShimMgr, h.Settings, h.Proxy, rt)
	core.Importer = importer.New(h.Cfg, core.Registry, h.PathMgr, h.ShimMgr, rt)

	// Seed ~/.svc/mirrors.json (the editable GitHub mirror list) and point the
	// version cache at ~/.svc/cache. Both must be (re)initialised after any
	// SetSvcDir override above so the files land in the active SVC dir.
	sdk.InitMirrorsFile(h.Cfg.SvcDir())
	sdk.InitVersionCacheDir(h.Cfg.CacheDir())

	if lvl >= LevelWrite {
		// One-time shims setup: creates the shims dir, installs the shim binary
		// and adds the single .svc.rc source line (Unix) or shims PATH entry
		// (Windows). This is the only place SVC ever touches the shell rc or the
		// registry PATH. Failure is not fatal: every command still works, and
		// `svc init` retries it.
		if err := h.ShimMgr.EnsureSetup(); err != nil {
			logger.Warn("Shim setup failed (run 'svc init' to retry): %v", err)
		}
	}

	// Self-heal the extracted layout of every active SDK version. A version
	// installed before a layout fix landed -- Rust's per-component tarball is
	// the case in point -- becomes usable again here, without a reinstall and
	// without the user switching versions.
	core.Installer.HealActiveVersions()

	// Refresh the shims of any SDK whose recorded bin dirs / env vars no longer
	// match its fetcher, so a component added in a later release reaches PATH
	// without the user having to switch versions. Runs after the layout heal so
	// the tree being shimmed is the repaired one.
	core.Installer.RefreshStaleShims()

	if lvl >= LevelWrite {
		cleanTmpDir(h.Cfg.TmpDir())
	}

	logger.Info("Application startup complete")

	if lvl >= LevelWrite {
		// Remove stale old-named rollback backups (e.g. mac SDKVersionControl.bak)
		// left by pre-rename versions; the canonical backup is kept for rollback.
		if exe, err := os.Executable(); err == nil {
			update.CleanStaleBackups(exe)
		}
	}

	return core, nil
}

// cleanTmpDir removes download/extract leftovers from a previous run. Extracted
// so the read/observe behaviour is testable without a Bootstrap.
func cleanTmpDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return
	}
	logger.Info("Cleaning up %d temporary files from previous run", len(entries))
	for _, e := range entries {
		os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}
