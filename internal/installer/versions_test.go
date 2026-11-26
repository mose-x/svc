package installer

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"svc/internal/apperr"
	"svc/internal/config"
	"svc/internal/proxy"
	"svc/internal/sdk"
)

// fakeVersionFetcher is a VersionFetcher whose remote list, failure and call
// count a test controls. It exists so the version entry points can be tested
// without a network: the real fetchers all reach a fixed upstream host, and a
// test that depended on nodejs.org answering would be a test of nodejs.org.
type fakeVersionFetcher struct {
	kind     sdk.SdkType
	versions []sdk.VersionInfo
	err      error

	mu      sync.Mutex
	fetches int
	clients []*http.Client
}

func (f *fakeVersionFetcher) FetchRemoteVersions() ([]sdk.VersionInfo, error) {
	f.mu.Lock()
	f.fetches++
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.versions, nil
}

func (f *fakeVersionFetcher) GetLocalStatus() (*sdk.SdkStatus, error) {
	return &sdk.SdkStatus{SdkType: f.kind, DisplayName: string(f.kind)}, nil
}

func (f *fakeVersionFetcher) GetDownloadURL(string) (string, string, error) { return "", "", nil }
func (f *fakeVersionFetcher) GetBinDirs() []string                          { return nil }
func (f *fakeVersionFetcher) GetExtraEnvVars() map[string]string            { return nil }
func (f *fakeVersionFetcher) VerifyCommand() (string, []string) {
	return "svc_test_nonexistent_cmd_xyz", []string{"--version"}
}
func (f *fakeVersionFetcher) Type() sdk.SdkType { return f.kind }
func (f *fakeVersionFetcher) SetHTTPClient(client *http.Client) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clients = append(f.clients, client)
}
func (f *fakeVersionFetcher) StripArchiveTopDir() bool { return false }

func (f *fakeVersionFetcher) fetchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches
}

func (f *fakeVersionFetcher) lastClient() *http.Client {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.clients) == 0 {
		return nil
	}
	return f.clients[len(f.clients)-1]
}

// newVersionsTestService wires a Service over a temp HOME with fakes registered
// over the real fetchers. A real SettingsManager and proxy.Service are supplied
// because fetchAndCacheVersions reads the proxy configuration before every
// fetch, and a nil one would panic instead of testing anything.
func newVersionsTestService(t *testing.T, fakes ...sdk.VersionFetcher) *Service {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg, err := config.NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetHomeDir(home)
	cfg.SetSvcDir(filepath.Join(home, ".svc"))
	settings := config.NewSettingsManager(home)
	registry := sdk.NewRegistry(cfg, settings)
	for _, fake := range fakes {
		registry.Register(fake)
	}
	return New(cfg, registry, nil, nil, nil, settings, proxy.New(settings), &recordingRuntime{})
}

func versionList(versions ...string) []sdk.VersionInfo {
	list := make([]sdk.VersionInfo, 0, len(versions))
	for _, version := range versions {
		list = append(list, sdk.VersionInfo{Version: version})
	}
	return list
}

// markerCode extracts the apperr code out of an error, or "" when there is none.
func markerCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	text := err.Error()
	if !strings.HasPrefix(text, "[svc:") {
		return ""
	}
	end := strings.Index(text, "]")
	if end < 0 {
		return ""
	}
	return text[len("[svc:"):end]
}

