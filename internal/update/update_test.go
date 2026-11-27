package update

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"svc/internal/config"
	"svc/internal/detach"
	"svc/internal/downloader"
	"svc/internal/proxy"
)

// newTestUpdater wires an Updater against an httptest releases endpoint with
// no Wails runtime (CheckUpdate never emits events or quits).
func newTestUpdater(t *testing.T, sm *config.SettingsManager, updateURL string) *Updater {
	t.Helper()
	return NewUpdater(AppInfo{UpdateURL: updateURL}, sm, downloader.NewDownloader(), proxy.New(sm), nil)
}

func TestCheckUpdateSendsAuthorizationWhenTokenSet(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer ts.Close()

	sm := config.NewSettingsManager(t.TempDir())
	s := sm.Get()
	s.GitHubToken = base64.StdEncoding.EncodeToString([]byte("ghp_testtoken"))
	if err := sm.Update(s); err != nil {
		t.Fatalf("sm.Update: %v", err)
	}

	up := newTestUpdater(t, sm, ts.URL+"/releases/latest")
	if _, err := up.CheckUpdate(); err != nil {
		t.Fatalf("CheckUpdate returned error: %v", err)
	}
	if want := "Bearer ghp_testtoken"; gotAuth != want {
		t.Errorf("Authorization header = %q; want %q", gotAuth, want)
	}
}

func TestCheckUpdateOmitsAuthorizationWhenNoToken(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer ts.Close()

	sm := config.NewSettingsManager(t.TempDir())
	up := newTestUpdater(t, sm, ts.URL+"/releases/latest")
	if _, err := up.CheckUpdate(); err != nil {
		t.Fatalf("CheckUpdate returned error: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q; want empty (no token configured)", gotAuth)
	}
}

// TestSha256Matches pins the case-insensitive digest comparison used by
// DownloadUpdate's integrity check. sha256OfFile always returns lowercase
// while release manifests may publish uppercase hex; a case-sensitive
// comparison would wrongly reject valid downloads.
func TestSha256Matches(t *testing.T) {
	const lower = "deadbeefcafef00dba5eba11cafebabedeadbeefcafef00dba5eba11cafebabe"
	tests := []struct {
		name     string
		actual   string
		expected string
		want     bool
	}{
		{"identical lowercase", lower, lower, true},
		{"uppercase manifest vs lowercase computed", lower, "DEADBEEFCAFEF00DBA5EBA11CAFEBABEDEADBEEFCAFEF00DBA5EBA11CAFEBABE", true},
		{"mixed case both sides", "DeadBeef" + lower[8:], "dEADbEEF" + lower[8:], true},
		{"surrounding whitespace tolerated", lower + "\n", "  " + lower, true},
		{"different digest rejected", lower, "0000000000000000000000000000000000000000000000000000000000000000", false},
		{"prefix digest rejected", lower, lower[:63] + "0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sha256Matches(tt.actual, tt.expected); got != tt.want {
				t.Errorf("sha256Matches(%q, %q) = %v; want %v", tt.actual, tt.expected, got, tt.want)
			}
		})
	}
}

