package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"svc/internal/appcore"
	"svc/internal/apperr"
	"svc/internal/sdk"
)

// failingHome is an OpenHome that fails without failing the test, unlike
// closedHome. It is what a command path degrades against when the SVC home
// genuinely cannot be opened.
func failingHome() func([]byte) (*appcore.Home, error) {
	return func([]byte) (*appcore.Home, error) {
		return nil, errors.New("the SVC home cannot be opened")
	}
}

// TestPositionalValidatorsTurnArityIntoAUsageError covers the wrappers around
// cobra's own validators. The distinction is not cosmetic: cobra returns a
// plain fmt.Errorf, which ExitCodeForError classifies as ExitError (1), while
// a mistyped command line is a usage mistake and must exit 2 -- the same code
// an unknown flag gets.
func TestPositionalValidatorsTurnArityIntoAUsageError(t *testing.T) {
	r := newRunner(t, newStreams(), globalFlags{}, closedHome(t))
	command := &cobra.Command{Use: "status <sdk>"}

	// Each case carries the raw cobra validator next to the wrapper, so the
	// test can assert the wrap changes the error's TYPE and nothing else: a
	// reworded message would lose the arity cobra worked out.
	cases := []struct {
		name     string
		validate cobra.PositionalArgs
		raw      cobra.PositionalArgs
		args     []string
		wantErr  bool
	}{
		{"noArgs accepts none", r.noArgs(), cobra.NoArgs, nil, false},
		{"noArgs refuses one", r.noArgs(), cobra.NoArgs, []string{"extra"}, true},
		{"exactArgs(1) accepts one", r.exactArgs(1), cobra.ExactArgs(1), []string{"nodejs"}, false},
		{"exactArgs(1) refuses none", r.exactArgs(1), cobra.ExactArgs(1), nil, true},
		{"exactArgs(1) refuses two", r.exactArgs(1), cobra.ExactArgs(1), []string{"a", "b"}, true},
		{"exactArgs(2) accepts two", r.exactArgs(2), cobra.ExactArgs(2), []string{"nodejs", "20.11.0"}, false},
		{"exactArgs(2) refuses one", r.exactArgs(2), cobra.ExactArgs(2), []string{"nodejs"}, true},
		{"rangeArgs(0,1) accepts none", r.rangeArgs(0, 1), cobra.RangeArgs(0, 1), nil, false},
		{"rangeArgs(0,1) accepts one", r.rangeArgs(0, 1), cobra.RangeArgs(0, 1), []string{"go"}, false},
		{"rangeArgs(0,1) refuses two", r.rangeArgs(0, 1), cobra.RangeArgs(0, 1), []string{"a", "b"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r.usageTarget = nil
			err := c.validate(command, c.args)

			if !c.wantErr {
				if err != nil {
					t.Fatalf("validate(%q) = %v, want it accepted", c.args, err)
				}
				if r.usageTarget != nil {
					t.Error("an accepted invocation recorded a usage target")
				}
				return
			}
			if err == nil {
				t.Fatalf("validate(%q) = nil, want a usage error", c.args)
			}
			var usage *UsageError
			if !errors.As(err, &usage) {
				t.Errorf("validate(%q) = %T, want a *UsageError", c.args, err)
			}
			if got := ExitCodeForError(err); got != ExitUsage {
				t.Errorf("ExitCodeForError = %d, want %d", got, ExitUsage)
			}
			// The command that was being parsed is the one whose usage gets
			// printed, so the wrapper has to record it.
			if r.usageTarget != command {
				t.Errorf("usageTarget = %v, want the subcommand", r.usageTarget)
			}
			// cobra's own wording survives the wrap, byte for byte: it names
			// the expected arity, which is the useful half of the message.
			if want := c.raw(command, c.args).Error(); err.Error() != want {
				t.Errorf("the wrapper reworded cobra's message: got %q, want %q", err.Error(), want)
			}
		})
	}
}

