package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"

	"svc/internal/update"
)

// The `self check` tests run against a loopback release feed.
//
// That is possible here and impossible for `settings check-proxy` in the same
// package: proxy.CheckProxy carries an SSRF guard that refuses 127.0.0.1, while
// Updater.CheckUpdate is an ordinary HTTP client with a URL taken from
// about.json. Injecting an about.json whose updateUrl points at an httptest
// server is therefore the whole trick -- no service is stubbed, and the code
// under test is the same code the GUI's Check button runs.

// hostAssetName builds a release-asset name matchPlatformAsset accepts on the
// host running the test. Deriving it from runtime.GOOS/GOARCH instead of
// writing out "macos-arm64" is what lets one test prove the
// "update available" path on all three CI operating systems.
func hostAssetName(t *testing.T, version string) string {
	t.Helper()
	osTokens := map[string]string{"windows": "windows", "darwin": "macos", "linux": "linux"}
	archTokens := map[string]string{"amd64": "x64", "arm64": "arm64"}
	osToken, archToken := osTokens[runtime.GOOS], archTokens[runtime.GOARCH]
	if osToken == "" || archToken == "" {
		t.Skipf("matchPlatformAsset names no asset for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	// The suffixes matchPlatformAsset rejects per platform: a .dmg on macOS, an
	// -installer.exe on Windows, a .deb/.rpm on Linux. Everything else takes no
	// suffix at all, which is what the Linux bare binary is called.
	extension := map[string]string{"windows": ".exe", "darwin": ".bin"}[runtime.GOOS]
	return fmt.Sprintf("svc-%s-%s-%s%s", version, osToken, archToken, extension)
}

// releaseAsset is one entry of a GitHub release's asset list.
type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// release is one entry of the releases list CheckUpdate fetches.
type release struct {
	Tag    string         `json:"tag_name"`
	Body   string         `json:"body"`
	Assets []releaseAsset `json:"assets"`
}

// newReleaseFeed serves a GitHub-shaped releases list at <server>/releases and
// the checksum manifest the assets point at, and returns the updateUrl to put
// in an injected about.json.
//
// CheckUpdate strips a trailing "/latest" off the configured URL and fetches
// the LIST, so the injected value has to carry that suffix for the command to
// take the same path a real about.json sends it down. The handlers build their
// own absolute URLs from the request's Host because an httptest server's port
// is not known until after it is listening.
func newReleaseFeed(t *testing.T, releases []release) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/releases", func(w http.ResponseWriter, req *http.Request) {
		rewritten := make([]release, len(releases))
		for i, entry := range releases {
			rewritten[i] = entry
			rewritten[i].Assets = make([]releaseAsset, 0, len(entry.Assets))
			for _, asset := range entry.Assets {
				asset.URL = "http://" + req.Host + "/" + asset.Name
				rewritten[i].Assets = append(rewritten[i].Assets, asset)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := writeJSONBody(w, rewritten); err != nil {
			t.Errorf("serve the releases list: %v", err)
		}
	})
	// The manifest lists every asset in every release, each at the same fixed
	// digest. fetchAssetSha256 picks the line whose second field names the
	// asset it chose, so a manifest that listed only itself -- the name it is
	// served under -- would match nothing and the sha256 assertion below would
	// be asserting that an empty string arrived.
	mux.HandleFunc("/"+updateTestAsset, func(w http.ResponseWriter, req *http.Request) {
		for _, entry := range releases {
			for _, asset := range entry.Assets {
				if asset.Name == updateTestAsset {
					continue
				}
				_, _ = io.WriteString(w, updateTestDigest+"  "+asset.Name+"\n")
			}
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL + "/releases/latest"
}

// updateTestAsset is the checksum manifest's own asset name, and
// updateTestDigest the hash it publishes for every other asset. Both are fixed
// so a test can assert the value arrived in the result rather than merely that
// something did.
const (
	updateTestAsset  = "sha256sums.txt"
	updateTestDigest = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
)

func writeJSONBody(w http.ResponseWriter, value any) error {
	return json.NewEncoder(w).Encode(value)
}

// released builds a release entry carrying the host's asset and a checksum
// manifest, which is the shape a real svc release has.
func released(t *testing.T, version, changelog string) release {
	t.Helper()
	return release{
		Tag:  "v" + version,
		Body: changelog,
		Assets: []releaseAsset{
			{Name: hostAssetName(t, version), Size: 2048},
			{Name: updateTestAsset, Size: 128},
		},
	}
}

func TestSelfCheckReportsAnAvailableUpdate(t *testing.T) {
	const (
		current   = "2.0.0"
		latest    = "2.1.0"
		changelog = "- a fix\n- another fix"
	)
	// The pre-release is listed FIRST and is newer, so a filter that let it
	// through would report 9.9.9 and this test would fail on the version rather
	// than on a subtler difference.
	feed := newReleaseFeed(t, []release{
		{Tag: "v9.9.9-rc1", Body: "a build nobody should be offered"},
		released(t, latest, changelog),
	})
	home := t.TempDir()
	about := aboutJSON(t, testAppInfo(current, feed))

	t.Run("text renders the update modal's own sentences", func(t *testing.T) {
		code, s := runAboutAt(t, home, about, "--lang", "en", "self", "check")
		if code != ExitOK {
			t.Fatalf("svc self check = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		text := s.stdout.String()
		for _, want := range []string{
			catalogWord(t, "en", "about.newVersion", map[string]string{"version": latest}),
			// The modal's body line: the current-version sentence, an arrow,
			// and the new version. Spelled the same way here.
			catalogWord(t, "en", "about.currentVersion", map[string]string{"version": current}) + " → v" + latest,
			hostAssetName(t, latest),
			catalogWord(t, "en", "cli.downloadURL", nil),
			// The release notes are a bare block, unlabelled, exactly as the
			// modal renders them.
			changelog,
		} {
			if !strings.Contains(text, want) {
				t.Errorf("stdout does not carry %q:\n%s", want, text)
			}
		}
		if s.stderr.Len() != 0 {
			t.Errorf("stderr = %q, want nothing: a found update is a result", s.stderr)
		}
	})

	t.Run("--json carries the service struct verbatim under one envelope", func(t *testing.T) {
		code, s := runAboutAt(t, home, about, "--json", "self", "check")
		if code != ExitOK {
			t.Fatalf("svc --json self check = exit %d, want %d", code, ExitOK)
		}
		var result SelfCheckResult
		decodeJSON(t, s, &result)
		if result.Current != current {
			t.Errorf("current = %q, want %q", result.Current, current)
		}
		if !result.Update.HasUpdate || result.Update.LatestVersion != latest {
			t.Errorf("update = %+v, want a %s update", result.Update, latest)
		}
		if result.Update.Filename != hostAssetName(t, latest) {
			t.Errorf("filename = %q, want the host's asset %q", result.Update.Filename, hostAssetName(t, latest))
		}
		if result.Update.Sha256 != updateTestDigest {
			t.Errorf("sha256 = %q, want the digest the manifest published", result.Update.Sha256)
		}
		if !strings.HasPrefix(result.Update.DownloadURL, "http://127.0.0.1:") {
			t.Errorf("downloadUrl = %q, want the loopback asset URL", result.Update.DownloadURL)
		}
		// The envelope is the CLI's own two fields; everything under `update`
		// is update.UpdateInfo and must stay exactly its shape.
		var document map[string]json.RawMessage
		decodeJSON(t, s, &document)
		if got, want := topLevelKeys(document), []string{"current", "update"}; !slices.Equal(got, want) {
			t.Errorf("svc self check --json marshals to %v, want %v", got, want)
		}
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(document["update"], &nested); err != nil {
			t.Fatalf("the update field is not an object: %v", err)
		}
		if got, want := topLevelKeys(nested), jsonKeysOf(t, update.UpdateInfo{}); !slices.Equal(got, want) {
			t.Errorf("update marshals to %v, want the service struct's %v", got, want)
		}
	})
}

func TestSelfCheckReportsUpToDate(t *testing.T) {
	const current = "2.1.0"
	feed := newReleaseFeed(t, []release{released(t, current, "the release you are running")})
	home := t.TempDir()

	code, s := runAboutAt(t, home, aboutJSON(t, testAppInfo(current, feed)), "--lang", "en", "self", "check")
	if code != ExitOK {
		t.Fatalf("svc self check = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	// The GUI toasts this one sentence and shows nothing else.
	if want := catalogWord(t, "en", "about.upToDate", nil); !strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout does not carry %q:\n%s", want, s.stdout)
	}
	if lines := outLines(s); len(lines) != 1 {
		t.Errorf("stdout has %d lines, want only the up-to-date sentence:\n%s", len(lines), s.stdout)
	}

	var result SelfCheckResult
	code, s = runAboutAt(t, home, aboutJSON(t, testAppInfo(current, feed)), "--json", "self", "check")
	if code != ExitOK {
		t.Fatalf("svc --json self check = exit %d, want %d", code, ExitOK)
	}
	decodeJSON(t, s, &result)
	if result.Update.HasUpdate {
		t.Errorf("update = %+v, want hasUpdate false", result.Update)
	}
	// The version it compared against is still reported: "up to date" without
	// the number is an answer a script cannot log.
	if result.Update.LatestVersion != current {
		t.Errorf("latestVersion = %q, want %q even when there is no update", result.Update.LatestVersion, current)
	}
}

// TestSelfCheckIgnoresPrereleases pins the filter that keeps an -rc1 or -beta
// build from ever being offered. A feed with nothing but a newer pre-release is
// "up to date with respect to stable releases", and an error here would be
// worse than a wrong version: a CI job gating on the exit code would fail.
func TestSelfCheckIgnoresPrereleases(t *testing.T) {
	feed := newReleaseFeed(t, []release{{Tag: "v9.9.9-rc1", Body: "a build nobody should be offered"}})
	home := t.TempDir()

	code, s := runAboutAt(t, home, aboutJSON(t, testAppInfo("2.0.0", feed)), "--lang", "en", "self", "check")
	if code != ExitOK {
		t.Fatalf("svc self check = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if want := catalogWord(t, "en", "about.upToDate", nil); !strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout does not carry %q:\n%s", want, s.stdout)
	}
	if strings.Contains(s.stdout.String(), "9.9.9") {
		t.Errorf("stdout offers the pre-release:\n%s", s.stdout)
	}
}

// TestSelfCheckWithoutAnUpdateURLFails is the state a build with no release
// feed configured is in -- which is the state every test run is in by default,
// because runArgs injects no about.json at all and ParseAppInfo's fallback
// leaves UpdateURL empty.
func TestSelfCheckWithoutAnUpdateURLFails(t *testing.T) {
	home := t.TempDir()

	code, s := runAboutAt(t, home, nil, "self", "check")
	if code != ExitError {
		t.Errorf("svc self check with no update URL = exit %d, want %d", code, ExitError)
	}
	if !strings.Contains(s.stderr.String(), "update URL is not configured") {
		t.Errorf("stderr does not name the cause:\n%s", s.stderr)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing: the check produced no result", s.stdout)
	}
}

// TestSelfCheckReportsAFeedFailure pins the exit code a script gates on: an
// unreachable or rate-limited feed is a transport failure, which is 6 and not
// 1, so "the network is down" stays distinguishable from "svc is broken".
func TestSelfCheckReportsAFeedFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	home := t.TempDir()

	code, s := runAboutAt(t, home, aboutJSON(t, testAppInfo("2.0.0", server.URL+"/releases/latest")), "self", "check")
	if code != ExitNetwork {
		t.Errorf("svc self check against a 403 feed = exit %d, want %d; stderr:\n%s", code, ExitNetwork, s.stderr)
	}
	// The service returns an apperr marker, so the user sees the catalog's own
	// sentence rather than the raw "[svc:update-http-status]{...}" form.
	if want := catalogWord(t, "zh", "errors.update-http-status", map[string]string{"status": "403"}); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr does not carry %q:\n%s", want, s.stderr)
	}
}

// TestSelfCheckReportsAMissingAsset covers the case where a newer release
// exists but carries nothing for this platform. The service treats that as a
// failure rather than as "up to date", because the second would tell a user on
// an unsupported platform that they are current when they are not.
func TestSelfCheckReportsAMissingAsset(t *testing.T) {
	feed := newReleaseFeed(t, []release{{
		Tag:    "v9.9.9",
		Body:   "a release with a source archive only",
		Assets: []releaseAsset{{Name: "svc-9.9.9-source.tar.gz", Size: 1}},
	}})
	home := t.TempDir()

	code, s := runAboutAt(t, home, aboutJSON(t, testAppInfo("2.0.0", feed)), "self", "check")
	if code != ExitNotFound {
		t.Errorf("svc self check with no matching asset = exit %d, want %d; stderr:\n%s", code, ExitNotFound, s.stderr)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", s.stdout)
	}
}

// topLevelKeys returns a JSON object's sorted key set. It is the shape
// assertion for the envelopes this package adds around service structs, which
// jsonKeysOf cannot make directly from a decoded document.
func topLevelKeys(document map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
