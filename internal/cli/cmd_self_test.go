package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"svc/internal/appcore"
	"svc/internal/apperr"
	"svc/internal/svclock"
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
	return newReleaseFeedPublishing(t, releases, updateTestDigest)
}

// newReleaseFeedPublishing is newReleaseFeed with the published digest chosen by
// the caller, which is how a test drives the checksum gate into failing: the
// bytes served stay the same and only the manifest lies about them.
func newReleaseFeedPublishing(t *testing.T, releases []release, digest string) string {
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
				_, _ = io.WriteString(w, digest+"  "+asset.Name+"\n")
			}
		}
	})
	// Every other asset is served at /<name> with the same bytes, so a command
	// that downloads one really downloads something.
	for _, entry := range releases {
		for _, asset := range entry.Assets {
			if asset.Name == updateTestAsset {
				continue
			}
			mux.HandleFunc("/"+asset.Name, func(w http.ResponseWriter, req *http.Request) {
				_, _ = w.Write(updateTestPayload)
			})
		}
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL + "/releases/latest"
}

// updateTestAsset is the checksum manifest's own asset name.
const updateTestAsset = "sha256sums.txt"

// updateTestPayload is the bytes every other asset on the loopback feed is
// served with, and updateTestDigest the hash the manifest publishes for them.
//
// The digest is computed rather than written out, and that is what lets a test
// drive `svc self update` all the way through DownloadUpdate: a manifest
// carrying a made-up hash would fail the checksum gate every time, so the only
// reachable outcome would be the failure and the happy path would be untested.
// A test can still assert the value arrived, because the constant it compares
// against is the same one the manifest was built from.
var (
	updateTestPayload = []byte("svc self-update test payload\n")
	updateTestDigest  = fmt.Sprintf("%x", sha256.Sum256(updateTestPayload))
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

// --- `svc self update` and `svc self rollback` --------------------------------
//
// The swap itself is never allowed to run in a test, and the reason is concrete
// rather than theoretical: ApplyUpdate and RollbackUpdate write a script that
// waits for THIS process to exit and then renames the binary it ran from, which
// under `go test` is the test executable. A suite that let one go would leave a
// background process swapping the test binary out from under itself up to a
// minute later. So ExecuteInput.Swap stands in for it, records what it was asked
// to do, and the scripts themselves are pinned where they can be -- in the
// builders in internal/update.
//
// What the injection costs is nothing much: the command's whole job is the
// orchestration around the swap (check, ask, lock, download, hand over, report)
// and all of that still runs for real, against the same loopback release feed
// `self check` uses.

// swapRecorder records the binary swaps a run asked for.
type swapRecorder struct {
	actions []SwapAction
	opts    []update.ApplyOptions
	// err, when set, is what the swap reports instead of succeeding.
	err error
}

func (rec *swapRecorder) fn() SwapFunc {
	return func(_ *update.Updater, action SwapAction, opts update.ApplyOptions) error {
		rec.actions = append(rec.actions, action)
		rec.opts = append(rec.opts, opts)
		return rec.err
	}
}

// called reports whether a swap was handed over at all.
func (rec *swapRecorder) called() bool { return len(rec.actions) > 0 }

// assertNoRelaunch is the red line: every swap a CLI command hands over must
// leave the relaunch off, or the script reopens a window at the end of a
// terminal command and, on the way out, prints its own lines into a stdout that
// has already been closed.
func (rec *swapRecorder) assertNoRelaunch(t *testing.T) {
	t.Helper()
	if len(rec.opts) == 0 {
		t.Fatal("no swap was handed over, so there is nothing to check")
	}
	for i, opts := range rec.opts {
		if opts.Relaunch {
			t.Errorf("swap %d relaunched the binary: a terminal command must never open a window", i)
		}
	}
}

// pipedEnv is an invocation whose stdout is a pipe: plain mode, no colour, no
// notes. terminalEnv is the same run at a terminal with colour off, so notes are
// written and every assertion can stay on plain text.
var (
	pipedEnv    = &Environment{}
	terminalEnv = &Environment{StdinTTY: true, StdoutTTY: true, EnvNoColor: "1"}
)

// selfRun drives one `svc self` invocation against an injected about.json, an
// injected swap and a chosen environment.
func selfRun(t *testing.T, home string, about []byte, swap SwapFunc, env *Environment, answers string, args ...string) (int, *streams) {
	t.Helper()
	s := newStreams()
	s.stdin.WriteString(answers)
	code := Execute(ExecuteInput{
		Args:      args,
		Catalogs:  shippedCatalogs(t),
		AboutJSON: about,
		Stdin:     s.stdin,
		Stdout:    s.stdout,
		Stderr:    s.stderr,
		OpenHome:  homeAt(t, home),
		Env:       env,
		Swap:      swap,
	})
	return code, s
}

// redirectTempDir points os.TempDir at a fresh temporary directory for the rest
// of the test.
//
// DownloadUpdate writes to a FIXED name inside it -- getUpdateFilePath has no
// home of its own to be injected with -- so a test that left the payload in the
// machine's real temp dir would collide with every other svc process on the box,
// and with the next run of itself.
func redirectTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	return dir
}