// TestSha256OfFile_roundTrip pins the helper that ApplyUpdate relies on for
// pre-copy hashing: same bytes -> same non-empty digest; different bytes ->
// different digest. ApplyUpdate feeds the digest into the platform script's
// post-copy check, so a regression here silently disables rollback.
// (Cross-platform; lives here so all three CI OSes run it.)
func TestSha256OfFile_roundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake_update.bin")
	payload := []byte("svc-update-payload-v1")
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}
	h1, err := sha256OfFile(path)
	if err != nil {
		t.Fatalf("sha256OfFile first call: %v", err)
	}
	if h1 == "" {
		t.Fatal("sha256OfFile returned empty digest")
	}
	h2, err := sha256OfFile(path)
	if err != nil {
		t.Fatalf("sha256OfFile second call: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("sha256OfFile not deterministic: %s then %s", h1, h2)
	}

	// Different bytes -> different digest.
	other := filepath.Join(dir, "other.bin")
	if err := os.WriteFile(other, []byte("different-bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	h3, err := sha256OfFile(other)
	if err != nil {
		t.Fatalf("sha256OfFile on other: %v", err)
	}
	if h1 == h3 {
		t.Fatalf("sha256OfFile collision between distinct payloads: %s", h1)
	}
}

// TestParseAppInfo covers valid JSON and the corrupt-payload fallback to the
// safe 0.1.0 defaults.
func TestParseAppInfo(t *testing.T) {
	info := ParseAppInfo([]byte(`{"version":"1.2.3","goVersion":"1.25","license":"MIT","repoUrl":"https://example.invalid/repo","updateUrl":"https://example.invalid/releases/latest"}`))
	if info.Version != "1.2.3" || info.UpdateURL != "https://example.invalid/releases/latest" {
		t.Errorf("ParseAppInfo = %+v; want version 1.2.3 and the configured update URL", info)
	}

	fallback := ParseAppInfo([]byte("{not json"))
	if fallback.Version != "0.1.0" {
		t.Errorf("ParseAppInfo(corrupt).Version = %q; want 0.1.0 fallback", fallback.Version)
	}
	if fb := ParseAppInfo(nil); fb.Version != "0.1.0" {
		t.Errorf("ParseAppInfo(nil).Version = %q; want 0.1.0 fallback", fb.Version)
	}
}

// TestFindBackupPath verifies the rollback backup lookup: it prefers the
// canonical <exe>.bak, and falls back to the newest *.bak in the directory
// when the canonical name is absent (the rename migration leaves the backup
// under the OLD executable name).
func TestFindBackupPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "svc")

	// No backup at all -> returns canonical path (caller then errors cleanly).
	if got := findBackupPath(exe); got != exe+".bak" {
		t.Errorf("no backup: got %q, want %q", got, exe+".bak")
	}

	// Canonical <exe>.bak present -> preferred.
	canonical := exe + ".bak"
	if err := os.WriteFile(canonical, []byte("c"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := findBackupPath(exe); got != canonical {
		t.Errorf("canonical present: got %q, want %q", got, canonical)
	}

	// Remove canonical; an old-named .bak remains -> fallback finds it.
	os.Remove(canonical)
	oldBak := filepath.Join(dir, "SDK Version Control.bak")
	if err := os.WriteFile(oldBak, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := findBackupPath(exe); got != oldBak {
		t.Errorf("old-named fallback: got %q, want %q", got, oldBak)
	}

	// Multiple .bak -> newest wins.
	newer := filepath.Join(dir, "zz.bak")
	if err := os.WriteFile(newer, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	// Ensure newer has a later modtime.
	future := time.Now().Add(10 * time.Minute)
	os.Chtimes(newer, future, future)
	if got := findBackupPath(exe); got != newer {
		t.Errorf("newest .bak: got %q, want %q", got, newer)
	}
}

// TestCleanStaleBackups verifies proactive cleanup of old-named backups while
// preserving a rollback source (promoting the newest old .bak to canonical
// when no canonical backup exists).
func TestCleanStaleBackups(t *testing.T) {
	t.Run("promotes newest old bak when canonical missing", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "svc")
		old1 := filepath.Join(dir, "SDK Version Control.bak")
		old2 := filepath.Join(dir, "zz.bak")
		for _, f := range []string{old1, old2} {
			if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		future := time.Now().Add(10 * time.Minute)
		os.Chtimes(old2, future, future)

		CleanStaleBackups(exe)

		// newest (old2) promoted to canonical; old1 removed.
		if _, err := os.Stat(exe + ".bak"); err != nil {
			t.Errorf("canonical backup not created from newest old bak: %v", err)
		}
		if _, err := os.Stat(old1); err == nil {
			t.Errorf("stale old bak %q not removed", old1)
		}
		if _, err := os.Stat(old2); err == nil {
			t.Errorf("promoted bak should no longer exist under old name")
		}
	})

	t.Run("removes old baks when canonical present", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "svc")
		canonical := exe + ".bak"
		old := filepath.Join(dir, "SDK Version Control.bak")
		for _, f := range []string{canonical, old} {
			if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		CleanStaleBackups(exe)
		if _, err := os.Stat(canonical); err != nil {
			t.Errorf("canonical backup should be kept: %v", err)
		}
		if _, err := os.Stat(old); err == nil {
			t.Errorf("stale old bak %q not removed", old)
		}
	})

	t.Run("no-op without backups", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "svc")
		CleanStaleBackups(exe) // must not panic or create anything
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Errorf("expected empty dir, got %d entries", len(entries))
		}
	})
}

