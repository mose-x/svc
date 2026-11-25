package appcore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"svc/internal/config"
	"svc/internal/logger"
	"svc/internal/wailsrt"
)

// TestMain pre-empts the logger singleton. NewHome calls logger.Init, which is
// sync.Once-guarded, so the first caller wins and keeps its log file open for
// the whole test binary. Left alone, that first caller is whichever test runs
// first: the log lands inside its t.TempDir(), and on Windows the open handle
// makes the TempDir cleanup fail with a sharing violation. Removal afterwards
// is best-effort for the same reason as app_logging_test.go's initAppLogger.
func TestMain(m *testing.M) {
	base, err := os.MkdirTemp("", "appcore-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "appcore: create temp log dir:", err)
		os.Exit(1)
	}
	logger.Init(base)
	code := m.Run()
	_ = os.RemoveAll(base)
	os.Exit(code)
}

// fakeRuntime is a minimal wailsrt.Runtime. wailsrt.Fake is declared in a
// _test.go file, so it is not importable from this package.
type fakeRuntime struct{ events []string }

func (f *fakeRuntime) Context() context.Context { return context.Background() }

func (f *fakeRuntime) EventsEmit(eventName string, data ...any) {
	f.events = append(f.events, eventName)
}

func (f *fakeRuntime) OpenFileDialog(title string, filters []wailsrt.FileFilter) (string, error) {
	return "", nil
}

func (f *fakeRuntime) OpenDirectoryDialog(title string) (string, error) { return "", nil }

func (f *fakeRuntime) Quit() {}

var testAboutJSON = []byte(`{"version":"9.9.9-test","goVersion":"1.25","license":"MIT License","repoUrl":"https://example.invalid/r","updateUrl":"https://example.invalid/u"}`)

// newTestHome builds a Home rooted in a temp HOME so nothing ever touches the
// real ~/.svc. HOME/USERPROFILE are redirected because os.UserHomeDir (and
// through it config.AvailableShells, which the shim setup walks) reads them
// directly. seed runs after the temp dirs exist but before NewHome, so tests
// can lay down a settings.json that NewHome is expected to pick up.
func newTestHome(t *testing.T, seed func(home string)) (*Home, *config.Config, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", "")
	if seed != nil {
		seed(home)
	}
	cfg, err := config.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	cfg.SetSvcDir(filepath.Join(home, ".svc"))
	cfg.SetHomeDir(home)
	return NewHome(cfg, testAboutJSON), cfg, home
}

// writeSettings seeds <home>/.svc/settings.json -- the fixed location the
// SettingsManager reads, independent of any custom install path.
func writeSettings(t *testing.T, home string, s config.AppSettings) {
	t.Helper()
	dir := filepath.Join(home, ".svc")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
}

// writeTmpLeftover drops a file into the temp dir and returns its path.
func writeTmpLeftover(t *testing.T, cfg *config.Config) string {
	t.Helper()
	if err := os.MkdirAll(cfg.TmpDir(), 0755); err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	p := filepath.Join(cfg.TmpDir(), "leftover.zip")
	if err := os.WriteFile(p, []byte("partial download"), 0644); err != nil {
		t.Fatalf("write leftover: %v", err)
	}
	return p
}

func TestLevelString(t *testing.T) {
	cases := []struct {
		lvl  Level
		want string
	}{
		{LevelMeta, "meta"},
		{LevelRead, "read"},
		{LevelWrite, "write"},
		{Level(99), "Level(99)"},
	}
	for _, c := range cases {
		if got := c.lvl.String(); got != c.want {
			t.Errorf("Level(%d).String() = %q, want %q", int(c.lvl), got, c.want)
		}
	}
}

// TestNewHomeReadsSettingsFromTheFixedHomeLocation guards the invariant that
// settings.json is resolved from HomeDir, never from SvcDir: with a custom
// install path the two differ, and reading the wrong one silently drops the
// user's language, proxy and install-path settings.
func TestNewHomeReadsSettingsFromTheFixedHomeLocation(t *testing.T) {
	_, cfg, home := newTestHome(t, func(home string) {
		writeSettings(t, home, config.AppSettings{Language: "en", Theme: "dark"})
	})
	cfg.SetSvcDir(filepath.Join(home, "somewhere-else"))

	h := NewHome(cfg, testAboutJSON)

	if got := h.Settings.Get().Language; got != "en" {
		t.Errorf("Settings.Get().Language = %q, want %q (settings.json must be read from <homeDir>/.svc, not SvcDir)", got, "en")
	}
	if got := h.Settings.Get().Theme; got != "dark" {
		t.Errorf("Settings.Get().Theme = %q, want %q", got, "dark")
	}
	if h.Cfg != cfg {
		t.Error("Home.Cfg is not the Config passed in")
	}
	if h.ShimMgr == nil || h.PathMgr == nil || h.Downloader == nil || h.Proxy == nil || h.SettingsSvc == nil {
		t.Error("NewHome left a service nil")
	}
	if h.AppInfo.Version != "9.9.9-test" {
		t.Errorf("AppInfo.Version = %q, want %q", h.AppInfo.Version, "9.9.9-test")
	}
}

