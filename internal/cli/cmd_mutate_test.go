package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"svc/internal/appcore"
	"svc/internal/apperr"
	"svc/internal/cli/i18n"
	"svc/internal/config"
	"svc/internal/sdk"
	"svc/internal/svclock"
	"svc/internal/wailsrt"
)

// The mutating commands are the first in the tree that change the machine, so
// their tests are split by how much of the system they need:
//
//   - pure helpers (labels, membership checks, the report channels) run
//     everywhere and touch nothing;
//   - the confirmation and the operation lock are driven through a LevelRead
//     bootstrap, which self-heals but never edits the shell rc or the registry,
//     so those run on all three CI platforms too;
//   - the command-level tests run a real switch or uninstall, which bootstraps at
//     LevelWrite. On Windows that means EnsureSetup merging the shims directory
//     into HKCU\Environment and ConfigureSdk writing GOROOT next to it, so those
//     tests skip there rather than edit the runner's environment -- the same
//     trade internal/appcore makes. The lock and confirmation logic they would
//     have covered is exercised by the LevelRead tests above, which do not skip.

// skipOnWindowsRegistryWrites names the reason a LevelWrite test cannot run on
// Windows.
func skipOnWindowsRegistryWrites(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("on Windows a LevelWrite bootstrap merges the shims dir into the CURRENT_USER registry PATH; a unit test must not touch the real registry")
	}
}

// goDisplayName is the label both shells put in a sentence about Go. Read from
// the registry rather than written out, so renaming it cannot make these tests
// pass against a wording the GUI no longer uses.
const goSdk = "go"

func goDisplayName() string { return sdk.SdkDisplayName(sdk.Golang) }

// seedGoVersion lays down the directory tree an extracted Go release has, so a
// switch or an uninstall has something real to point at. The extra go/ level is
// not decoration: GolangFetcher.GetBinDirs reports "go/bin" because the official
// archive extracts into a go/ subdirectory, and a flat bin/ would produce no
// shims at all -- the "the bin files were not handled" failure these tests exist
// to catch.
func seedGoVersion(home string, versions ...string) {
	for _, version := range versions {
		dir := filepath.Join(home, ".svc", goSdk, version, "go", "bin")
		if err := os.MkdirAll(dir, 0755); err != nil {
			panic(err)
		}
		for _, name := range []string{"go", "gofmt"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0755); err != nil {
				panic(err)
			}
		}
	}
}

// homeAt binds an OpenHome replacement to a home directory that already exists,
// so one test can run several invocations against the same on-disk state. Every
// other helper builds a fresh home per call, which cannot express "switch, then
// switch again to the version that is now active".
//
// PATH points at an empty directory and SHELL is cleared, exactly as tempHome
// does: a real `go` on PATH would make GetSdkStatus report a PATH copy, spawn a
// version probe, and turn these tests into a measurement of the machine they
// happen to run on.
func homeAt(t *testing.T, home string) func([]byte) (*appcore.Home, error) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", "")
	return func(aboutJSON []byte) (*appcore.Home, error) {
		cfg, err := newTestConfig(home)
		if err != nil {
			return nil, err
		}
		return appcore.NewHome(cfg, aboutJSON), nil
	}
}

// newTestConfig builds a Config rooted at home with the SVC dir underneath it,
// the layout both shells use.
func newTestConfig(home string) (*config.Config, error) {
	cfg, err := config.NewConfig()
	if err != nil {
		return nil, err
	}
	cfg.SetHomeDir(home)
	cfg.SetSvcDir(filepath.Join(home, ".svc"))
	return cfg, nil
}

// runHome invokes the CLI against an existing home as a pipe: plain mode, no
// colour, no notes, nobody at the keyboard.
func runHome(t *testing.T, home string, args ...string) (int, *streams) {
	t.Helper()
	return runAt(t, home, &Environment{}, "", args...)
}

// interactiveRunHome invokes the CLI against an existing home at a terminal whose
// stdin carries answers, which is the only way to test a declined confirmation
// without a pty.
func interactiveRunHome(t *testing.T, home, answers string, args ...string) (int, *streams) {
	t.Helper()
	return runAt(t, home, &Environment{StdinTTY: true, StdoutTTY: true, EnvNoColor: "1"}, answers, args...)
}

func runAt(t *testing.T, home string, env *Environment, answers string, args ...string) (int, *streams) {
	t.Helper()
	s := newStreams()
	s.stdin.WriteString(answers)
	code := Execute(ExecuteInput{
		Args:     args,
		Catalogs: shippedCatalogs(t),
		Stdin:    s.stdin,
		Stdout:   s.stdout,
		Stderr:   s.stderr,
		OpenHome: homeAt(t, home),
		Env:      env,
	})
	return code, s
}

// interactiveRun is runAt over a fresh home, for the single-invocation cases.
func interactiveRun(t *testing.T, answers string, seed func(home string), args ...string) (int, *streams) {
	t.Helper()
	s := newStreams()
	s.stdin.WriteString(answers)
	code := Execute(ExecuteInput{
		Args:     args,
		Catalogs: shippedCatalogs(t),
		Stdin:    s.stdin,
		Stdout:   s.stdout,
		Stderr:   s.stderr,
		OpenHome: tempHome(t, seed),
		Env:      &Environment{StdinTTY: true, StdoutTTY: true, EnvNoColor: "1"},
	})
	return code, s
}

// --- pure helpers -----------------------------------------------------------

func TestExternalManagerLabel(t *testing.T) {
	cases := []struct {
		manager string
		want    string
	}{
		{"nvm-rust", "nvm(nvm-rust)"},
		{"nvm", "nvm"},
		// An unknown manager keeps its machine name rather than rendering as
		// nothing: a manager added on the backend must stay readable in both
		// shells until the two label tables catch up.
		{"fnm", "fnm"},
		{"", ""},
	}
	for _, c := range cases {
		if got := ExternalManagerLabel(c.manager); got != c.want {
			t.Errorf("ExternalManagerLabel(%q) = %q, want %q", c.manager, got, c.want)
		}
	}
}

// externalManagerEntryRE reads `'key': 'label'` and `key: 'label'` pairs out of
// the TypeScript table.
var externalManagerEntryRE = regexp.MustCompile(`(?m)^\s*'?([A-Za-z0-9_-]+)'?:\s*'([^']*)',?\s*$`)

