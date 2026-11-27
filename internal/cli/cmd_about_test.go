package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"svc/internal/update"
)

// aboutJSON renders an about.json a test can inject through
// ExecuteInput.AboutJSON. It goes through the real struct rather than being
// written out as a literal so a renamed field shows up here as a compile error
// instead of as a silently ignored key in a hand-written document.
func aboutJSON(t *testing.T, info update.AppInfo) []byte {
	t.Helper()
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal about.json: %v", err)
	}
	return raw
}

// testAppInfo is the about.json the `about` and `self` tests share. The update
// URL is a caller-supplied one, so nothing here can reach a real release feed.
func testAppInfo(version, updateURL string) update.AppInfo {
	return update.AppInfo{
		Version:   version,
		GoVersion: "go1.25.0",
		License:   "MIT License",
		RepoURL:   "https://github.com/mose-x/svc",
		UpdateURL: updateURL,
	}
}

func TestAboutReportsTheEmbeddedAppInfo(t *testing.T) {
	home := t.TempDir()
	info := testAppInfo("2.1.0", "https://example.invalid/repos/mose-x/svc/releases/latest")

	t.Run("text renders the four rows of the About tab", func(t *testing.T) {
		code, s := runAboutAt(t, home, aboutJSON(t, info), "--lang", "en", "about")
		if code != ExitOK {
			t.Fatalf("svc about = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		text := s.stdout.String()
		for _, key := range []string{"about.version", "about.goVersion", "about.license", "about.repo"} {
			if want := catalogWord(t, "en", key, nil); !strings.Contains(text, want) {
				t.Errorf("stdout does not carry the label %q (%s):\n%s", want, key, text)
			}
		}
		// The version carries the "v" the About tab renders in front of it.
		for _, want := range []string{"v" + info.Version, info.GoVersion, info.License, info.RepoURL} {
			if !strings.Contains(text, want) {
				t.Errorf("stdout does not carry %q:\n%s", want, text)
			}
		}
		if s.stderr.Len() != 0 {
			t.Errorf("stderr = %q, want nothing: about is a result", s.stderr)
		}
	})

	t.Run("--json is GetAppInfo verbatim, with no envelope", func(t *testing.T) {
		code, s := runAboutAt(t, home, aboutJSON(t, info), "--json", "about")
		if code != ExitOK {
			t.Fatalf("svc --json about = exit %d, want %d", code, ExitOK)
		}
		var document map[string]json.RawMessage
		decodeJSON(t, s, &document)
		got := make([]string, 0, len(document))
		for key := range document {
			got = append(got, key)
		}
		slices.Sort(got)
		// The GetAppInfo binding hands the window exactly update.AppInfo, so
		// the document a script reads has exactly those keys: wrapping it would
		// create a second schema for one fact.
		if want := jsonKeysOf(t, update.AppInfo{}); !slices.Equal(got, want) {
			t.Errorf("svc about --json marshals to %v, want the service struct's %v", got, want)
		}
		var decoded update.AppInfo
		decodeJSON(t, s, &decoded)
		if decoded != info {
			t.Errorf("svc about --json = %+v, want %+v", decoded, info)
		}
	})
}

// TestAboutBuildsNoServiceGraph pins the LevelMeta half of the design: `about`
// reads what option resolution already parsed and stops there. The assertion is
// a file Bootstrap writes and option resolution does not -- mirrors.json is
// seeded by appcore.Bootstrap, so its absence proves the fourteen-SDK startup
// never ran. `svc list` is the control: same home shape, same command family,
// and it does bootstrap.
func TestAboutBuildsNoServiceGraph(t *testing.T) {
	mirrors := filepath.Join(".svc", "mirrors.json")

	t.Run("about leaves the bootstrap seed unwritten", func(t *testing.T) {
		home := t.TempDir()
		if code, s := runAboutAt(t, home, aboutJSON(t, testAppInfo("2.1.0", "")), "about"); code != ExitOK {
			t.Fatalf("svc about = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		if _, err := os.Stat(filepath.Join(home, mirrors)); err == nil {
			t.Errorf("%s exists after `svc about`; the command built a service graph it does not need", mirrors)
		}
	})

	t.Run("list, which does bootstrap, writes it", func(t *testing.T) {
		home := t.TempDir()
		if code, _ := runAboutAt(t, home, aboutJSON(t, testAppInfo("2.1.0", "")), "list"); code != ExitOK {
			t.Fatalf("svc list = exit %d, want %d", code, ExitOK)
		}
		if _, err := os.Stat(filepath.Join(home, mirrors)); err != nil {
			t.Errorf("%s is missing after `svc list`: %v -- the control case stopped proving anything", mirrors, err)
		}
	})
}

// TestAboutFallsBackWhenAboutJsonIsUnreadable pins the behaviour a corrupt or
// missing embed produces: ParseAppInfo's built-in defaults, reported like any
// other answer. A command that failed here would be a command that cannot say
// what version it is in exactly the situation where a user needs to know.
func TestAboutFallsBackWhenAboutJsonIsUnreadable(t *testing.T) {
	home := t.TempDir()
	code, s := runAboutAt(t, home, []byte("{not json"), "--json", "about")
	if code != ExitOK {
		t.Fatalf("svc about with a corrupt about.json = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var decoded update.AppInfo
	decodeJSON(t, s, &decoded)
	if want := update.ParseAppInfo([]byte("{not json")); decoded != want {
		t.Errorf("svc about = %+v, want ParseAppInfo's fallback %+v", decoded, want)
	}
	if decoded.Version == "" {
		t.Error("the fallback carries no version, so `svc about` would print an empty row")
	}
}