// TestVersionFetcherRejectsBadInput covers the three refusals versionFetcher
// makes, each of which a caller of RefreshRemoteVersions inherits: an unwired
// service, an identifier that must never reach a filesystem path, and a name the
// registry does not know. The last one has to keep the unknown-sdk-type marker,
// because that is what maps the failure to exit code 3 in the CLI and to the
// translated sentence in the GUI.
func TestVersionFetcherRejectsBadInput(t *testing.T) {
	t.Run("an unwired service", func(t *testing.T) {
		svc := &Service{}
		if _, err := svc.versionFetcher("nodejs"); markerCode(t, err) != string(apperr.AppNotInitialized) {
			t.Errorf("versionFetcher on a nil registry gave %v, want the app-not-initialized marker", err)
		}
		if _, err := svc.RefreshRemoteVersions("nodejs"); markerCode(t, err) != string(apperr.AppNotInitialized) {
			t.Errorf("RefreshRemoteVersions on a nil registry gave %v, want the app-not-initialized marker", err)
		}
	})

	t.Run("an identifier that is not a path segment", func(t *testing.T) {
		svc := newVersionsTestService(t)
		for _, bad := range []string{"", "..", "../etc", "a/b"} {
			if _, err := svc.versionFetcher(bad); err == nil {
				t.Errorf("versionFetcher(%q) accepted an identifier that must never reach a path", bad)
			}
			if _, err := svc.RefreshRemoteVersions(bad); err == nil {
				t.Errorf("RefreshRemoteVersions(%q) accepted an identifier that must never reach a path", bad)
			}
		}
	})

	t.Run("an unregistered name", func(t *testing.T) {
		svc := newVersionsTestService(t)
		_, err := svc.versionFetcher("nosuchsdk")
		if markerCode(t, err) != string(apperr.UnknownSdkType) {
			t.Fatalf("versionFetcher(nosuchsdk) gave %v, want the unknown-sdk-type marker", err)
		}
		if !strings.Contains(err.Error(), "nosuchsdk") {
			t.Errorf("the marker dropped the offending name: %v", err)
		}
		if _, err := svc.RefreshRemoteVersions("nosuchsdk"); markerCode(t, err) != string(apperr.UnknownSdkType) {
			t.Errorf("RefreshRemoteVersions(nosuchsdk) gave %v, want the unknown-sdk-type marker", err)
		}
	})
}

// TestRefreshRemoteVersionsFetchesAndCaches is the happy path of the entry point
// the CLI's --refresh flag and the detached `svc __refresh` child both call: the
// list arrives, it is handed back to the caller, it lands in the cache, and the
// fetch ran through a proxy-configured client.
func TestRefreshRemoteVersionsFetchesAndCaches(t *testing.T) {
	want := versionList("24.0.0", "22.11.0", "20.18.0")
	fake := &fakeVersionFetcher{kind: sdk.NodeJS, versions: want}
	svc := newVersionsTestService(t, fake)

	got, err := svc.RefreshRemoteVersions("nodejs")
	if err != nil {
		t.Fatalf("RefreshRemoteVersions: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d versions, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Version != want[i].Version {
			t.Errorf("version %d = %q, want %q (the list must stay in the fetched order)", i, got[i].Version, want[i].Version)
		}
	}
	if fake.fetchCount() != 1 {
		t.Errorf("the fetcher ran %d times, want exactly 1", fake.fetchCount())
	}
	if fake.lastClient() == nil {
		t.Error("no HTTP client was injected; the user's proxy configuration would be ignored")
	}

	cached, ok := sdk.GetCachedVersions(sdk.NodeJS)
	if !ok {
		t.Fatal("the fetched list was not cached")
	}
	if len(cached) != len(want) {
		t.Errorf("the cache holds %d versions, want %d", len(cached), len(want))
	}
}

// TestRefreshRemoteVersionsIgnoresAnExistingCache is what separates it from
// GetRemoteVersions: a caller that asks for a refresh must get a fetch even when
// a perfectly good cached list exists. Without this, `svc versions --refresh`
// and the background child would both be no-ops on the exact runs where they are
// the only way to see new data.
func TestRefreshRemoteVersionsIgnoresAnExistingCache(t *testing.T) {
	stale := versionList("1.0.0")
	fresh := versionList("2.0.0", "1.0.0")
	sdk.SetCachedVersions(sdk.Golang, stale)

	fake := &fakeVersionFetcher{kind: sdk.Golang, versions: fresh}
	svc := newVersionsTestService(t, fake)

	got, err := svc.RefreshRemoteVersions("go")
	if err != nil {
		t.Fatalf("RefreshRemoteVersions: %v", err)
	}
	if fake.fetchCount() != 1 {
		t.Errorf("the fetcher ran %d times, want 1: a refresh must not be satisfied by the cache", fake.fetchCount())
	}
	if len(got) != 2 || got[0].Version != "2.0.0" {
		t.Errorf("got %v, want the freshly fetched list", got)
	}
	cached, _ := sdk.GetCachedVersions(sdk.Golang)
	if len(cached) != 2 {
		t.Errorf("the cache still holds %d versions; the refresh did not overwrite it", len(cached))
	}
}

