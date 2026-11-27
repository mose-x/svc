package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"svc/internal/config"
	"svc/internal/sdk"
	"svc/internal/svclock"
)

// The endpoint overrides are the one setting the GUI edits as a whole map and
// the CLI edits one entry at a time, so the tests here are mostly about the
// entries that were NOT named surviving -- which is the difference between a
// read-modify-write and the whole-map replace SaveEndpoints actually performs.
//
// None of these commands reaches appcore.LevelWrite, so unlike the install-path
// migration they run on all three CI platforms.

func TestEndpointsSetSavesOneOverrideAndKeepsTheOthers(t *testing.T) {
	var home string
	code, s := pipedRun(t, func(h string) {
		home = h
		writeSettings(t, h, config.AppSettings{
			Theme:     "system",
			Language:  "zh",
			Endpoints: map[string]string{"go": "https://old.example/", "jdk": "https://mirrors.example/adoptium"},
		})
	}, "--yes", "endpoints", "set", "go", "  https://goproxy.cn  ")
	if code != ExitOK {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	got := storedSettings(t, home).Endpoints
	if got["go"] != "https://goproxy.cn" {
		t.Errorf("the go override is %q, want the trimmed URL", got["go"])
	}
	// SaveEndpoints replaces the whole map. An override the command was not
	// told about is the one a naive implementation loses.
	if got["jdk"] != "https://mirrors.example/adoptium" {
		t.Errorf("the jdk override is %q, want it kept", got["jdk"])
	}
	if len(got) != 2 {
		t.Errorf("the map holds %v, want exactly the two overrides", got)
	}
	if want := catalogWord(t, "zh", "settings.settingsSaved", nil); !strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", s.stdout, want)
	}
}