// updatePayloadPath is where DownloadUpdate leaves the bytes, per platform.
func updatePayloadPath(dir string) string {
	name := "svc_update_new"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

func TestSelfUpdateReportsUpToDateWithoutAsking(t *testing.T) {
	const current = "2.1.0"
	feed := newReleaseFeed(t, []release{released(t, current, "the release you are running")})
	home := t.TempDir()
	about := aboutJSON(t, testAppInfo(current, feed))
	rec := &swapRecorder{}

	// --yes is passed and still nothing is asked, because there is nothing to
	// do: the window toasts "you are up to date" and closes the modal.
	code, s := selfRun(t, home, about, rec.fn(), pipedEnv, "", "--lang", "en", "self", "update", "--yes")
	if code != ExitOK {
		t.Fatalf("svc self update on the newest build = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if want := catalogWord(t, "en", "about.upToDate", nil); !strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout does not carry %q:\n%s", want, s.stdout)
	}
	if rec.called() {
		t.Errorf("a build that is already current handed over a swap: %v", rec.actions)
	}

	code, s = selfRun(t, home, about, rec.fn(), pipedEnv, "", "--json", "self", "update", "--yes")
	if code != ExitOK {
		t.Fatalf("svc --json self update = exit %d, want %d", code, ExitOK)
	}
	var result SelfApplyResult
	decodeJSON(t, s, &result)
	if result.Applied {
		t.Errorf("applied = true for a run that changed nothing")
	}
	if result.Action != string(SwapUpdate) || result.From != current {
		t.Errorf("result = %+v, want action %q from %q", result, SwapUpdate, current)
	}
	// A document is emitted even though nothing happened, so a cron job can run
	// `svc self update --json --yes` unconditionally and always parse one. To
	// and filename stay out: there is no version and no asset to name.
	var document map[string]json.RawMessage
	decodeJSON(t, s, &document)
	if got, want := topLevelKeys(document), []string{"action", "applied", "from"}; !slices.Equal(got, want) {
		t.Errorf("svc self update --json marshals to %v, want %v", got, want)
	}
}

func TestSelfUpdateDownloadsAndHandsOverTheSwap(t *testing.T) {
	const (
		current = "2.0.0"
		latest  = "2.1.0"
	)
	feed := newReleaseFeed(t, []release{released(t, latest, "- a fix")})
	home := t.TempDir()
	about := aboutJSON(t, testAppInfo(current, feed))
	tmp := redirectTempDir(t)
	rec := &swapRecorder{}

	t.Run("text reports the hand-over, not a success", func(t *testing.T) {
		code, s := selfRun(t, home, about, rec.fn(), pipedEnv, "", "--lang", "en", "self", "update", "--yes")
		if code != ExitOK {
			t.Fatalf("svc self update = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		if want := catalogWord(t, "en", "cli.selfUpdateApplied", nil); !strings.Contains(s.stdout.String(), want) {
			t.Errorf("stdout does not carry the hand-over sentence %q:\n%s", want, s.stdout)
		}
		if len(rec.actions) != 1 || rec.actions[0] != SwapUpdate {
			t.Fatalf("swaps handed over = %v, want exactly one %q", rec.actions, SwapUpdate)
		}
		rec.assertNoRelaunch(t)

		// The download really happened and really passed the checksum gate:
		// DownloadUpdate deletes the payload on a mismatch, so bytes at the
		// fixed path are the proof.
		got, err := os.ReadFile(updatePayloadPath(tmp))
		if err != nil {
			t.Fatalf("the downloaded payload is not at %s: %v", updatePayloadPath(tmp), err)
		}
		if !slices.Equal(got, updateTestPayload) {
			t.Errorf("the payload on disk is %q, want the bytes the feed served", got)
		}
		// Progress is decoration and belongs on stderr, never in the result
		// stream a caller is piping somewhere.
		if strings.Contains(s.stdout.String(), "Downloading") {
			t.Errorf("progress reached stdout:\n%s", s.stdout)
		}
	})

	t.Run("--json emits exactly one document", func(t *testing.T) {
		code, s := selfRun(t, home, about, rec.fn(), pipedEnv, "", "--json", "self", "update", "--yes")
		if code != ExitOK {
			t.Fatalf("svc --json self update = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		var result SelfApplyResult
		decodeJSON(t, s, &result)
		if !result.Applied || result.Action != string(SwapUpdate) {
			t.Errorf("result = %+v, want an applied update", result)
		}
		if result.From != current || result.To != latest {
			t.Errorf("result = %+v, want %s → %s", result, current, latest)
		}
		if result.Filename != hostAssetName(t, latest) {
			t.Errorf("filename = %q, want the asset that was swapped in", result.Filename)
		}
		var document map[string]json.RawMessage
		decodeJSON(t, s, &document)
		if got, want := topLevelKeys(document), []string{"action", "applied", "filename", "from", "to"}; !slices.Equal(got, want) {
			t.Errorf("svc self update --json marshals to %v, want %v", got, want)
		}
		// The download emitted progress, and every line of it went to stderr:
		// stdout parsed as one document above, which it could not have if a
		// single progress line had landed there.
		if s.stderr.Len() == 0 {
			t.Error("stderr is empty: the download emitted no progress at all")
		}
	})
}

// TestSelfUpdateSettlesTheProgressLineBeforeTheResult drives the whole run
// through one buffer, because the property is ordering rather than content: the
// download renders a tty progress line that carries no newline of its own, so
// the hand-over sentence has to finish it first or it lands on the same line,
// behind the last frame's padding. stdout and stderr point at the same buffer to
// preserve the order a terminal would have shown.
func TestSelfUpdateSettlesTheProgressLineBeforeTheResult(t *testing.T) {
	const (
		current = "2.0.0"
		latest  = "2.1.0"
	)
	feed := newReleaseFeed(t, []release{released(t, latest, "- a fix")})
	home := t.TempDir()
	about := aboutJSON(t, testAppInfo(current, feed))
	redirectTempDir(t)
	rec := &swapRecorder{}

	var terminal bytes.Buffer
	code := Execute(ExecuteInput{
		Args:      []string{"--lang", "en", "self", "update", "--yes"},
		Catalogs:  shippedCatalogs(t),
		AboutJSON: about,
		Stdin:     strings.NewReader(""),
		Stdout:    &terminal,
		Stderr:    &terminal,
		OpenHome:  homeAt(t, home),
		Env:       terminalEnv,
		Swap:      rec.fn(),
	})
	written := terminal.String()
	if code != ExitOK {
		t.Fatalf("svc self update at a terminal = exit %d, want %d:\n%s", code, ExitOK, written)
	}
	if !strings.Contains(written, "\r") {
		t.Fatalf("the run rendered no progress line, so it settles nothing:\n%q", written)
	}

	result := catalogWord(t, "en", "cli.selfUpdateApplied", nil)
	at := strings.Index(written, result)
	if at <= 0 {
		t.Fatalf("the hand-over sentence is missing or starts the output:\n%q", written)
	}
	if written[at-1] != '\n' {
		t.Errorf("the result follows %q instead of starting a fresh line:\n%q", written[max(0, at-24):at], written)
	}
	// One newline settles the frame and one terminates the sentence. Anything
	// after that is the blank line the deferred Close used to add on top.
	if !strings.HasSuffix(written, "\n"+result+"\n") {
		t.Errorf("the run does not end with the settled result:\n%q", written)
	}
}

// TestSelfUpdateAsksTheWindowsOwnQuestion pins the confirmation to the modal's
// own words: the button that starts the download as the question, and the
// "current → latest" line from under the modal's title as the explanation. The
// window asks twice, once to download and once to restart, and a terminal cannot
// -- the payload lives in a temp file that dies with the run -- so the two
// collapse into the first.
func TestSelfUpdateAsksTheWindowsOwnQuestion(t *testing.T) {
	const (
		current = "2.0.0"
		latest  = "2.1.0"
	)
	feed := newReleaseFeed(t, []release{released(t, latest, "- a fix")})
	home := t.TempDir()
	about := aboutJSON(t, testAppInfo(current, feed))
	redirectTempDir(t)
	question := catalogWord(t, "zh", "about.downloadAndInstall", nil)
	arrow := catalogWord(t, "zh", "about.currentVersion", map[string]string{"version": current}) + " → v" + latest

	t.Run("a decline changes nothing", func(t *testing.T) {
		rec := &swapRecorder{}
		code, s := selfRun(t, home, about, rec.fn(), terminalEnv, "n\n", "self", "update")
		if code != ExitCanceled {
			t.Fatalf("a declined update = exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
		}
		if rec.called() {
			t.Errorf("a declined update handed over a swap: %v", rec.actions)
		}
		if s.stdout.Len() != 0 {
			t.Errorf("stdout = %q, want nothing: a cancelled command has no result", s.stdout)
		}
		if !strings.Contains(s.stderr.String(), question) {
			t.Errorf("stderr does not ask the modal's own question %q:\n%s", question, s.stderr)
		}
		if !strings.Contains(s.stderr.String(), arrow) {
			t.Errorf("stderr does not explain what replaces what (%q):\n%s", arrow, s.stderr)
		}
		if _, err := os.Stat(updatePayloadPath(os.TempDir())); err == nil {
			t.Error("a declined update still downloaded the payload")
		}
	})

	t.Run("an accept hands the swap over", func(t *testing.T) {
		rec := &swapRecorder{}
		code, _ := selfRun(t, home, about, rec.fn(), terminalEnv, "y\n", "self", "update")
		if code != ExitOK {
			t.Fatalf("an accepted update = exit %d, want %d", code, ExitOK)
		}
		if len(rec.actions) != 1 || rec.actions[0] != SwapUpdate {
			t.Errorf("swaps handed over = %v, want exactly one %q", rec.actions, SwapUpdate)
		}
		rec.assertNoRelaunch(t)
	})
}

// TestSelfUpdateRefusesToGuessWithoutATerminal is the property that makes an
// unattended binary swap impossible by accident: with no terminal on stdin and
// no --yes, the command refuses rather than assuming either answer.
func TestSelfUpdateRefusesToGuessWithoutATerminal(t *testing.T) {
	feed := newReleaseFeed(t, []release{released(t, "2.1.0", "- a fix")})
	home := t.TempDir()
	about := aboutJSON(t, testAppInfo("2.0.0", feed))
	redirectTempDir(t)
	rec := &swapRecorder{}

	code, s := selfRun(t, home, about, rec.fn(), pipedEnv, "", "self", "update")
	if code != ExitUsage {
		t.Fatalf("svc self update with no terminal and no --yes = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	if rec.called() {
		t.Errorf("the command handed over a swap it had no permission for: %v", rec.actions)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", s.stdout)
	}
	if !strings.Contains(s.stderr.String(), "--yes") {
		t.Errorf("stderr does not tell the caller how to answer in advance:\n%s", s.stderr)
	}
}

// TestSelfUpdateStopsAtAChecksumMismatch is the integrity gate: a payload whose
// bytes do not match the digest the release published must never reach the swap,
// because the swap is the step that puts those bytes in place of a working
// binary.
func TestSelfUpdateStopsAtAChecksumMismatch(t *testing.T) {
	const latest = "2.1.0"
	feed := newReleaseFeedPublishing(t, []release{released(t, latest, "- a fix")},
		"0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0")
	home := t.TempDir()
	about := aboutJSON(t, testAppInfo("2.0.0", feed))
	redirectTempDir(t)
	rec := &swapRecorder{}

	code, s := selfRun(t, home, about, rec.fn(), pipedEnv, "", "self", "update", "--yes")
	if code != ExitIntegrity {
		t.Fatalf("svc self update against a lying manifest = exit %d, want %d; stderr:\n%s", code, ExitIntegrity, s.stderr)
	}
	if rec.called() {
		t.Errorf("a payload that failed its checksum was handed to the swap: %v", rec.actions)
	}
	if want := catalogWord(t, "zh", "errors.checksum-mismatch", map[string]string{
		"expected": "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0",
		"got":      updateTestDigest,
	}); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing: the update did not happen", s.stdout)
	}
}

// TestSelfUpdateReportsASwapFailure covers the hand-over itself failing: the
// payload is down and verified, the script could not be started, and the command
// must say so rather than report a swap that is not running.
func TestSelfUpdateReportsASwapFailure(t *testing.T) {
	feed := newReleaseFeed(t, []release{released(t, "2.1.0", "- a fix")})
	home := t.TempDir()
	about := aboutJSON(t, testAppInfo("2.0.0", feed))
	redirectTempDir(t)
	rec := &swapRecorder{err: errors.New("failed to launch update script")}

	code, s := selfRun(t, home, about, rec.fn(), pipedEnv, "", "self", "update", "--yes")
	if code != ExitError {
		t.Fatalf("a failed hand-over = exit %d, want %d", code, ExitError)
	}
	if !strings.Contains(s.stderr.String(), "failed to launch update script") {
		t.Errorf("stderr does not carry the failure:\n%s", s.stderr)
	}
	if want := catalogWord(t, "zh", "cli.selfUpdateApplied", nil); strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout claims a hand-over that failed:\n%s", s.stdout)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", s.stdout)
	}
}

// TestSelfUpdateWaitsBehindAnotherOperation pins the lock: a download and a
// binary swap are exactly the operations that must not overlap with an install
// running in the window next door.
func TestSelfUpdateWaitsBehindAnotherOperation(t *testing.T) {
	feed := newReleaseFeed(t, []release{released(t, "2.1.0", "- a fix")})
	home := t.TempDir()
	about := aboutJSON(t, testAppInfo("2.0.0", feed))
	redirectTempDir(t)

	// The lock lives in <home>/.svc, which is the directory the run resolves;
	// holding it from here is the same file the command would take.
	dir := filepath.Join(home, ".svc")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	held, err := svclock.Acquire(dir, nil)
	if err != nil {
		t.Fatalf("take the operation lock: %v", err)
	}
	defer held.Release()

	rec := &swapRecorder{}
	code, s := selfRun(t, home, about, rec.fn(), pipedEnv, "", "self", "update", "--yes", "--no-wait")
	if code != ExitBusy {
		t.Fatalf("svc self update --no-wait against a held lock = exit %d, want %d; stderr:\n%s", code, ExitBusy, s.stderr)
	}
	if rec.called() {
		t.Errorf("the swap ran while another svc operation held the lock: %v", rec.actions)
	}
}

func TestSelfRollbackHandsOverTheSwap(t *testing.T) {
	const current = "2.1.0"
	home := t.TempDir()
	// No about.json injection is needed for the version: rollback never talks
	// to the release feed, so the parsed fallback is all it reports.
	rec := &swapRecorder{}

	t.Run("text reports the hand-over", func(t *testing.T) {
		code, s := selfRun(t, home, nil, rec.fn(), pipedEnv, "", "--lang", "en", "self", "rollback", "--yes")
		if code != ExitOK {
			t.Fatalf("svc self rollback = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		if want := catalogWord(t, "en", "cli.selfRollbackApplied", nil); !strings.Contains(s.stdout.String(), want) {
			t.Errorf("stdout does not carry the hand-over sentence %q:\n%s", want, s.stdout)
		}
		if len(rec.actions) != 1 || rec.actions[0] != SwapRollback {
			t.Fatalf("swaps handed over = %v, want exactly one %q", rec.actions, SwapRollback)
		}
		rec.assertNoRelaunch(t)
	})

	t.Run("--json names no target version", func(t *testing.T) {
		code, s := selfRun(t, home, nil, rec.fn(), pipedEnv, "", "--json", "self", "rollback", "--yes")
		if code != ExitOK {
			t.Fatalf("svc --json self rollback = exit %d, want %d", code, ExitOK)
		}
		var result SelfApplyResult
		decodeJSON(t, s, &result)
		if !result.Applied || result.Action != string(SwapRollback) {
			t.Errorf("result = %+v, want an applied rollback", result)
		}
		// To stays out: what comes back is whatever the .bak holds, and reading
		// that would mean running the binary being restored.
		var document map[string]json.RawMessage
		decodeJSON(t, s, &document)
		if got, want := topLevelKeys(document), []string{"action", "applied", "from"}; !slices.Equal(got, want) {
			t.Errorf("svc self rollback --json marshals to %v, want %v", got, want)
		}
	})
}

// TestSelfRollbackAsksItsOwnQuestion pins the one place the CLI deliberately
// does not borrow the window's wording: settings.rollbackConfirmDesc ends by
// promising the app will restart, and a terminal swap must not. The question
// above it stays shared.
func TestSelfRollbackAsksItsOwnQuestion(t *testing.T) {
	home := t.TempDir()
	rec := &swapRecorder{}

	code, s := selfRun(t, home, nil, rec.fn(), terminalEnv, "n\n", "self", "rollback")
	if code != ExitCanceled {
		t.Fatalf("a declined rollback = exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
	}
	if rec.called() {
		t.Errorf("a declined rollback handed over a swap: %v", rec.actions)
	}
	stderr := s.stderr.String()
	if want := catalogWord(t, "zh", "settings.rollbackConfirm", nil); !strings.Contains(stderr, want) {
		t.Errorf("stderr does not ask the window's own question %q:\n%s", want, s.stderr)
	}
	if want := catalogWord(t, "zh", "cli.selfRollbackDesc", nil); !strings.Contains(stderr, want) {
		t.Errorf("stderr does not explain the rollback in the CLI's own words %q:\n%s", want, s.stderr)
	}
	// The borrowed sentence promises a restart. Printing it would confirm an
	// outcome the command then declines to produce.
	if borrowed := catalogWord(t, "zh", "settings.rollbackConfirmDesc", nil); strings.Contains(stderr, borrowed) {
		t.Errorf("stderr promises the window's restart:\n%s", s.stderr)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing: a cancelled command has no result", s.stdout)
	}
}

func TestSelfRollbackRefusesToGuessWithoutATerminal(t *testing.T) {
	home := t.TempDir()
	rec := &swapRecorder{}

	code, s := selfRun(t, home, nil, rec.fn(), pipedEnv, "", "self", "rollback")
	if code != ExitUsage {
		t.Fatalf("svc self rollback with no terminal and no --yes = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	if rec.called() {
		t.Errorf("the command handed over a rollback it had no permission for: %v", rec.actions)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", s.stdout)
	}
}

// TestSelfRollbackWithoutABackupExits3 drives the REAL RollbackUpdate -- no
// injected swap -- because the failure happens before anything is launched: the
// service stats the backup, finds none, and returns apperr.NoBackup. That is the
// whole reason the CLI does not pre-check with HasBackup: the service's error
// names the exact path it looked at, which a bool cannot.
func TestSelfRollbackWithoutABackupExits3(t *testing.T) {
	if (&update.Updater{}).HasBackup() {
		t.Skip("a .bak sits next to the test binary, so the real rollback would launch a swap script")
	}
	home := t.TempDir()

	code, s := selfRun(t, home, nil, nil, pipedEnv, "", "self", "rollback", "--yes")
	if code != ExitNotFound {
		t.Fatalf("svc self rollback with no backup = exit %d, want %d; stderr:\n%s", code, ExitNotFound, s.stderr)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if want := catalogWord(t, "zh", "errors.no-backup", map[string]string{"path": exe + ".bak"}); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
	if s.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing: there was no rollback to report", s.stdout)
	}
}

// --- the dispatch underneath --------------------------------------------------

// TestPerformSwapRejectsAnUnknownAction covers the default branch. Guessing here
// would mean replacing a binary, so an action the table does not name is an
// error rather than a fallback.
func TestPerformSwapRejectsAnUnknownAction(t *testing.T) {
	err := performSwap(nil, SwapAction("downgrade"), update.ApplyOptions{})
	if err == nil {
		t.Fatal("performSwap with an unknown action = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "downgrade") {
		t.Errorf("the refusal does not name the action: %v", err)
	}
}

// TestSwapBinaryNeverAsksForARelaunch pins the one decision the whole design
// turns on, at the seam where the command layer meets the service: whatever the
// action, the CLI hands it over with Relaunch off.
func TestSwapBinaryNeverAsksForARelaunch(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	rec := &swapRecorder{}
	r.swap = rec.fn()

	for _, action := range []SwapAction{SwapUpdate, SwapRollback} {
		// A nil Updater is safe: the recorder never touches it, and that is the
		// point -- the assertion is about the options the command layer builds.
		if err := r.swapBinary(nil, action); err != nil {
			t.Fatalf("swapBinary(%q): %v", action, err)
		}
	}
	if got, want := rec.actions, []SwapAction{SwapUpdate, SwapRollback}; !slices.Equal(got, want) {
		t.Errorf("actions = %v, want %v", got, want)
	}
	rec.assertNoRelaunch(t)
}

// TestBootstrapRoutesUpdateEventsToStderr pins the wiring end to end, the same
// way the scan test does: an update:progress event emitted through the runtime
// bootstrap built has to reach the progress renderer, on stderr, and never the
// result stream. Without it a self-update would download 40 MB in silence and
// then exit.
func TestBootstrapRoutesUpdateEventsToStderr(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if _, err := r.bootstrap(appcore.LevelRead); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer r.shutdown()
	if r.runtime == nil {
		t.Fatal("bootstrap did not build a runtime")
	}

	r.runtime.EventsEmit(EventUpdateProgress, update.UpdateProgress{
		Stage:   StageDownloading,
		Percent: 42,
		Message: "Downloading 12.0MB / 43.6MB",
	})
	if s.stdout.Len() != 0 {
		t.Errorf("the result stream got %q, want nothing while an update downloads", s.stdout)
	}
	if s.stderr.Len() == 0 {
		t.Fatal("stderr is empty: the runtime bootstrap wired dropped the update event")
	}
	if strings.Contains(s.stderr.String(), "progress.") {
		t.Errorf("stderr leaked a catalog key: %q", s.stderr)
	}
}

// TestApplyOptionsIsTheOnlyDifferenceBetweenTheShells is the source-level half of
// the red line: app.go must keep asking for a relaunch, because the window Quit
// closed has to come back.
func TestApplyOptionsIsTheOnlyDifferenceBetweenTheShells(t *testing.T) {
	source := string(repoFile(t, "app.go"))
	for _, call := range []string{
		"a.updater.ApplyUpdate(update.ApplyOptions{Relaunch: true})",
		"a.updater.RollbackUpdate(update.ApplyOptions{Relaunch: true})",
	} {
		if !strings.Contains(source, call) {
			t.Errorf("app.go no longer calls %s; the window would close and never come back after a swap", call)
		}
	}
	// And the two error codes the CLI promises in its help stay mapped.
	if got := ExitCodeForError(apperr.New(apperr.NoBackup, map[string]string{"path": "/x/svc.bak"})); got != ExitNotFound {
		t.Errorf("a missing backup exits %d, want %d", got, ExitNotFound)
	}
	if got := ExitCodeForError(apperr.New(apperr.ChecksumMismatch, nil)); got != ExitIntegrity {
		t.Errorf("a checksum mismatch exits %d, want %d", got, ExitIntegrity)
	}
}