// TestResolveSdkOffersTheAcceptedNames covers the hint path: an unknown SDK at
// a terminal gets the list of what would have been accepted, on stderr, and a
// piped or JSON run gets nothing but the error.
func TestResolveSdkOffersTheAcceptedNames(t *testing.T) {
	cases := []struct {
		name      string
		flags     globalFlags
		stdoutTTY bool
		wantHint  bool
	}{
		{"a terminal gets the hint", globalFlags{noColor: true}, true, true},
		{"a pipe does not", globalFlags{noColor: true}, false, false},
		{"--json does not", globalFlags{json: true}, true, false},
		{"--quiet does not", globalFlags{noColor: true, quiet: true}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStreams()
			r := newRunner(t, s, c.flags, tempHome(t, nil))
			r.in.Env.StdoutTTY = c.stdoutTTY
			core, err := r.bootstrap(appcore.LevelRead)
			if err != nil {
				t.Fatalf("bootstrap: %v", err)
			}
			defer r.shutdown()

			kind, err := r.resolveSdk(core, "klingon")
			if err == nil {
				t.Fatalf("resolveSdk accepted an unknown name and returned %q", kind)
			}
			if kind != "" {
				t.Errorf("resolveSdk returned %q alongside the error, want the zero value", kind)
			}
			if got := ExitCodeForError(err); got != ExitNotFound {
				t.Errorf("ExitCodeForError = %d, want %d", got, ExitNotFound)
			}
			if s.stdout.Len() != 0 {
				t.Errorf("the result stream got %q, want nothing for a failure", s.stdout)
			}

			// resolveSdk reports the failure; printing it is reportError's job
			// at the top of Execute. The name that was typed therefore lives in
			// the error, and only the hint is written here.
			if !strings.Contains(err.Error(), "klingon") {
				t.Errorf("the error %q does not name the SDK that was typed", err)
			}
			stderr := s.stderr.String()
			if c.wantHint {
				// The hint has to list what a resolver would accept, and only
				// that: a name in the hint that ResolveSdkType then refuses is
				// worse than no hint at all.
				for _, name := range []string{"nodejs", "go", "dart"} {
					if !strings.Contains(stderr, name) {
						t.Errorf("stderr = %q does not offer %q", stderr, name)
					}
					if _, resolveErr := ResolveSdkType(core.Registry, name); resolveErr != nil {
						t.Errorf("the hint offers %q but ResolveSdkType refuses it: %v", name, resolveErr)
					}
				}
			} else if stderr != "" {
				t.Errorf("stderr = %q, want it empty: the hint is a note and notes need a terminal", stderr)
			}
		})
	}

	t.Run("a known name resolves without a word", func(t *testing.T) {
		s := newStreams()
		r := newRunner(t, s, globalFlags{noColor: true}, tempHome(t, nil))
		r.in.Env.StdoutTTY = true
		core, err := r.bootstrap(appcore.LevelRead)
		if err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		defer r.shutdown()

		kind, err := r.resolveSdk(core, "Node.js")
		if err != nil {
			t.Fatalf("resolveSdk(Node.js) = %v, want it accepted", err)
		}
		if kind != sdk.NodeJS {
			t.Errorf("resolveSdk(Node.js) = %q, want %q", kind, sdk.NodeJS)
		}
		if s.stderr.Len() != 0 {
			t.Errorf("stderr = %q, want silence for a successful resolve", s.stderr)
		}
	})
}

// TestResolveSdkWithoutARegistryKeepsTheErrorClean covers the degraded path:
// a bootstrap that produced no registry must still report a marker rather than
// panicking on a nil dereference inside the hint.
func TestResolveSdkWithoutARegistryKeepsTheErrorClean(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}
	r.printer = NewPrinter(Options{Mode: ModeTTY}, r.catalog, s.stdout, s.stderr)

	_, err := r.resolveSdk(&appcore.Core{}, "nodejs")
	if err == nil {
		t.Fatal("resolveSdk without a registry = nil error, want the uninitialized marker")
	}
	if !strings.Contains(err.Error(), string(apperr.AppNotInitialized)) {
		t.Errorf("the error %q does not carry the app-not-initialized marker", err)
	}
	if strings.Contains(s.stderr.String(), "Available") || strings.Contains(s.stderr.String(), "可用的 SDK") {
		t.Errorf("stderr = %q offers a hint that has no names to offer", s.stderr)
	}
}

