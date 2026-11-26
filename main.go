package main

import (
	"embed"
	"fmt"
	"os"

	"svc/internal/cli"
	"svc/internal/config"
	"svc/internal/logger"
	"svc/internal/shim"
	"svc/internal/shimmanager"
	"svc/internal/update"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// 1. If invoked as a shim (argv[0] is a known SDK command name), run the
	//    shim: it looks up the real binary and execs it.
	if shim.IsShimMode() {
		shim.Run()
		return
	}

	// 2. Everything else is decided by cli.Dispatch: the four commands that
	//    predate the CLI keep their original code path and their original
	//    output, the command tree gets the rest, and no arguments at all still
	//    launches the GUI.
	switch cli.Dispatch(os.Args[1:]) {
	case cli.KindLegacyInit:
		runInitCLI()
	case cli.KindLegacyVersion:
		printVersion()
	case cli.KindCommand:
		os.Exit(cli.Execute(cli.ExecuteInput{
			Args:      os.Args[1:],
			Catalogs:  cliCatalogs(),
			AboutJSON: aboutJSON,
			Stdin:     os.Stdin,
			Stdout:    os.Stdout,
			Stderr:    os.Stderr,
		}))
	default:
		runGUI()
	}
}

// runGUI launches the Wails application. It stays the default for `svc` with no
// arguments, which is the one behaviour of the original dispatcher that the CLI
// must not change.
func runGUI() {
	app := NewApp()

	// Match the native window background to the saved theme so the frame shown
	// before the WebView's first paint is theme-coloured, not a blank flash.
	bg := &options.RGBA{R: 0x14, G: 0x14, B: 0x14, A: 0xff}
	if app.settings != nil && app.settings.Get().Theme == "light" {
		bg = &options.RGBA{R: 0xf5, G: 0xf5, B: 0xf5, A: 0xff}
	}

	err := wails.Run(&options.App{
		Title:            "svc",
		Width:            1200,
		Height:           800,
		DisableResize:    true,
		BackgroundColour: bg,
		// Disallow a second instance: a duplicate launch would open a second
		// window over the first. The lock is held for the process lifetime on
		// all platforms (win/mac/linux).
		SingleInstanceLock: &options.SingleInstanceLock{UniqueId: "svc"},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		// L2: use fmt.Fprintf(os.Stderr, ...) instead of the builtin println,
		// matching the error-reporting style used elsewhere in this file and
		// giving a guaranteed, formatted stderr message.
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// runInitCLI performs one-time shim setup from the command line.
// It creates the shims directory, installs the shim binary, and adds the
// single .svc.rc source line to the user's shell config (or registry PATH on
// Windows). After running "svc init", the user only needs to open a new shell.
func runInitCLI() {
	cfg, err := config.NewConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize config: %v\n", err)
		os.Exit(1)
	}

	// Honour a custom install path saved in settings.json. settings.json lives
	// at the fixed <home>/.svc location regardless of any custom install path
	// -- the shim runtime reads it from there to discover that path -- so this
	// is HomeDir(), not SvcDir(). With SvcDir() the manager looked in
	// ~/.svc/.svc/ for a default install and silently ignored every custom one.
	sm := config.NewSettingsManager(cfg.HomeDir())
	if s := sm.Get(); s.InstallPath != "" {
		cfg.SetSvcDir(s.InstallPath)
	}

	logger.Init(cfg.SvcDir())

	mgr := shimmanager.New(cfg)
	if err := mgr.EnsureSetup(); err != nil {
		fmt.Fprintf(os.Stderr, "Setup failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("SVC shims setup complete.")
	fmt.Printf("  SVC home:  %s\n", cfg.SvcDir())
	fmt.Printf("  Shims dir: %s\n", cfg.ShimsDir())
	fmt.Printf("  RC file:   %s\n", cfg.RcFilePath())
	fmt.Println()
	fmt.Println("Open a new terminal window, then install/switch SDKs from the app.")
}

func printVersion() {
	info := update.ParseAppInfo(aboutJSON)
	if info.Version == "" {
		fmt.Fprintln(os.Stdout, "svc 0.1.0")
		return
	}
	fmt.Fprintf(os.Stdout, "svc %s\n", info.Version)
}