// TestRefreshRemoteVersionsPropagatesFetchError is the error path. The failure
// must reach the caller unchanged: `svc versions --refresh` turns it into an
// exit code, and swallowing it would report an empty list as a success.
func TestRefreshRemoteVersionsPropagatesFetchError(t *testing.T) {
	upstream := errors.New("dial tcp: connection refused")
	fake := &fakeVersionFetcher{kind: sdk.Python, err: upstream}
	svc := newVersionsTestService(t, fake)

	if _, err := svc.RefreshRemoteVersions("python"); !errors.Is(err, upstream) {
		t.Errorf("RefreshRemoteVersions gave %v, want it to wrap %v", err, upstream)
	}
	if _, ok := sdk.GetCachedVersions(sdk.Python); ok {
		t.Error("a failed fetch populated the cache; the next run would print nothing as if it were a real list")
	}
}

// TestGetRemoteVersionsServesCacheWithoutFetching pins the cache-first half of
// the GUI contract, which the refactor into versionFetcher must not have
// changed: a cache hit returns immediately and only asks for a background
// refresh.
//
// The cooldown is consumed up front on purpose. refreshVersionsInBackground
// throttles through sdk.ShouldRefreshVersions, and a throttle that says "due"
// would start a goroutine whose fetch lands whenever it likes -- making the
// assertion below a race. Asking once first makes the second answer "not due"
// for the next RefreshCooldown, so the fetch count is deterministic.
func TestGetRemoteVersionsServesCacheWithoutFetching(t *testing.T) {
	cachedList := versionList("21.0.0", "20.0.0")
	sdk.SetCachedVersions(sdk.Rust, cachedList)
	if !sdk.ShouldRefreshVersions(sdk.Rust) {
		t.Fatal("the cooldown was already consumed by another test; this test needs a fresh one")
	}

	fake := &fakeVersionFetcher{kind: sdk.Rust, versions: versionList("99.0.0")}
	svc := newVersionsTestService(t, fake)

	got, err := svc.GetRemoteVersions("rust")
	if err != nil {
		t.Fatalf("GetRemoteVersions: %v", err)
	}
	if len(got) != 2 || got[0].Version != "21.0.0" {
		t.Errorf("got %v, want the cached list", got)
	}
	if fake.fetchCount() != 0 {
		t.Errorf("the fetcher ran %d times on a cache hit inside the cooldown, want 0", fake.fetchCount())
	}
}

// TestGetRemoteVersionsFetchesOnCacheMiss is the other half: a first run has to
// show a real list rather than an empty panel that fills in later, so a miss
// fetches synchronously. This is the path RefreshRemoteVersions now shares.
func TestGetRemoteVersionsFetchesOnCacheMiss(t *testing.T) {
	fake := &fakeVersionFetcher{kind: sdk.Dart, versions: versionList("3.5.0", "3.4.0")}
	svc := newVersionsTestService(t, fake)

	got, err := svc.GetRemoteVersions("dart")
	if err != nil {
		t.Fatalf("GetRemoteVersions: %v", err)
	}
	if fake.fetchCount() != 1 {
		t.Errorf("the fetcher ran %d times on a cache miss, want 1", fake.fetchCount())
	}
	if len(got) != 2 || got[0].Version != "3.5.0" {
		t.Errorf("got %v, want the fetched list", got)
	}
}

// TestGetRemoteVersionsPropagatesFetchErrorOnMiss keeps the two entry points
// honest about the same failure: a miss that cannot fetch is an error, not an
// empty list.
func TestGetRemoteVersionsPropagatesFetchErrorOnMiss(t *testing.T) {
	upstream := errors.New("403 rate limited")
	fake := &fakeVersionFetcher{kind: sdk.Ruby, err: upstream}
	svc := newVersionsTestService(t, fake)

	if _, err := svc.GetRemoteVersions("ruby"); !errors.Is(err, upstream) {
		t.Errorf("GetRemoteVersions gave %v, want it to wrap %v", err, upstream)
	}
}
