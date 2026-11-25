package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"svc/internal/appcore"
	"svc/internal/config"
	"svc/internal/logger"
)

// TestMain claims the logger for a directory outside every t.TempDir().
//
// logger.Init is sync.Once-guarded, so the first caller wins and keeps its log
// file open for the whole test binary. Left alone that first caller is
// whichever test runs first, its log lands inside its own t.TempDir(), and on
// Windows the open handle makes the TempDir cleanup fail with a sharing
// violation. internal/appcore does the same thing for the same reason.
func TestMain(m *testing.M) {
	base, err := os.MkdirTemp("", "cli-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cli: create the temp log dir:", err)
		os.Exit(1)
	}
	logger.Init(base)
	code := m.Run()
	_ = os.RemoveAll(base)
	os.Exit(code)
}

// streams is the trio Execute writes to, so a test can assert the
// stdout-is-results / stderr-is-everything-else split.
type streams struct {
	stdin  *bytes.Buffer
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func newStreams() *streams {
	return &streams{stdin: &bytes.Buffer{}, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
}

// shippedCatalogs returns the real frontend catalogs, the same bytes main.go
// embeds. Tests use them rather than a copy so a wording change in the GUI is
// visible here instead of silently passing.
func shippedCatalogs(t *testing.T) map[string][]byte {
	t.Helper()
	catalogs := make(map[string][]byte, 2)
	for _, lang := range []string{"zh", "en"} {
		raw, err := os.ReadFile(filepath.Join(repoRootRel, "frontend/src/i18n", lang+".json"))
		if err != nil {
			t.Fatalf("read the %s catalog: %v", lang, err)
		}
		catalogs[lang] = raw
	}
	return catalogs
}

// tempHome builds an OpenHome replacement rooted in a temporary HOME, so a test
// can drive the real assembly path without ever touching ~/.svc. seed runs
// after the directories exist and receives the fake home, so a test can lay
// down a settings.json first.
//
// HOME and USERPROFILE are redirected because os.UserHomeDir -- and through it
// config.AvailableShells, which the shim setup walks -- reads them directly.
func tempHome(t *testing.T, seed func(home string)) func([]byte) (*appcore.Home, error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", "")
	if seed != nil {
		seed(home)
	}
	return func(aboutJSON []byte) (*appcore.Home, error) {
		cfg, err := config.NewConfig()
		if err != nil {
			return nil, err
		}
		cfg.SetHomeDir(home)
		cfg.SetSvcDir(filepath.Join(home, ".svc"))
		return appcore.NewHome(cfg, aboutJSON), nil
	}
}

// writeSettings seeds <home>/.svc/settings.json -- the fixed location the
// SettingsManager reads, independent of any custom install path.
func writeSettings(t *testing.T, home string, settings config.AppSettings) {
	t.Helper()
	dir := filepath.Join(home, ".svc")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), raw, 0644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
}