// TestEndpointsSetReportsTheCanonicalIdentifier pins that the SDK is resolved
// before it is used as a map key: the override has to land under the identifier
// the fetchers look up, not under the spelling the user happened to type. A
// "Go" key would be an override nothing ever reads.
func TestEndpointsSetReportsTheCanonicalIdentifier(t *testing.T) {
	home := t.TempDir()
	code, s := runHome(t, home, "--yes", "endpoints", "set", goDisplayName(), "https://goproxy.cn")
	if code != ExitOK {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if _, ok := storedSettings(t, home).Endpoints[goSdk]; !ok {
		t.Errorf("the override is not stored under %q: %v", goSdk, storedSettings(t, home).Endpoints)
	}
	code, s = runHome(t, home, "--yes", "--json", "endpoints", "set", "GO", "https://goproxy.cn")
	if code != ExitOK {
		t.Fatalf("the json set = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result SettingsWriteResult
	decodeJSON(t, s, &result)
	want := SettingsWriteResult{Action: actionEndpointSet, Key: goSdk, Value: "https://goproxy.cn"}
	if result != want {
		t.Errorf("the document is %+v, want %+v", result, want)
	}
}

func TestEndpointsSetAsksFirst(t *testing.T) {
	home := t.TempDir()

	t.Run("a pipe without --yes is refused", func(t *testing.T) {
		code, s := runHome(t, home, "endpoints", "set", goSdk, "https://goproxy.cn")
		if code != ExitUsage {
			t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
		}
		if len(storedEndpoints(t, home)) != 0 {
			t.Errorf("an unconfirmed set wrote %v", storedEndpoints(t, home))
		}
	})

	t.Run("a decline changes nothing", func(t *testing.T) {
		code, s := interactiveRunHome(t, home, "n\n", "endpoints", "set", goSdk, "https://goproxy.cn")
		if code != ExitCanceled {
			t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
		}
		// The GUI's own two sentences: an endpoint override changes where every
		// later download comes from, so the question is not a formality.
		for _, key := range []string{"endpoint.confirmSave", "endpoint.confirmSaveDesc"} {
			if want := catalogWord(t, "zh", key, nil); !strings.Contains(s.stderr.String(), want) {
				t.Errorf("stderr = %q, want %q (%s)", s.stderr, want, key)
			}
		}
		if len(storedEndpoints(t, home)) != 0 {
			t.Errorf("a declined set wrote %v", storedEndpoints(t, home))
		}
	})

	t.Run("an acceptance saves it", func(t *testing.T) {
		code, s := interactiveRunHome(t, home, "y\n", "endpoints", "set", goSdk, "https://goproxy.cn")
		if code != ExitOK {
			t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		if got := storedEndpoints(t, home)[goSdk]; got != "https://goproxy.cn" {
			t.Errorf("the stored override is %q", got)
		}
	})
}

func TestEndpointsSetRefusesAnEmptyURL(t *testing.T) {
	home := t.TempDir()
	code, s := runHome(t, home, "--yes", "endpoints", "set", goSdk, "   ")
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	// The GUI's draft map treats an empty field as "reset this one". Here that
	// meaning has a command of its own, and the refusal names it instead of
	// silently deleting an override the user did not ask to delete.
	want := catalogWord(t, "zh", "cli.useCommand", map[string]string{"command": "svc endpoints unset <sdk>"})
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
	if len(storedEndpoints(t, home)) != 0 {
		t.Errorf("a refused set wrote %v", storedEndpoints(t, home))
	}
}

func TestEndpointsSetRefusesAnUnknownSdk(t *testing.T) {
	code, s := runHome(t, t.TempDir(), "--yes", "endpoints", "set", "cobol", "https://example.com")
	if code != ExitNotFound {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitNotFound, s.stderr)
	}
	if want := catalogWord(t, "zh", "errors.unknown-sdk-type", map[string]string{"sdk": "cobol"}); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
}

func TestEndpointsUnsetRemovesOnlyThatSdk(t *testing.T) {
	var home string
	code, s := pipedRun(t, func(h string) {
		home = h
		writeSettings(t, h, config.AppSettings{
			Endpoints: map[string]string{"go": "https://goproxy.cn", "jdk": "https://mirrors.example/adoptium"},
		})
	}, "--yes", "endpoints", "unset", goSdk)
	if code != ExitOK {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	got := storedSettings(t, home).Endpoints
	if _, ok := got[goSdk]; ok {
		t.Errorf("the go override survived: %v", got)
	}
	if got["jdk"] != "https://mirrors.example/adoptium" {
		t.Errorf("the jdk override is %q, want it kept", got["jdk"])
	}
	if want := catalogWord(t, "zh", "settings.settingsSaved", nil); !strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", s.stdout, want)
	}
}

// TestEndpointsUnsetNamesTheSdkByItsDisplayName pins the parameter the GUI's
// reset modal interpolates: the sentence a user reads says "Go", not "go",
// because that is what handleResetOneEndpoint passes.
func TestEndpointsUnsetNamesTheSdkByItsDisplayName(t *testing.T) {
	home := t.TempDir()
	code, s := interactiveRunHome(t, home, "n\n", "endpoints", "unset", goSdk)
	if code != ExitCanceled {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
	}
	params := map[string]string{"sdk": sdk.SdkDisplayName(sdk.Golang)}
	for _, key := range []string{"endpoint.confirmResetOne", "endpoint.confirmResetOneDesc"} {
		if want := catalogWord(t, "zh", key, params); !strings.Contains(s.stderr.String(), want) {
			t.Errorf("stderr = %q, want %q (%s)", s.stderr, want, key)
		}
	}
}

// TestEndpointsUnsetIsIdempotent pins the documented behaviour of unsetting
// what is not there: the state asked for is the state left behind, so it is a
// success rather than a not-found. A script that resets a fleet to defaults
// must not have to inspect each override first.
func TestEndpointsUnsetIsIdempotent(t *testing.T) {
	home := t.TempDir()
	code, s := runHome(t, home, "--yes", "endpoints", "unset", goSdk)
	if code != ExitOK {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if want := catalogWord(t, "zh", "settings.settingsSaved", nil); !strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", s.stdout, want)
	}
	var result SettingsWriteResult
	code, s = runHome(t, home, "--yes", "--json", "endpoints", "unset", goSdk)
	if code != ExitOK {
		t.Fatalf("the json unset = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	decodeJSON(t, s, &result)
	want := SettingsWriteResult{Action: actionEndpointUnset, Key: goSdk}
	if result != want {
		t.Errorf("the document is %+v, want %+v", result, want)
	}
}

func TestEndpointsSetFailsFastOnAContendedLockWithNoWait(t *testing.T) {
	home := t.TempDir()
	held, err := svclock.Acquire(filepath.Join(home, ".svc"), nil)
	if err != nil {
		t.Fatalf("take the operation lock: %v", err)
	}
	defer held.Release()

	code, s := runHome(t, home, "--no-wait", "--yes", "endpoints", "set", goSdk, "https://goproxy.cn")
	if code != ExitBusy {
		t.Fatalf("a contended --no-wait set = exit %d, want %d; stderr:\n%s", code, ExitBusy, s.stderr)
	}
	if len(storedEndpoints(t, home)) != 0 {
		t.Errorf("a refused set wrote %v", storedEndpoints(t, home))
	}
}

// TestEndpointsSetStaysReadOnly is the endpoint half of the same claim
// `settings set` makes: an override edits settings.json and nothing else.
func TestEndpointsSetStaysReadOnly(t *testing.T) {
	home := t.TempDir()
	code, s := runHome(t, home, "--yes", "endpoints", "set", goSdk, "https://goproxy.cn")
	if code != ExitOK {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".svc", "shims.json")); !os.IsNotExist(err) {
		t.Errorf("an endpoint override ran the shim setup (stat err = %v)", err)
	}
}

// TestCopyEndpointsIsNotAnAlias pins the reason the map is copied before it is
// edited. GetEndpoints returns the map the settings manager holds, so editing
// it in place would change what a later Get reports even if the save that
// follows failed -- an in-memory change nobody agreed to.
func TestCopyEndpointsIsNotAnAlias(t *testing.T) {
	source := map[string]string{"go": "https://goproxy.cn"}
	copied := copyEndpoints(source)
	copied["go"] = "https://other.example/"
	copied["jdk"] = "https://mirrors.example/"
	if source["go"] != "https://goproxy.cn" {
		t.Errorf("editing the copy changed the source: %v", source)
	}
	if len(source) != 1 {
		t.Errorf("the source grew to %v", source)
	}
	if got := copyEndpoints(nil); got == nil || len(got) != 0 {
		t.Errorf("copyEndpoints(nil) = %v, want an empty non-nil map", got)
	}
}

// storedEndpoints reads the override map back off disk, tolerating a
// settings.json that does not exist yet.
func storedEndpoints(t *testing.T, home string) map[string]string {
	t.Helper()
	path := filepath.Join(home, ".svc", "settings.json")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	return storedSettings(t, home).Endpoints
}