// TestExternalManagerLabelMatchesTheFrontend pins the Go table to the TypeScript
// one it ports, in both directions. A label renamed on either side alone would
// make the window and the terminal describe the same nvm installation
// differently, and nothing at runtime would notice.
func TestExternalManagerLabelMatchesTheFrontend(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRootRel, "frontend/src/constants/sdk.ts"))
	if err != nil {
		t.Fatalf("read sdk.ts: %v", err)
	}
	start := strings.Index(string(raw), "externalManagerNames")
	if start < 0 {
		t.Fatal("frontend/src/constants/sdk.ts no longer declares externalManagerNames")
	}
	body := string(raw)[start:]
	end := strings.Index(body, "}")
	if end < 0 {
		t.Fatal("the externalManagerNames table is not closed; the source shape changed")
	}
	frontend := map[string]string{}
	for _, match := range externalManagerEntryRE.FindAllStringSubmatch(body[:end], -1) {
		frontend[match[1]] = match[2]
	}
	if len(frontend) == 0 {
		t.Fatal("the regexp matched no entries; the source shape changed")
	}
	backend := sdk.ExternalManagerLabels()
	if len(frontend) != len(backend) {
		t.Errorf("the frontend lists %d managers %v, the backend lists %d %v",
			len(frontend), sortedKeys(frontend), len(backend), sortedKeys(backend))
	}
	for manager, label := range frontend {
		if got := ExternalManagerLabel(manager); got != label {
			t.Errorf("ExternalManagerLabel(%q) = %q, the frontend shows %q", manager, got, label)
		}
	}
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func TestRequireInstalled(t *testing.T) {
	installed := &sdk.SdkStatus{InstalledVersions: []string{"1.22.0", "1.23.0"}}
	cases := []struct {
		name    string
		status  *sdk.SdkStatus
		version string
		wantErr bool
	}{
		{"an installed version passes", installed, "1.22.0", false},
		{"the newest installed version passes", installed, "1.23.0", false},
		{"an uninstalled version is refused", installed, "1.24.0", true},
		{"a prefix of an installed version is refused", installed, "1.22", true},
		{"nothing installed refuses everything", &sdk.SdkStatus{}, "1.22.0", true},
		// A nil status is a caller bug, not a panic: the command reports the
		// version as missing rather than taking the process down.
		{"a missing status is refused", nil, "1.22.0", true},
		{"an empty version is refused", installed, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := requireInstalled(c.status, c.version)
			if c.wantErr && err == nil {
				t.Fatal("requireInstalled accepted a version that is not installed")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("requireInstalled refused an installed version: %v", err)
			}
			if err == nil {
				return
			}
			// The refusal has to be the service layer's own marker: that is what
			// makes it exit 3 and what makes the message the one the GUI shows.
			if code, ok := i18n.ErrorCode(err.Error()); !ok || code != string(apperr.VersionDirMissing) {
				t.Errorf("requireInstalled = %v (code %q), want the %q marker", err, code, apperr.VersionDirMissing)
			}
			if got := ExitCodeForError(err); got != ExitNotFound {
				t.Errorf("ExitCodeForError = %d, want %d", got, ExitNotFound)
			}
		})
	}
}

func TestMutationResultShape(t *testing.T) {
	t.Run("a plain success carries no warning key", func(t *testing.T) {
		result := MutationResult{Action: actionSwitch, SdkType: goSdk, Version: "1.22.0", Active: true}
		got := jsonKeysOf(t, result)
		want := []string{"action", "active", "sdkType", "version"}
		if !slices.Equal(got, want) {
			t.Errorf("MutationResult marshals to %v, want %v", got, want)
		}
	})
	t.Run("a warning is part of the contract", func(t *testing.T) {
		result := MutationResult{Action: actionUninstall, SdkType: goSdk, Version: "1.22.0", Warning: warningActiveVersionDeleted}
		got := jsonKeysOf(t, result)
		want := []string{"action", "active", "sdkType", "version", "warning"}
		if !slices.Equal(got, want) {
			t.Errorf("MutationResult marshals to %v, want %v", got, want)
		}
		var decoded map[string]any
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["active"] != false {
			t.Errorf("active = %v, want false: an uninstalled version is never the active one", decoded["active"])
		}
	})
	t.Run("the action and warning tokens are the documented literals", func(t *testing.T) {
		actions := []string{actionInstall, actionReinstall, actionSwitch, actionUninstall}
		if !slices.Equal(actions, []string{"install", "reinstall", "switch", "uninstall"}) {
			t.Errorf("the action tokens changed: %v -- they are a --json contract", actions)
		}
		warnings := []string{warningAlreadyActive, warningActiveVersionDeleted}
		if !slices.Equal(warnings, []string{"already-active", "active-version-deleted"}) {
			t.Errorf("the warning tokens changed: %v -- they are a --json contract", warnings)
		}
	})
}

// TestReportMutationRoutesEachChannel pins the three-way split: the document to
// stdout in JSON mode and nothing else, the outcome sentences to stdout in the
// text modes, and the advice to stderr where only an interactive terminal sees
// it. A hint that leaked into stdout would be parsed by `$(svc ...)` callers.
func TestReportMutationRoutesEachChannel(t *testing.T) {
	report := MutationReport{
		Result:  MutationResult{Action: actionSwitch, SdkType: goSdk, Version: "1.22.0", Active: true},
		Summary: "switched",
		Warning: "watch out",
		Hint:    "reopen the terminal",
	}
	cases := []struct {
		name       string
		flags      globalFlags
		stdoutTTY  bool
		json       bool
		wantStdout []string
		wantStderr []string
	}{
		{"a pipe gets the outcome only", globalFlags{}, false, false, []string{"switched", "watch out"}, nil},
		{"a terminal also gets the hint", globalFlags{}, true, false, []string{"switched", "watch out"}, []string{"reopen the terminal"}},
		{"--json gets one document", globalFlags{json: true}, true, true, nil, nil},
		{"--quiet keeps the outcome and drops the hint", globalFlags{quiet: true}, true, false, []string{"switched", "watch out"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStreams()
			r := newRunner(t, s, c.flags, tempHome(t, nil))
			r.in.Env.StdoutTTY = c.stdoutTTY
			if err := r.resolveOptions(); err != nil {
				t.Fatalf("resolveOptions: %v", err)
			}
			if err := r.reportMutation(report); err != nil {
				t.Fatalf("reportMutation: %v", err)
			}
			if c.json {
				var decoded MutationResult
				decodeJSON(t, s, &decoded)
				if decoded != report.Result {
					t.Errorf("the document is %+v, want %+v", decoded, report.Result)
				}
				return
			}
			if got := outLines(s); !slices.Equal(got, c.wantStdout) {
				t.Errorf("stdout = %q, want %q", got, c.wantStdout)
			}
			if got := errLines(s); !slices.Equal(got, c.wantStderr) {
				t.Errorf("stderr = %q, want %q", got, c.wantStderr)
			}
		})
	}
}

// TestReportMutationOfANoOpStaysQuiet pins the empty-summary case: a switch that
// changed nothing prints its warning and no blank line, so a script never has to
// filter an empty record out of the result.
func TestReportMutationOfANoOpStaysQuiet(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}
	report := MutationReport{
		Result: MutationResult{
			Action: actionSwitch, SdkType: goSdk, Version: "1.22.0",
			Active: true, Warning: warningAlreadyActive,
		},
		Warning: "already active",
	}
	if err := r.reportMutation(report); err != nil {
		t.Fatalf("reportMutation: %v", err)
	}
	if got := outLines(s); !slices.Equal(got, []string{"already active"}) {
		t.Errorf("stdout = %q, want exactly the warning", got)
	}
}