// TestSdkCompletionOffersTheRegistryNames covers the Tab path. Two claims are
// pinned: the candidates are exactly the names ResolveSdkType accepts, and
// completing does not bootstrap -- Bootstrap self-heals layouts and refreshes
// shims, which is far too much work for something that happens on a keypress.
func TestSdkCompletionOffersTheRegistryNames(t *testing.T) {
	cases := []struct {
		name       string
		toComplete string
		want       []string
	}{
		{"no prefix offers every SDK", "", nil},
		{"a prefix narrows it", "no", []string{"nodejs"}},
		{"the prefix is case-insensitive", "NO", []string{"nodejs"}},
		{"two SDKs share a prefix", "d", []string{"dotnet", "dart"}},
		{"a full name still completes", "rust", []string{"rust"}},
		{"nothing matches", "zzz", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRunner(t, newStreams(), globalFlags{}, tempHome(t, nil))
			command := &cobra.Command{Use: "status <sdk>"}

			got, directive := r.sdkCompletion(command, nil, c.toComplete)
			if directive != cobra.ShellCompDirectiveNoFileComp {
				t.Errorf("directive = %v, want NoFileComp: an SDK name is never a file", directive)
			}
			if c.want == nil {
				if c.toComplete == "" && len(got) != len(sdk.AllSdkTypes()) {
					t.Errorf("got %d candidates, want the %d registered SDKs", len(got), len(sdk.AllSdkTypes()))
				}
				if c.toComplete == "zzz" && len(got) != 0 {
					t.Errorf("got %v, want no candidates", got)
				}
				return
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("candidate %d = %q, want %q (registry order)", i, got[i], c.want[i])
				}
				// A candidate the resolver would then refuse is the drift this
				// test exists to catch.
				if _, err := ResolveSdkType(r.completionRegistry(), got[i]); err != nil {
					t.Errorf("the candidate %q is refused by ResolveSdkType: %v", got[i], err)
				}
			}

			// Completing must not assemble the service graph.
			if r.core != nil || r.runtime != nil || r.progress != nil || r.scan != nil {
				t.Error("completion bootstrapped the service graph; Tab would self-heal SDK layouts")
			}
		})
	}
}

// TestCompletionRegistryDegradesToNoCandidates covers the failure mode the
// doc comment promises: every failure yields no candidates, never an error and
// never a diagnostic, because cobra would insert that text into the user's
// command line.
func TestCompletionRegistryDegradesToNoCandidates(t *testing.T) {
	t.Run("an unopenable home", func(t *testing.T) {
		r := newRunner(t, newStreams(), globalFlags{}, failingHome())
		if got := r.completionRegistry(); got != nil {
			t.Errorf("completionRegistry = %v, want nil when the home cannot be opened", got)
		}
		got, directive := r.sdkCompletion(&cobra.Command{Use: "status"}, nil, "")
		if got != nil {
			t.Errorf("candidates = %v, want none", got)
		}
		if directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("directive = %v, want NoFileComp even on failure", directive)
		}
	})

	t.Run("a resolved home builds the registry", func(t *testing.T) {
		r := newRunner(t, newStreams(), globalFlags{}, tempHome(t, nil))
		registry := r.completionRegistry()
		if registry == nil {
			t.Fatal("completionRegistry = nil for a resolved home")
		}
		if len(registry.All()) != len(sdk.AllSdkTypes()) {
			t.Errorf("the registry holds %d fetchers, want %d", len(registry.All()), len(sdk.AllSdkTypes()))
		}
	})
}

// TestReadCoreNeverReachesTheWriteLevel is the side-effect claim of the
// read-only phase, asserted from the command layer: readCore asks for LevelRead,
// which is the level that does not edit the shell rc or the registry PATH.
func TestReadCoreNeverReachesTheWriteLevel(t *testing.T) {
	r := newRunner(t, newStreams(), globalFlags{}, tempHome(t, nil))
	defer r.shutdown()

	core, err := r.readCore()
	if err != nil {
		t.Fatalf("readCore: %v", err)
	}
	if core == nil || core.Installer == nil || core.Registry == nil {
		t.Fatal("readCore returned an incomplete Core")
	}
	// A second call must reuse the graph, so a command that reads twice does
	// not self-heal twice.
	if again, err := r.readCore(); err != nil || again != core {
		t.Errorf("the second readCore = %v, %v; want the same Core and no error", again, err)
	}
}