func TestNewHomeFallsBackOnCorruptAboutJSON(t *testing.T) {
	h, _, _ := newTestHome(t, nil)
	got := NewHome(h.Cfg, []byte("{not json"))
	if got.AppInfo.Version == "9.9.9-test" {
		t.Error("corrupt about.json must not leak the previous AppInfo")
	}
}

func TestBootstrapRejectsMissingRuntimeAndLowLevel(t *testing.T) {
	h, _, _ := newTestHome(t, nil)

	if _, err := h.Bootstrap(context.Background(), nil, LevelRead); err == nil {
		t.Error("Bootstrap(nil runtime) = nil error, want an error")
	}

	rt := &fakeRuntime{}
	for _, lvl := range []Level{LevelMeta, Level(-1)} {
		core, err := h.Bootstrap(context.Background(), rt, lvl)
		if err == nil {
			t.Errorf("Bootstrap(%s) = nil error, want an error", lvl)
		}
		if core != nil {
			t.Errorf("Bootstrap(%s) returned a Core alongside the error", lvl)
		}
	}
	if len(rt.events) != 0 {
		t.Errorf("a rejected Bootstrap emitted events: %v", rt.events)
	}
}

// TestBootstrapReadLevelAssemblesWithoutSetupSideEffects is the core guarantee
// for read-only shells: every service is wired, but the shell rc, the shims dir
// and the temp dir are left exactly as they were.
func TestBootstrapReadLevelAssemblesWithoutSetupSideEffects(t *testing.T) {
	h, cfg, home := newTestHome(t, nil)
	leftover := writeTmpLeftover(t, cfg)
	rt := &fakeRuntime{}

	core, err := h.Bootstrap(context.Background(), rt, LevelRead)
	if err != nil {
		t.Fatalf("Bootstrap(LevelRead): %v", err)
	}

	if core.Home != h {
		t.Error("Core.Home is not the Home that bootstrapped it")
	}
	if core.RT != wailsrt.Runtime(rt) {
		t.Error("Core.RT is not the runtime passed in")
	}
	if core.Registry == nil || core.Storage == nil || core.Updater == nil ||
		core.PkgMgr == nil || core.Installer == nil || core.Importer == nil {
		t.Error("LevelRead left a service nil")
	}

	if _, err := os.Stat(cfg.ShimsDir()); !os.IsNotExist(err) {
		t.Errorf("LevelRead created the shims dir %s (stat err=%v); EnsureSetup must stay write-only", cfg.ShimsDir(), err)
	}
	if _, err := os.Stat(cfg.RcFilePath()); !os.IsNotExist(err) {
		t.Errorf("LevelRead wrote the rc file %s (stat err=%v)", cfg.RcFilePath(), err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Errorf("LevelRead touched ~/.zshrc (stat err=%v)", err)
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Errorf("LevelRead removed %s (err=%v); the tmp cleanup is write-only", leftover, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.SvcDir(), "mirrors.json")); err != nil {
		t.Errorf("mirrors.json was not seeded in the SVC dir: %v", err)
	}
}

// TestBootstrapHonoursCustomInstallPath proves the SetSvcDir override happens
// before anything derives a path from it: InitMirrorsFile/InitVersionCacheDir
// must land in the custom install dir, not in the default ~/.svc.
func TestBootstrapHonoursCustomInstallPath(t *testing.T) {
	var custom string
	h, cfg, _ := newTestHome(t, func(home string) {
		custom = filepath.Join(home, "custom-install")
		if err := os.MkdirAll(custom, 0755); err != nil {
			t.Fatalf("mkdir custom: %v", err)
		}
		writeSettings(t, home, config.AppSettings{InstallPath: custom})
	})
	if custom == "" {
		t.Fatal("seed callback did not run")
	}

	if _, err := h.Bootstrap(context.Background(), &fakeRuntime{}, LevelRead); err != nil {
		t.Fatalf("Bootstrap(LevelRead): %v", err)
	}

	if got := cfg.SvcDir(); got != custom {
		t.Errorf("SvcDir() = %q, want the custom install path %q", got, custom)
	}
	if _, err := os.Stat(filepath.Join(custom, "mirrors.json")); err != nil {
		t.Errorf("mirrors.json missing from the custom install path (InitMirrorsFile must run after the SetSvcDir override): %v", err)
	}
	if _, err := os.Stat(filepath.Join(custom, "cache")); err != nil && !os.IsNotExist(err) {
		t.Errorf("unexpected stat error for the version cache dir: %v", err)
	}
}

// TestBootstrapInstallPathOverrideBeatsSettings covers --install-path: a
// shell-supplied override must win over the InstallPath saved in settings.json,
// otherwise the flag would silently do nothing for every user who has ever
// picked a custom install path in the GUI.
func TestBootstrapInstallPathOverrideBeatsSettings(t *testing.T) {
	var saved, override string
	h, cfg, _ := newTestHome(t, func(home string) {
		saved = filepath.Join(home, "saved-install")
		override = filepath.Join(home, "override-install")
		for _, dir := range []string{saved, override} {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
		}
		writeSettings(t, home, config.AppSettings{InstallPath: saved})
	})
	if saved == "" || override == "" {
		t.Fatal("the seed callback did not run")
	}
	h.InstallPathOverride = override

	if _, err := h.Bootstrap(context.Background(), &fakeRuntime{}, LevelRead); err != nil {
		t.Fatalf("Bootstrap(LevelRead): %v", err)
	}

	if got := cfg.SvcDir(); got != override {
		t.Errorf("SvcDir() = %q, want the override %q (settings.json says %q)", got, override, saved)
	}
	if _, err := os.Stat(filepath.Join(override, "mirrors.json")); err != nil {
		t.Errorf("mirrors.json missing from the override dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(saved, "mirrors.json")); err == nil {
		t.Error("the override must not leak into the saved install path")
	}

	// The override is per-process, never persisted: a second Home over a
	// settings.json that still names `saved` resolves to `saved`.
	if got := h.Settings.Get().InstallPath; got != saved {
		t.Errorf("settings.json InstallPath = %q, want it left alone at %q", got, saved)
	}
	other, _, _ := newTestHome(t, nil)
	if other.InstallPathOverride != "" {
		t.Error("a fresh Home must not inherit an override")
	}
}

// TestBootstrapWriteLevelRunsSetup covers the write-only steps: EnsureSetup
// (shims dir + rc file) and the temp-dir cleanup.
func TestBootstrapWriteLevelRunsSetup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on Windows EnsureSetup merges the shims dir into the CURRENT_USER registry PATH; a unit test must not touch the real registry")
	}
	h, cfg, home := newTestHome(t, nil)
	leftover := writeTmpLeftover(t, cfg)

	if _, err := h.Bootstrap(context.Background(), &fakeRuntime{}, LevelWrite); err != nil {
		t.Fatalf("Bootstrap(LevelWrite): %v", err)
	}

	if info, err := os.Stat(cfg.ShimsDir()); err != nil || !info.IsDir() {
		t.Errorf("LevelWrite did not create the shims dir %s: %v", cfg.ShimsDir(), err)
	}
	// EnsureSetup's rc side effect is the source line inside the shell rc
	// files, not .svc.rc itself (that one is written by updateRcFile once an
	// SDK has env vars). $SHELL is cleared in newTestHome, so ensurePathEntry
	// falls back to zsh + bash.
	rcPath := cfg.RcFilePath()
	zshrc, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		t.Fatalf("LevelWrite did not create ~/.zshrc: %v", err)
	}
	if want := `source "` + rcPath + `"`; !strings.Contains(string(zshrc), want) {
		t.Errorf("~/.zshrc has no source line for %s; got:\n%s", rcPath, zshrc)
	}
	if _, err := os.Stat(filepath.Join(cfg.SvcDir(), "env.sh.fish")); err != nil && !os.IsNotExist(err) {
		t.Errorf("unexpected stat error for env.sh.fish: %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("LevelWrite left %s behind (stat err=%v); the tmp cleanup must empty it", leftover, err)
	}
}

func TestCleanTmpDir(t *testing.T) {
	t.Run("removes leftovers but keeps the dir", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"a.zip", "b.tar.gz"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		nested := filepath.Join(dir, "extracted")
		if err := os.MkdirAll(filepath.Join(nested, "bin"), 0755); err != nil {
			t.Fatal(err)
		}

		cleanTmpDir(dir)

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir after clean: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("cleanTmpDir left %d entries behind", len(entries))
		}
	})

	t.Run("empty dir is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		cleanTmpDir(dir)
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("cleanTmpDir removed the dir itself: %v", err)
		}
	})

	t.Run("missing dir is a no-op", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "does-not-exist")
		cleanTmpDir(dir)
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("cleanTmpDir created a missing dir (stat err=%v)", err)
		}
	})
}