// TestConfirmMutation covers every answer the prompt can give: the four states of
// "who is listening" x "what did they say", plus --yes, which removes the
// question entirely.
func TestConfirmMutation(t *testing.T) {
	const explanation = "this rewrites the world"
	cases := []struct {
		name     string
		yes      bool
		stdinTTY bool
		answers  string
		wantErr  error
		wantAsk  bool
		wantDesc bool
	}{
		{"--yes never asks", true, true, "", nil, false, false},
		{"--yes off a pipe never asks", true, false, "", nil, false, false},
		{"a yes proceeds", false, true, "y\n", nil, true, true},
		{"a full yes proceeds", false, true, "yes\n", nil, true, true},
		{"a Chinese yes proceeds", false, true, "确认\n", nil, true, true},
		{"a no is a cancellation", false, true, "n\n", ErrConfirmationDeclined, true, true},
		{"a Chinese no is a cancellation", false, true, "取消\n", ErrConfirmationDeclined, true, true},
		{"an empty answer takes the no default", false, true, "\n", ErrConfirmationDeclined, true, true},
		{"EOF is an abort, not a consent", false, true, "", ErrConfirmationAborted, true, true},
		{"a pipe without --yes refuses to guess", false, false, "", ErrConfirmationRequired, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStreams()
			s.stdin.WriteString(c.answers)
			r := newRunner(t, s, globalFlags{yes: c.yes}, tempHome(t, nil))
			r.in.Env.StdinTTY = c.stdinTTY
			r.in.Env.StdoutTTY = true
			if err := r.resolveOptions(); err != nil {
				t.Fatalf("resolveOptions: %v", err)
			}

			err := r.confirmMutation("proceed?", explanation)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("confirmMutation = %v, want %v", err, c.wantErr)
			}
			asked := strings.Contains(s.stderr.String(), "proceed?")
			if asked != c.wantAsk {
				t.Errorf("the question was asked = %v, want %v (stderr %q)", asked, c.wantAsk, s.stderr)
			}
			// The explanation is part of the decision, so it is shown only when
			// there is a decision to make: with --yes it would be noise.
			shown := strings.Contains(s.stderr.String(), explanation)
			if shown != c.wantDesc {
				t.Errorf("the explanation was shown = %v, want %v (stderr %q)", shown, c.wantDesc, s.stderr)
			}
			if err != nil && ExitCodeForError(err) != ExitCodeForError(c.wantErr) {
				t.Errorf("ExitCodeForError(%v) = %d, want %d", err, ExitCodeForError(err), ExitCodeForError(c.wantErr))
			}
		})
	}
}

// TestConfirmationDeclinedExitsAsACancellation pins the exit code on its own: a
// declined confirmation is what the user asked for, so it must not be exit 1, and
// it must not be exit 0 either -- a script cannot tell "nothing happened" from
// "it happened" any other way.
func TestConfirmationDeclinedExitsAsACancellation(t *testing.T) {
	if got := ExitCodeForError(ErrConfirmationDeclined); got != ExitCanceled {
		t.Errorf("ExitCodeForError(ErrConfirmationDeclined) = %d, want %d", got, ExitCanceled)
	}
	wrapped := errors.Join(errors.New("uninstall go"), ErrConfirmationDeclined)
	if got := ExitCodeForError(wrapped); got != ExitCanceled {
		t.Errorf("a wrapped decline = %d, want %d", got, ExitCanceled)
	}
	if errors.Is(ErrConfirmationDeclined, ErrConfirmationAborted) || errors.Is(ErrConfirmationDeclined, ErrConfirmationRequired) {
		t.Error("ErrConfirmationDeclined is aliased to one of the other confirmation sentinels")
	}
}

// --- the operation lock -----------------------------------------------------

// lockCore bootstraps at LevelRead, which is all withOpLock needs (it only reads
// the SVC dir) and is safe on every platform.
func lockCore(t *testing.T, s *streams, flags globalFlags, seed func(home string)) (*runner, *appcore.Core) {
	t.Helper()
	r := newRunner(t, s, flags, tempHome(t, seed))
	r.in.Env.StdoutTTY = true
	core, err := r.bootstrap(appcore.LevelRead)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(r.shutdown)
	return r, core
}

func TestWithOpLockRunsTheOperationAndReleases(t *testing.T) {
	s := newStreams()
	r, core := lockCore(t, s, globalFlags{}, nil)
	ran := false
	if err := r.withOpLock(core, func() error { ran = true; return nil }); err != nil {
		t.Fatalf("withOpLock: %v", err)
	}
	if !ran {
		t.Fatal("the operation never ran")
	}
	// The lock must be free again, or the next svc process would block behind one
	// that already finished.
	lock, err := svclock.TryAcquire(core.Home.Cfg.SvcDir())
	if err != nil {
		t.Fatalf("the lock was still held after withOpLock returned: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	// An uncontended run says nothing: the "waiting" note exists to explain a
	// pause, and printing it when there was no pause is a lie.
	if s.stderr.Len() != 0 {
		t.Errorf("an uncontended run wrote %q to stderr", s.stderr)
	}
}

func TestWithOpLockPropagatesTheFailureAndReleases(t *testing.T) {
	s := newStreams()
	r, core := lockCore(t, s, globalFlags{}, nil)
	boom := errors.New("the download was corrupt")
	err := r.withOpLock(core, func() error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("withOpLock = %v, want the operation's own error", err)
	}
	lock, acquireErr := svclock.TryAcquire(core.Home.Cfg.SvcDir())
	if acquireErr != nil {
		t.Fatalf("a failed operation left the lock held: %v", acquireErr)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

// TestWithOpLockWaitsForAContendedLock is the default behaviour the design
// promises: a second svc process pauses, says why, and then runs. The GUI holds
// the lock for the length of an install, so failing instead of waiting would make
// `svc install` unusable whenever the window happens to be open.
func TestWithOpLockWaitsForAContendedLock(t *testing.T) {
	s := newStreams()
	r, core := lockCore(t, s, globalFlags{}, nil)

	held, err := svclock.Acquire(core.Home.Cfg.SvcDir(), nil)
	if err != nil {
		t.Fatalf("take the lock the way another svc process would: %v", err)
	}

	ran := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.withOpLock(core, func() error { close(ran); return nil })
	}()

	select {
	case <-ran:
		t.Fatal("the operation ran while another holder had the lock")
	case <-time.After(100 * time.Millisecond):
	}

	if err := held.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("withOpLock: %v", err)
	}
	select {
	case <-ran:
	default:
		t.Fatal("the operation never ran after the lock was released")
	}

	// Read only after the goroutine has joined: the stream buffers are not
	// synchronised, and a concurrent read is a race the detector would catch.
	if want := catalogWord(t, "zh", "cli.waitingForLock", nil); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("the wait was not explained; stderr = %q, want it to contain %q", s.stderr, want)
	}
}

// TestWithOpLockNoWaitRefusesAContendedLock is the --no-wait half: a caller that
// cannot block gets a distinct exit code instead of a hang, and the operation
// does not run.
func TestWithOpLockNoWaitRefusesAContendedLock(t *testing.T) {
	s := newStreams()
	r, core := lockCore(t, s, globalFlags{noWait: true}, nil)

	held, err := svclock.Acquire(core.Home.Cfg.SvcDir(), nil)
	if err != nil {
		t.Fatalf("take the lock: %v", err)
	}
	defer held.Release()

	ran := false
	err = r.withOpLock(core, func() error { ran = true; return nil })
	if ran {
		t.Error("the operation ran even though --no-wait was refused")
	}
	if err == nil {
		t.Fatal("--no-wait against a held lock = nil, want a refusal")
	}
	if code, ok := i18n.ErrorCode(err.Error()); !ok || code != string(apperr.OperationInProgress) {
		t.Errorf("the refusal is %v (code %q), want the %q marker", err, code, apperr.OperationInProgress)
	}
	if got := ExitCodeForError(err); got != ExitBusy {
		t.Errorf("ExitCodeForError = %d, want %d", got, ExitBusy)
	}
	// --no-wait promises not to wait, so it must not claim to be waiting either.
	if s.stderr.Len() != 0 {
		t.Errorf("--no-wait wrote %q to stderr", s.stderr)
	}
}

// TestWithOpLockNoWaitRunsWhenTheLockIsFree pins the other half of --no-wait: it
// changes nothing at all when there is no contention.
func TestWithOpLockNoWaitRunsWhenTheLockIsFree(t *testing.T) {
	s := newStreams()
	r, core := lockCore(t, s, globalFlags{noWait: true}, nil)
	ran := false
	if err := r.withOpLock(core, func() error { ran = true; return nil }); err != nil {
		t.Fatalf("withOpLock: %v", err)
	}
	if !ran {
		t.Error("the operation never ran")
	}
}

// TestWithOpLockReportsABrokenLock covers the third outcome of TryAcquire: not
// "held" and not "granted" but "cannot be opened at all". That must surface as the
// failure it is rather than as contention, because exit 7 tells the user to retry
// and retrying will never help.
func TestWithOpLockReportsABrokenLock(t *testing.T) {
	s := newStreams()
	// A directory where the lock FILE should be: the SVC dir itself has to stay
	// usable, because bootstrapping over a broken one fails before withOpLock is
	// ever reached and the test would then prove nothing about the lock.
	r, core := lockCore(t, s, globalFlags{noWait: true}, func(home string) {
		if err := os.MkdirAll(filepath.Join(home, ".svc", svclock.LockFileName), 0755); err != nil {
			t.Fatal(err)
		}
	})
	err := r.withOpLock(core, func() error { return nil })
	if err == nil {
		t.Fatal("withOpLock over an unopenable lock = nil, want a failure")
	}
	if svclock.IsBusy(err) {
		t.Errorf("an unopenable lock was reported as contention: %v", err)
	}
	if got := ExitCodeForError(err); got != ExitError {
		t.Errorf("ExitCodeForError = %d, want %d", got, ExitError)
	}
}

func TestSdkThenNothingCompletesOnlyTheSdk(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}

	first, directive := r.sdkThenNothing(nil, nil, "go")
	if len(first) == 0 {
		t.Error("the first argument offered no SDK identifiers")
	}
	if !slices.Contains(first, goSdk) {
		t.Errorf("the candidates %v do not include go", first)
	}
	// NoFileComp rather than 0: offering the shell the filesystem after `svc
	// install go ` would complete to filenames, which is never the answer.
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("the first argument's directive = %v, want NoFileComp", directive)
	}

	// A version can only come from a remote list, and a completion function that
	// reached for the network would hang the shell on every press of Tab.
	second, directive := r.sdkThenNothing(nil, []string{goSdk}, "1.2")
	if len(second) != 0 {
		t.Errorf("the version argument offered %v, want nothing", second)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("the version argument's directive = %v, want NoFileComp", directive)
	}
}

