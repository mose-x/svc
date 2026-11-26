package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// runArgs drives one invocation end to end against a temporary SVC home and
// returns the exit code with the streams it wrote to.
//
// env decides which output contract the run resolves to. Passing an Environment
// with nothing set makes the run a pipe, which is the strictest discipline and
// therefore what most assertions want; the mode must never depend on whether
// the test binary happens to be attached to a terminal.
func runArgs(t *testing.T, env *Environment, seed func(home string), args ...string) (int, *streams) {
	t.Helper()
	s := newStreams()
	code := Execute(ExecuteInput{
		Args:     args,
		Catalogs: shippedCatalogs(t),
		Stdin:    s.stdin,
		Stdout:   s.stdout,
		Stderr:   s.stderr,
		OpenHome: tempHome(t, seed),
		Env:      env,
	})
	return code, s
}

// pipedRun is an invocation whose stdout is a pipe: plain mode, no colour, and
// no notes.
func pipedRun(t *testing.T, seed func(home string), args ...string) (int, *streams) {
	t.Helper()
	return runArgs(t, &Environment{}, seed, args...)
}

// terminalRun is an invocation at a terminal with colour disabled, so notes are
// written but every assertion can stay on plain text.
func terminalRun(t *testing.T, seed func(home string), args ...string) (int, *streams) {
	t.Helper()
	return runArgs(t, &Environment{StdoutTTY: true, EnvNoColor: "1"}, seed, args...)
}

// outLines splits a run's result stream into the lines a reader sees. The
// trailing newline every writer adds is dropped, and an empty stream yields no
// lines rather than one empty one -- the difference matters for the commands
// whose success is silence.
func outLines(s *streams) []string {
	return linesOf(s.stdout.String())
}

// errLines is outLines for the diagnostic stream.
func errLines(s *streams) []string {
	return linesOf(s.stderr.String())
}

// linesOf splits rendered output into the lines a reader sees.
func linesOf(text string) []string {
	trimmed := strings.TrimSuffix(text, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// decodeJSON unmarshals a run's stdout, failing the test when the result stream
// is not exactly one JSON document. That failure is the one the output contract
// exists to prevent, so it is reported with the bytes that caused it.
func decodeJSON(t *testing.T, s *streams, into any) {
	t.Helper()
	if err := json.Unmarshal(s.stdout.Bytes(), into); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, s.stdout)
	}
}

// jsonKeysOf marshals a value and returns the top-level key set of the
// resulting object. It is how a test asserts that a command emitted a service
// struct verbatim rather than a CLI-only envelope around it.
func jsonKeysOf(t *testing.T, value any) []string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T: %v", value, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("%T did not marshal to a JSON object: %v", value, err)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// sharedHome binds ONE temporary home and returns a function that invokes the
// CLI against it as often as the test likes. Every other helper builds a fresh
// home per call, which is right for a single invocation and wrong for comparing
// two: the paths a command reports would differ between them for no reason
// other than that they ran twice.
func sharedHome(t *testing.T, seed func(home string)) func(args ...string) (int, *streams) {
	t.Helper()
	openHome := tempHome(t, seed)
	catalogs := shippedCatalogs(t)
	return func(args ...string) (int, *streams) {
		s := newStreams()
		code := Execute(ExecuteInput{
			Args:     args,
			Catalogs: catalogs,
			Stdin:    s.stdin,
			Stdout:   s.stdout,
			Stderr:   s.stderr,
			OpenHome: openHome,
			Env:      &Environment{},
		})
		return code, s
	}
}