// TestHasBackupForExe verifies the rollback-availability probe used by the
// frontend to disable the rollback button instead of surfacing a
// "no backup found" error on click.
func TestHasBackupForExe(t *testing.T) {
	t.Run("no backup", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "svc")
		if err := os.WriteFile(exe, []byte("x"), 0755); err != nil {
			t.Fatal(err)
		}
		if hasBackupForExe(exe) {
			t.Error("hasBackupForExe = true; want false with no .bak present")
		}
	})

	t.Run("canonical backup present", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "svc")
		if err := os.WriteFile(exe+".bak", []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if !hasBackupForExe(exe) {
			t.Error("hasBackupForExe = false; want true with canonical .bak")
		}
	})

	t.Run("old-named backup counts", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "svc")
		if err := os.WriteFile(filepath.Join(dir, "SDK Version Control.bak"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if !hasBackupForExe(exe) {
			t.Error("hasBackupForExe = false; want true with an old-named .bak")
		}
	})

	t.Run("directory named .bak does not count", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "svc")
		if err := os.MkdirAll(exe+".bak", 0755); err != nil {
			t.Fatal(err)
		}
		if hasBackupForExe(exe) {
			t.Error("hasBackupForExe = true; want false when the .bak is a directory")
		}
	})

	t.Run("empty exe path", func(t *testing.T) {
		if hasBackupForExe("") {
			t.Error("hasBackupForExe(\"\") = true; want false")
		}
	})
}

// TestStartSwapScriptChoosesTheSpawnTheCallerAskedFor pins the dispatch
// ApplyOptions.Relaunch controls.
//
// Nothing about the choice is visible in a returned error, and both branches
// end up with the script running, so the assertion has to be about which
// function was reached. The difference is the one the CLI contract is built
// on: the relaunching branch inherits this process's descriptors and starts a
// child that reopens the window, and the other sends the child's stdio to the
// null device and reaps it. A test that launched a real script and only
// checked for nil would pass either way.
func TestStartSwapScriptChoosesTheSpawnTheCallerAskedFor(t *testing.T) {
	exe, argv := trivialCommand()

	var detached []detach.Options
	var detachedArgs []string
	originalSpawn, originalInherited := detachedSpawn, startInherited
	detachedSpawn = func(gotExe string, gotArgv []string, opts detach.Options) error {
		if gotExe != exe {
			t.Errorf("detach received exe %q, want %q", gotExe, exe)
		}
		detachedArgs = gotArgv
		detached = append(detached, opts)
		return nil
	}
	inherited := 0
	startInherited = func(gotExe string, gotArgv []string) error {
		inherited++
		return originalInherited(gotExe, gotArgv)
	}
	t.Cleanup(func() { detachedSpawn, startInherited = originalSpawn, originalInherited })

	// The CLI's shape: no relaunch, so the script goes out through detach with
	// its stdio on the null device -- an empty LogFile is that promise, and a
	// log file here would mean a swap the user asked for from a terminal was
	// appending to svc's diagnostics.
	if err := startSwapScript(exe, argv, ApplyOptions{}); err != nil {
		t.Fatalf("startSwapScript without relaunch: %v", err)
	}
	if inherited != 0 {
		t.Errorf("the no-relaunch branch reached the inherited-stdio spawn %d times", inherited)
	}
	if len(detached) != 1 {
		t.Fatalf("the no-relaunch branch spawned %d times, want exactly one", len(detached))
	}
	if detached[0].LogFile != "" {
		t.Errorf("the detached swap script logs to %q, want the null device", detached[0].LogFile)
	}
	if detached[0].Dir != os.TempDir() {
		t.Errorf("the detached swap script runs in %q, want %q", detached[0].Dir, os.TempDir())
	}
	if len(detachedArgs) != len(argv) {
		t.Errorf("the detached swap script got argv %v, want %v", detachedArgs, argv)
	}

	// The GUI's shape: the pre-existing path, unchanged. It really starts the
	// child, which is why the command is one that exits immediately.
	detached, detachedArgs = nil, nil
	if err := startSwapScript(exe, argv, ApplyOptions{Relaunch: true}); err != nil {
		t.Fatalf("startSwapScript with relaunch: %v", err)
	}
	if inherited != 1 {
		t.Errorf("the relaunching branch reached the inherited-stdio spawn %d times, want 1", inherited)
	}
	if len(detached) != 0 {
		t.Errorf("the relaunching branch went through detach %d times; the GUI's script must inherit stdio and relaunch the binary", len(detached))
	}
}

// TestApplyOptionsDefaultsToNoRelaunch pins the zero value: a caller that
// builds ApplyOptions{} gets the CLI's behaviour, not the GUI's. Every existing
// call site that wants a relaunch says so explicitly, which is what makes
// "a new shell forgot to ask" fail safe rather than open a window.
func TestApplyOptionsDefaultsToNoRelaunch(t *testing.T) {
	if (ApplyOptions{}).Relaunch {
		t.Error("the zero ApplyOptions relaunches; a caller that did not ask for a window must not get one")
	}
}