// --- command level ----------------------------------------------------------

func TestSwitchCommandActivatesAnInstalledVersion(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	code, s := pipedRun(t, func(home string) { seedGoVersion(home, "1.22.0", "1.23.0") },
		"switch", goSdk, "1.23.0", "--yes")
	if code != ExitOK {
		t.Fatalf("svc switch = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	want := catalogWord(t, "zh", "detail.switchSuccess", map[string]string{"version": "1.23.0"})
	if got := outLines(s); !slices.Equal(got, []string{want}) {
		t.Errorf("stdout = %q, want only %q", got, want)
	}
	// The reopen hint is advice, so a pipe must not carry it.
	if s.stderr.Len() != 0 {
		t.Errorf("a piped switch wrote %q to stderr", s.stderr)
	}
}

func TestSwitchCommandWritesTheActiveVersionAndTheShims(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	code, s := pipedRun(t, func(h string) {
		home = h
		seedGoVersion(h, "1.22.0", "1.23.0")
	}, "switch", goSdk, "1.23.0", "--yes")
	if code != ExitOK {
		t.Fatalf("svc switch = exit %d; stderr:\n%s", code, s.stderr)
	}

	if got := readActiveVersion(t, home, goSdk); got != "1.23.0" {
		t.Errorf("config.json records go = %q, want 1.23.0", got)
	}

	shims, err := os.ReadFile(filepath.Join(home, ".svc", "shims.json"))
	if err != nil {
		t.Fatalf("read shims.json: %v", err)
	}
	var shimCfg struct {
		Commands map[string]string `json:"commands"`
	}
	if err := json.Unmarshal(shims, &shimCfg); err != nil {
		t.Fatalf("shims.json is not the documented shape: %v\n%s", err, shims)
	}
	// Both binaries in the version's go/bin must be routable, which is only true
	// when the bin dir the fetcher declares is the one that got scanned.
	for _, command := range []string{"go", "gofmt"} {
		if shimCfg.Commands[command] != goSdk {
			t.Errorf("shims.json routes %q to %q, want the go SDK type", command, shimCfg.Commands[command])
		}
	}
	for _, name := range []string{"go", "gofmt", "svc-shim"} {
		if _, err := os.Stat(filepath.Join(home, ".svc", "shims", name)); err != nil {
			t.Errorf("the shim file %s is missing: %v", name, err)
		}
	}
}

func TestSwitchCommandReportsTheResultInJSON(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	code, s := pipedRun(t, func(home string) { seedGoVersion(home, "1.22.0") },
		"--json", "switch", goSdk, "1.22.0", "--yes")
	if code != ExitOK {
		t.Fatalf("svc --json switch = exit %d; stderr:\n%s", code, s.stderr)
	}
	var result MutationResult
	decodeJSON(t, s, &result)
	want := MutationResult{Action: actionSwitch, SdkType: goSdk, Version: "1.22.0", Active: true}
	if result != want {
		t.Errorf("the document is %+v, want %+v", result, want)
	}
}

func TestSwitchCommandHintsToReopenTheTerminal(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	code, s := terminalRun(t, func(home string) { seedGoVersion(home, "1.22.0") },
		"switch", goSdk, "1.22.0", "--yes")
	if code != ExitOK {
		t.Fatalf("svc switch = exit %d; stderr:\n%s", code, s.stderr)
	}
	want := catalogWord(t, "zh", "detail.reopenTerminalHint", nil)
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want the reopen hint %q", s.stderr, want)
	}
	// The hint belongs to the human, never to the result a script captures.
	if strings.Contains(s.stdout.String(), want) {
		t.Errorf("the reopen hint leaked into stdout: %q", s.stdout)
	}
}

func TestSwitchCommandOnTheActiveVersionChangesNothing(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	seed := func(h string) {
		home = h
		seedGoVersion(h, "1.22.0")
	}
	if code, s := pipedRun(t, seed, "switch", goSdk, "1.22.0", "--yes"); code != ExitOK {
		t.Fatalf("the switch that makes 1.22.0 active = exit %d; stderr:\n%s", code, s.stderr)
	}
	configPath := filepath.Join(home, ".svc", "config.json")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}

	code, s := runHome(t, home, "--json", "switch", goSdk, "1.22.0", "--yes")
	if code != ExitOK {
		t.Fatalf("switching to the active version = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result MutationResult
	decodeJSON(t, s, &result)
	if result.Warning != warningAlreadyActive {
		t.Errorf("warning = %q, want %q", result.Warning, warningAlreadyActive)
	}
	if !result.Active {
		t.Error("active = false; the version really is the active one")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.json again: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("a no-op switch rewrote config.json:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestSwitchCommandSaysSoWhenTheVersionIsAlreadyActive(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	seed := func(h string) {
		home = h
		seedGoVersion(h, "1.22.0")
	}
	if code, s := pipedRun(t, seed, "switch", goSdk, "1.22.0", "--yes"); code != ExitOK {
		t.Fatalf("the first switch = exit %d; stderr:\n%s", code, s.stderr)
	}
	// The GUI is silent about this case because its handler returns before a modal
	// opens. A terminal cannot afford silence: exit 0 with nothing printed is
	// indistinguishable from a command that never ran.
	code, s := runHome(t, home, "switch", goSdk, "1.22.0", "--yes")
	if code != ExitOK {
		t.Fatalf("the second switch = exit %d, want %d", code, ExitOK)
	}
	want := catalogWord(t, "zh", "cli.alreadyActive", map[string]string{"version": "1.22.0"})
	if got := outLines(s); !slices.Equal(got, []string{want}) {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestSwitchCommandRefusesAnUninstalledVersion(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	code, s := pipedRun(t, func(home string) { seedGoVersion(home, "1.22.0") },
		"switch", goSdk, "1.99.0", "--yes")
	if code != ExitNotFound {
		t.Fatalf("svc switch to a missing version = exit %d, want %d; stderr:\n%s", code, ExitNotFound, s.stderr)
	}
	want := catalogWord(t, "zh", "errors.version-dir-missing", map[string]string{"version": "1.99.0"})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want it to name the missing version (%q)", s.stderr, want)
	}
}

// TestSwitchCommandRefusesAMissingVersionBeforeAsking pins the order: the
// membership check runs before the confirmation, so a user is never asked to
// approve an operation that is about to fail.
func TestSwitchCommandRefusesAMissingVersionBeforeAsking(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	code, s := interactiveRun(t, "y\n", func(home string) { seedGoVersion(home, "1.22.0") },
		"switch", goSdk, "1.99.0")
	if code != ExitNotFound {
		t.Fatalf("svc switch to a missing version = exit %d, want %d; stderr:\n%s", code, ExitNotFound, s.stderr)
	}
	if strings.Contains(s.stderr.String(), catalogWord(t, "zh", "detail.switchConfirm", map[string]string{
		"sdk": goDisplayName(), "version": "1.99.0",
	})) {
		t.Errorf("the user was asked to confirm a switch that could not happen:\n%s", s.stderr)
	}
}

func TestSwitchCommandWithoutYesRefusesAPipe(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	code, s := pipedRun(t, func(h string) {
		home = h
		seedGoVersion(h, "1.22.0")
	}, "switch", goSdk, "1.22.0")
	if code != ExitUsage {
		t.Fatalf("svc switch without --yes off a terminal = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	// Nothing may have been written: the refusal has to come before the work.
	if _, err := os.Stat(filepath.Join(home, ".svc", "shims.json")); !os.IsNotExist(err) {
		t.Errorf("a refused switch left shims.json behind (stat err = %v)", err)
	}
}

func TestSwitchCommandDeclinedAtThePromptChangesNothing(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	code, s := interactiveRun(t, "n\n", func(h string) {
		home = h
		seedGoVersion(h, "1.22.0")
	}, "switch", goSdk, "1.22.0")
	if code != ExitCanceled {
		t.Fatalf("a declined switch = exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
	}
	want := catalogWord(t, "zh", "detail.switchConfirm", map[string]string{
		"sdk": goDisplayName(), "version": "1.22.0",
	})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want the GUI's own confirmation wording %q", s.stderr, want)
	}
	// config.json itself always exists -- config.load() creates it on first read,
	// before any command runs -- so the assertion is about what is IN it: no
	// active version, and no shims.json, which only ConfigureSdk writes.
	if active := readActiveVersion(t, home, goSdk); active != "" {
		t.Errorf("a declined switch activated %q", active)
	}
	if _, err := os.Stat(filepath.Join(home, ".svc", "shims.json")); !os.IsNotExist(err) {
		t.Errorf("a declined switch wrote shims.json (stat err = %v)", err)
	}
}

// readActiveVersion reads config.json the way the services write it and returns
// the active version of one SDK, or "" when there is none.
func readActiveVersion(t *testing.T, home, sdkType string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".svc", "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read config.json: %v", err)
	}
	var decoded struct {
		ActiveVersions map[string]string `json:"activeVersions"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("config.json is not the documented shape: %v\n%s", err, raw)
	}
	return decoded.ActiveVersions[sdkType]
}

func TestUninstallCommandRemovesAnInactiveVersion(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	code, s := pipedRun(t, func(h string) {
		home = h
		seedGoVersion(h, "1.22.0", "1.23.0")
	}, "uninstall", goSdk, "1.22.0", "--yes")
	if code != ExitOK {
		t.Fatalf("svc uninstall = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".svc", goSdk, "1.22.0")); !os.IsNotExist(err) {
		t.Errorf("the version directory survived the uninstall (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".svc", goSdk, "1.23.0", "go", "bin", "go")); err != nil {
		t.Errorf("the other version was damaged: %v", err)
	}
	want := catalogWord(t, "zh", "detail.uninstallSuccess", map[string]string{"version": "1.22.0"})
	if got := outLines(s); !slices.Equal(got, []string{want}) {
		t.Errorf("stdout = %q, want only %q", got, want)
	}
}

// TestUninstallCommandOfTheActiveVersionIsASuccessWithAWarning is the case the
// storage sentinel exists for. The GUI shows a warning toast and refreshes; the
// CLI has to exit 0 -- the deletion was exactly what was asked for -- while still
// telling the caller that the SDK now has no active version.
func TestUninstallCommandOfTheActiveVersionIsASuccessWithAWarning(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	seed := func(h string) {
		home = h
		seedGoVersion(h, "1.22.0", "1.23.0")
	}
	if code, s := pipedRun(t, seed, "switch", goSdk, "1.23.0", "--yes"); code != ExitOK {
		t.Fatalf("the switch that makes the case interesting = exit %d; stderr:\n%s", code, s.stderr)
	}

	code, s := runHome(t, home, "--json", "uninstall", goSdk, "1.23.0", "--yes")
	if code != ExitOK {
		t.Fatalf("uninstalling the active version = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result MutationResult
	decodeJSON(t, s, &result)
	want := MutationResult{
		Action: actionUninstall, SdkType: goSdk, Version: "1.23.0",
		Warning: warningActiveVersionDeleted,
	}
	if result != want {
		t.Errorf("the document is %+v, want %+v", result, want)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".svc", "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if strings.Contains(string(raw), "1.23.0") {
		t.Errorf("config.json still names the deleted version:\n%s", raw)
	}
}

func TestUninstallCommandWarnsInTheTextModesToo(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	seed := func(h string) {
		home = h
		seedGoVersion(h, "1.22.0", "1.23.0")
	}
	if code, s := pipedRun(t, seed, "switch", goSdk, "1.23.0", "--yes"); code != ExitOK {
		t.Fatalf("switch = exit %d; stderr:\n%s", code, s.stderr)
	}
	code, s := runHome(t, home, "uninstall", goSdk, "1.23.0", "--yes")
	if code != ExitOK {
		t.Fatalf("uninstall = exit %d; stderr:\n%s", code, s.stderr)
	}
	want := []string{
		catalogWord(t, "zh", "detail.uninstallSuccess", map[string]string{"version": "1.23.0"}),
		catalogWord(t, "zh", "detail.activeVersionDeleted", map[string]string{"sdk": goDisplayName()}),
	}
	if got := outLines(s); !slices.Equal(got, want) {
		t.Errorf("stdout = %q, want %q -- the warning has to survive a pipe", got, want)
	}
}

func TestUninstallCommandRefusesAnUninstalledVersion(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	code, s := pipedRun(t, func(h string) {
		home = h
		seedGoVersion(h, "1.22.0")
	}, "uninstall", goSdk, "1.99.0", "--yes")
	if code != ExitNotFound {
		t.Fatalf("uninstalling a missing version = exit %d, want %d; stderr:\n%s", code, ExitNotFound, s.stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".svc", goSdk, "1.22.0", "go", "bin", "go")); err != nil {
		t.Errorf("the installed version was touched: %v", err)
	}
}

// TestUninstallCommandAsksHarderAboutTheActiveVersion pins the GUI's two
// wordings: deleting the version in use is a different question from deleting a
// spare one, and flattening them would drop the only warning the user gets.
func TestUninstallCommandAsksHarderAboutTheActiveVersion(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	seed := func(h string) {
		home = h
		seedGoVersion(h, "1.22.0", "1.23.0")
	}
	if code, s := pipedRun(t, seed, "switch", goSdk, "1.23.0", "--yes"); code != ExitOK {
		t.Fatalf("switch = exit %d; stderr:\n%s", code, s.stderr)
	}

	cases := []struct {
		version string
		key     string
	}{
		{"1.23.0", "detail.uninstallActiveConfirm"},
		{"1.22.0", "detail.uninstallConfirm"},
	}
	for _, c := range cases {
		// Decline, so the question is asked and nothing is deleted.
		code, s := interactiveRunHome(t, home, "n\n", "uninstall", goSdk, c.version)
		if code != ExitCanceled {
			t.Fatalf("declining the uninstall of %s = exit %d, want %d; stderr:\n%s",
				c.version, code, ExitCanceled, s.stderr)
		}
		want := catalogWord(t, "zh", c.key, map[string]string{"sdk": goDisplayName(), "version": c.version})
		if !strings.Contains(s.stderr.String(), want) {
			t.Errorf("uninstalling %s asked %q, want %q (%s)", c.version, s.stderr, want, c.key)
		}
		if _, err := os.Stat(filepath.Join(home, ".svc", goSdk, c.version)); err != nil {
			t.Errorf("a declined uninstall deleted %s: %v", c.version, err)
		}
	}
}

func TestReinstallCommandRefusesAnUninstalledVersion(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	code, s := pipedRun(t, func(h string) {
		home = h
		seedGoVersion(h, "1.22.0")
	}, "reinstall", goSdk, "1.99.0", "--yes")
	if code != ExitNotFound {
		t.Fatalf("reinstalling a version that is not installed = exit %d, want %d; stderr:\n%s",
			code, ExitNotFound, s.stderr)
	}
	// The refusal has to come before any download: the temp dir is the only place
	// InstallSdk would have written to, and it must still be empty.
	entries, err := os.ReadDir(filepath.Join(home, ".svc", "tmp"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read the temp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused reinstall left %d files in the temp dir", len(entries))
	}
}

func TestInstallCommandWithoutYesRefusesAPipe(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	// No version is seeded and none may appear: the run has to stop at the
	// confirmation, long before InstallSdk could reach the network.
	code, s := pipedRun(t, func(h string) { home = h }, "install", goSdk, "1.22.0")
	if code != ExitUsage {
		t.Fatalf("svc install without --yes off a terminal = exit %d, want %d; stderr:\n%s",
			code, ExitUsage, s.stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".svc", goSdk, "1.22.0")); !os.IsNotExist(err) {
		t.Errorf("a refused install created the version directory (stat err = %v)", err)
	}
}

// TestInstallCommandAsksWithTheGuiWording covers the interactive half of install
// without downloading anything: the question is declined, so the run stops at the
// prompt. It pins the catalog keys the GUI's modal uses, which is the "与 UI 一致"
// half of the contract that a successful-install test cannot reach offline.
func TestInstallCommandAsksWithTheGuiWording(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	code, s := interactiveRun(t, "n\n", nil, "install", goSdk, "1.22.0")
	if code != ExitCanceled {
		t.Fatalf("a declined install = exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
	}
	stderr := s.stderr.String()
	for key, params := range map[string]map[string]string{
		"detail.confirmInstallSdk":  {"sdk": goDisplayName(), "version": "1.22.0"},
		"detail.confirmInstallDesc": {"version": "1.22.0"},
	} {
		if want := catalogWord(t, "zh", key, params); !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want it to carry %q (%s)", stderr, want, key)
		}
	}
}

func TestReinstallCommandAsksWithTheReinstallWording(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	code, s := interactiveRun(t, "n\n", func(home string) { seedGoVersion(home, "1.22.0") },
		"reinstall", goSdk, "1.22.0")
	if code != ExitCanceled {
		t.Fatalf("a declined reinstall = exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
	}
	stderr := s.stderr.String()
	if want := catalogWord(t, "zh", "detail.reinstallConfirm", map[string]string{"version": "1.22.0"}); !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if want := catalogWord(t, "zh", "detail.reinstallConfirmDesc", nil); !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	// The install wording must not appear: reinstall has its own, and showing both
	// would ask two questions for one operation.
	if strings.Contains(stderr, catalogWord(t, "zh", "detail.confirmInstallSdk", map[string]string{
		"sdk": goDisplayName(), "version": "1.22.0",
	})) {
		t.Errorf("reinstall asked the install question:\n%s", stderr)
	}
}

func TestInstallCommandRefusesAnUnknownSdk(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	code, s := pipedRun(t, nil, "install", "klingon", "1.0.0", "--yes")
	if code != ExitNotFound {
		t.Fatalf("svc install of an unknown SDK = exit %d, want %d; stderr:\n%s", code, ExitNotFound, s.stderr)
	}
	want := catalogWord(t, "zh", "errors.unknown-sdk-type", map[string]string{"sdk": "klingon"})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
}

func TestMutatingCommandsRejectTheWrongArity(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	cases := [][]string{
		{"install", goSdk},
		{"install"},
		{"reinstall", goSdk},
		{"switch", goSdk},
		{"uninstall", goSdk, "1.22.0", "extra"},
	}
	for _, args := range cases {
		code, s := pipedRun(t, nil, append(args, "--yes")...)
		if code != ExitUsage {
			t.Errorf("svc %s = exit %d, want %d; stderr:\n%s", strings.Join(args, " "), code, ExitUsage, s.stderr)
		}
		// The usage printed has to be the subcommand's, or the user is shown the
		// root's flags and no hint about the argument they got wrong.
		if !strings.Contains(s.stderr.String(), "Usage:") {
			t.Errorf("svc %s printed no usage:\n%s", strings.Join(args, " "), s.stderr)
		}
	}
}

func TestSwitchCommandIsAlsoUseAndUninstallIsAlsoRm(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	seed := func(h string) {
		home = h
		seedGoVersion(h, "1.22.0", "1.23.0")
	}
	if code, s := pipedRun(t, seed, "use", goSdk, "1.23.0", "--yes"); code != ExitOK {
		t.Fatalf("svc use = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	code, s := runHome(t, home, "--json", "rm", goSdk, "1.22.0", "--yes")
	if code != ExitOK {
		t.Fatalf("svc rm = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result MutationResult
	decodeJSON(t, s, &result)
	if result.Action != actionUninstall {
		t.Errorf("the rm alias reported action %q, want %q -- an alias is a spelling, not a different operation",
			result.Action, actionUninstall)
	}
}

// TestMutatingCommandFailsFastOnAContendedLockWithNoWait is the end-to-end half of
// the lock: the other holder is a real lock on the real SVC dir, taken the way
// another svc process would take it.
func TestMutatingCommandFailsFastOnAContendedLockWithNoWait(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	seed := func(h string) {
		home = h
		seedGoVersion(h, "1.22.0")
	}
	if code, s := pipedRun(t, seed, "current", goSdk); code != ExitOK {
		t.Fatalf("the run that creates the home = exit %d; stderr:\n%s", code, s.stderr)
	}
	held, err := svclock.Acquire(filepath.Join(home, ".svc"), nil)
	if err != nil {
		t.Fatalf("take the operation lock: %v", err)
	}
	defer held.Release()

	code, s := runHome(t, home, "--no-wait", "switch", goSdk, "1.22.0", "--yes")
	if code != ExitBusy {
		t.Fatalf("a contended --no-wait switch = exit %d, want %d; stderr:\n%s", code, ExitBusy, s.stderr)
	}
	if want := catalogWord(t, "zh", "errors.operation-in-progress", nil); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
}

// TestMutatingCommandWaitsForAContendedLockByDefault is the other half: without
// --no-wait the same contention becomes a pause and a note, and the switch still
// happens.
func TestMutatingCommandWaitsForAContendedLockByDefault(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	seed := func(h string) {
		home = h
		seedGoVersion(h, "1.22.0")
	}
	if code, s := pipedRun(t, seed, "current", goSdk); code != ExitOK {
		t.Fatalf("the run that creates the home = exit %d; stderr:\n%s", code, s.stderr)
	}
	held, err := svclock.Acquire(filepath.Join(home, ".svc"), nil)
	if err != nil {
		t.Fatalf("take the operation lock: %v", err)
	}

	done := make(chan int, 1)
	stderr := make(chan string, 1)
	go func() {
		code, s := interactiveRunHome(t, home, "", "switch", goSdk, "1.22.0", "--yes")
		done <- code
		stderr <- s.stderr.String()
	}()
	// Give the command time to reach the lock and block on it.
	time.Sleep(200 * time.Millisecond)
	select {
	case code := <-done:
		t.Fatalf("the command finished with exit %d while another process held the lock", code)
	default:
	}
	if err := held.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if code := <-done; code != ExitOK {
		t.Fatalf("the waited-for switch = exit %d, want %d", code, ExitOK)
	}
	// A terminal run, so the note is written; the wait has to be explained.
	if want := catalogWord(t, "zh", "cli.waitingForLock", nil); !strings.Contains(<-stderr, want) {
		t.Error("the wait was not explained to the user")
	}
}

// --- the GUI/CLI parity test (§8) -------------------------------------------

// dialogRefusingRuntime is the wailsrt.Runtime the GUI supplies, reduced to what a
// switch needs. Using it rather than the CLI's own clirt runtime keeps the two
// halves of the parity test genuinely two shells over one service graph, and its
// pickers fail loudly: a service that tried to open a dialog would surface here as
// an error instead of hanging a test.
type dialogRefusingRuntime struct{ ctx context.Context }

func (r dialogRefusingRuntime) Context() context.Context { return r.ctx }

func (r dialogRefusingRuntime) EventsEmit(string, ...any) {}

func (r dialogRefusingRuntime) OpenFileDialog(string, []wailsrt.FileFilter) (string, error) {
	return "", errors.New("a switch must not open a file picker")
}

func (r dialogRefusingRuntime) OpenDirectoryDialog(string) (string, error) {
	return "", errors.New("a switch must not open a directory picker")
}

func (r dialogRefusingRuntime) Quit() {}

// stateFiles is the on-disk artefact set one switch is allowed to leave behind:
// the two JSON files the shims and the SDKs are resolved from, the rc file the
// shells source, and the three places its source line or its env vars are copied
// to. Comparing these is what makes "one kernel, two shells" a mechanical claim
// instead of a review habit.
var stateFiles = []string{
	".svc/config.json",
	".svc/shims.json",
	".svc.rc",
	".svc/env.sh.fish",
	".zshrc",
	".bashrc",
}

// stateMissing marks a file that was not written. A distinct marker rather than an
// empty string, so two runs that both failed to produce a file are not compared
// as equal.
const stateMissing = "<missing>"

// normalizeHome replaces the run's home directory with a fixed placeholder. Every
// absolute path in these files is a path inside the home, and the only difference
// between the two runs is which temporary directory each was given.
func normalizeHome(text, home string) string {
	return strings.ReplaceAll(text, home, "<HOME>")
}

func snapshotState(t *testing.T, home string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string, len(stateFiles))
	for _, rel := range stateFiles {
		raw, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rel)))
		if err != nil {
			if os.IsNotExist(err) {
				snapshot[rel] = stateMissing
				continue
			}
			t.Fatalf("read %s: %v", rel, err)
		}
		snapshot[rel] = normalizeHome(string(raw), home)
	}
	return snapshot
}

// newParityHome builds one of the two identical homes the parity test needs.
func newParityHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", "")
	seedGoVersion(home, "1.22.0", "1.23.0")
	return home
}

// guiSwitch runs the switch the way the GUI does: bootstrap at LevelWrite, then
// call the binding. DetailPanel.tsx handleSwitchVersion does exactly this and
// nothing else, so this is the reference behaviour.
func guiSwitch(t *testing.T, home, version string) {
	t.Helper()
	cfg, err := newTestConfig(home)
	if err != nil {
		t.Fatalf("config.NewConfig: %v", err)
	}
	ctx := context.Background()
	core, err := appcore.NewHome(cfg, nil).Bootstrap(ctx, dialogRefusingRuntime{ctx: ctx}, appcore.LevelWrite)
	if err != nil {
		t.Fatalf("the GUI's bootstrap: %v", err)
	}
	if err := core.Installer.SwitchVersion(goSdk, version); err != nil {
		t.Fatalf("the GUI's SwitchVersion: %v", err)
	}
}

// TestSwitchProducesTheSameStateAsTheGui is the parity test the design calls for:
// two identical homes, one switched through the GUI's own route and one through
// `svc switch`, compared byte for byte. It is the mechanical proof that the CLI is
// a shell over the same kernel rather than a second implementation that happens to
// look similar today.
func TestSwitchProducesTheSameStateAsTheGui(t *testing.T) {
	skipOnWindowsRegistryWrites(t)

	guiHome := newParityHome(t)
	guiSwitch(t, guiHome, "1.22.0")
	want := snapshotState(t, guiHome)

	cliHome := newParityHome(t)
	code, s := runHome(t, cliHome, "switch", goSdk, "1.22.0", "--yes")
	if code != ExitOK {
		t.Fatalf("svc switch = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	got := snapshotState(t, cliHome)

	for _, rel := range stateFiles {
		// Two missing files would compare equal, so the reference run has to have
		// written everything the list claims it writes.
		if want[rel] == stateMissing {
			t.Fatalf("the GUI path did not write %s, so the comparison of it proves nothing", rel)
		}
		if got[rel] == stateMissing {
			t.Errorf("the CLI path did not write %s but the GUI path did:\n%s", rel, want[rel])
			continue
		}
		if got[rel] != want[rel] {
			t.Errorf("%s differs between the two shells\n--- GUI ---\n%s\n--- CLI ---\n%s", rel, want[rel], got[rel])
		}
	}
}

// --- the external-manager refusal -------------------------------------------

// seedNodeManagerHome lays down the directory whose presence means "this machine
// belongs to nvm". Only the directory matters: DetectNodeManager asks whether a
// manager home exists, not whether it holds a working node, because a half-built
// ~/.nvm.rust is still a manager the user is going to run.
func seedNodeManagerHome(t *testing.T, home string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".nvm.rust"), 0755); err != nil {
		t.Fatal(err)
	}
}

// managedSdkSentence is the refusal as the shipped catalog renders it. The
// manager is asked of the machine rather than written out, because which one the
// host resolves to (nvm-rust from a seeded home, or nvm from a node already on
// the runner's PATH) is not this test's subject -- that the sentence reaches the
// user with the same labels the GUI uses is.
func managedSdkSentence(t *testing.T) string {
	t.Helper()
	manager := sdk.NodeManagerOnMachine()
	if manager == "" {
		t.Fatal("no external manager was detected, so the run cannot have been refused for one")
	}
	return catalogWord(t, "zh", "errors.managed-sdk", map[string]string{
		"sdk":     sdk.SdkDisplayName(sdk.NodeJS),
		"manager": sdk.ExternalManagerLabel(manager),
	})
}

// TestInstallRefusesAnSdkAnExternalManagerOwns pins the CLI's half of the rule
// and, more specifically, where it fires. The probe is injected rather than
// seeded: getPlatformPath caches the login shell's PATH behind a sync.Once, so
// no amount of t.Setenv can say what `node` resolves to here, and the host
// running the test may well have nvm of its own.
//
// The stdin assertion is the one that carries the weight. "y\n" is a user who
// said yes; finding it untouched proves the refusal happened before the question
// was asked, which is the difference between declining an operation and asking
// permission to decline it.
func TestInstallRefusesAnSdkAnExternalManagerOwns(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	s := newStreams()
	s.stdin.WriteString("y\n")
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	r.in.Env.StdinTTY = true
	r.in.Env.StdoutTTY = true
	core, err := r.bootstrap(appcore.LevelWrite)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(r.shutdown)
	core.Installer.NodeManagerProbe = func() string { return "nvm-rust" }

	err = r.runInstall(actionInstall, "nodejs", "22.11.0")
	if err == nil {
		t.Fatal("runInstall = nil on a machine nvm owns, want a refusal")
	}
	if got := ExitCodeForError(err); got != ExitUsage {
		t.Errorf("ExitCodeForError = %d, want %d: a refusal is a usage failure, not a crash (%v)", got, ExitUsage, err)
	}
	// stdout is the result stream. A refused install produced no result, so it
	// must stay empty or a script would read a sentence as an outcome.
	if s.stdout.Len() != 0 {
		t.Errorf("a refused install wrote %q to stdout", s.stdout)
	}
	if got := s.stdin.Len(); got != len("y\n") {
		t.Errorf("stdin has %d byte(s) left, want %d: the confirmation prompt was reached before the refusal", got, len("y\n"))
	}
}

// TestSwitchCommandRefusesAMachineAnExternalManagerOwns drives the whole command
// end to end, probe included, so the wiring is covered too: a refusal that only
// existed in runInstall would leave `svc switch` re-pointing the node shim at
// svc's copy, which is the exact fight the rule is there to prevent.
func TestSwitchCommandRefusesAMachineAnExternalManagerOwns(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	code, s := pipedRun(t, func(home string) {
		seedNodeManagerHome(t, home)
		seedNodeVersion(t, home)
	}, "switch", string(sdk.NodeJS), nodeVersion, "--yes")

	if code != ExitUsage {
		t.Fatalf("svc switch nodejs = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("a refused switch wrote %q to stdout", s.stdout)
	}
	if got := errLines(s); !slices.Equal(got, []string{"svc: " + managedSdkSentence(t)}) {
		t.Errorf("stderr = %q, want only the refusal", got)
	}
}

// TestInstallCommandRefusesAMachineAnExternalManagerOwns is the same rule from
// the other entry point, and deliberately runs without --yes: the refusal has to
// be a usage failure (exit 2) rather than the "a pipe cannot answer a prompt"
// failure a missing --yes would otherwise produce.
func TestInstallCommandRefusesAMachineAnExternalManagerOwns(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	code, s := pipedRun(t, func(home string) {
		seedNodeManagerHome(t, home)
	}, "install", string(sdk.NodeJS), nodeVersion)

	if code != ExitUsage {
		t.Fatalf("svc install nodejs = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	if !strings.Contains(s.stderr.String(), managedSdkSentence(t)) {
		t.Errorf("stderr = %q, want it to carry the refusal", s.stderr)
	}
}

// TestUninstallStaysAllowedOnAMachineAnExternalManagerOwns pins the escape
// route. Refusing to install and refusing to remove would leave a user who once
// installed Node.js through svc with a copy they cannot delete, and "keep using
// nvm" is not advice they can follow until that copy is gone.
func TestUninstallStaysAllowedOnAMachineAnExternalManagerOwns(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	var home string
	code, s := pipedRun(t, func(h string) {
		home = h
		seedNodeManagerHome(t, h)
		seedNodeVersion(t, h)
	}, "uninstall", string(sdk.NodeJS), nodeVersion, "--yes")

	if code != ExitOK {
		t.Fatalf("svc uninstall nodejs = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if strings.Contains(s.stderr.String(), "errors.managed-sdk") || strings.Contains(s.stdout.String(), "managed-sdk") {
		t.Errorf("an uninstall was refused for the external manager; stderr:\n%s", s.stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".svc", string(sdk.NodeJS), nodeVersion)); !os.IsNotExist(err) {
		t.Errorf("the version directory survived the uninstall (stat err = %v)", err)
	}
}

// TestOtherSdksAreUnaffectedByTheNodeManager pins the scope of the rule from the
// command level: a machine drowning in nvm still installs Go.
func TestOtherSdksAreUnaffectedByTheNodeManager(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	s := newStreams()
	s.stdin.WriteString("y\n")
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	r.in.Env.StdinTTY = true
	r.in.Env.StdoutTTY = true
	core, err := r.bootstrap(appcore.LevelWrite)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(r.shutdown)
	core.Installer.NodeManagerProbe = func() string { return "nvm-rust" }

	if err := core.Installer.ExternalManagerRefusal(goSdk); err != nil {
		t.Errorf("ExternalManagerRefusal(go) = %v on a machine nvm owns, want nil", err)
	}
}
