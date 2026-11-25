package sdk

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// resetRefreshThrottle clears both halves of the throttle and points the version
// cache at dir, restoring the previous values on cleanup. Both are package
// globals, so tests using this helper must not run in parallel.
func resetRefreshThrottle(t *testing.T, dir string) {
	t.Helper()
	previousDir := versionCacheDir()
	refreshState.Lock()
	previousLast := refreshState.last
	refreshState.last = make(map[SdkType]time.Time)
	refreshState.Unlock()

	InitVersionCacheDir(dir)
	t.Cleanup(func() {
		refreshState.Lock()
		refreshState.last = previousLast
		refreshState.Unlock()
		InitVersionCacheDir(previousDir)
	})
}

// forgetInMemoryRefresh drops this process's half of the throttle, leaving only
// what a DIFFERENT process could see. That is the whole point of the stamp file,
// so most of the tests below use it to prove they are not passing on in-memory
// state alone.
func forgetInMemoryRefresh() {
	refreshState.Lock()
	refreshState.last = make(map[SdkType]time.Time)
	refreshState.Unlock()
}

func TestRefreshDue(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		last     time.Time
		haveLast bool
		cooldown time.Duration
		want     bool
	}{
		{"never refreshed is due", time.Time{}, false, RefreshCooldown, true},
		{"just refreshed is not due", now.Add(-time.Second), true, RefreshCooldown, false},
		{"inside the window is not due", now.Add(-29 * time.Second), true, RefreshCooldown, false},
		{"exactly at the boundary is due", now.Add(-RefreshCooldown), true, RefreshCooldown, true},
		{"past the window is due", now.Add(-RefreshCooldown - time.Second), true, RefreshCooldown, true},
		{"zero cooldown is always due", now, true, 0, true},
		{"stamp from the future waits", now.Add(time.Minute), true, RefreshCooldown, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := refreshDue(c.last, now, c.cooldown, c.haveLast); got != c.want {
				t.Errorf("refreshDue(last=%v, haveLast=%v, cooldown=%v) = %v, want %v",
					c.last, c.haveLast, c.cooldown, got, c.want)
			}
		})
	}
}

// TestRefreshCooldownIsThirtySeconds pins the value both shells promise. A
// silent change here would desynchronise the GUI's in-process refresh from the
// CLI's detached one.
func TestRefreshCooldownIsThirtySeconds(t *testing.T) {
	if RefreshCooldown != 30*time.Second {
		t.Errorf("RefreshCooldown = %v, want 30s", RefreshCooldown)
	}
}

func TestRefreshStampPath(t *testing.T) {
	got := refreshStampPath(filepath.Join("home", "cache"), Rust)
	want := filepath.Join("home", "cache", "refresh-rust.stamp")
	if got != want {
		t.Errorf("refreshStampPath = %q, want %q", got, want)
	}
}

func TestTouchStampAndStampTime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache") // does not exist yet
	stamp := refreshStampPath(dir, Rust)
	when := time.Now().Add(-2 * time.Minute).Truncate(time.Second)

	if _, ok := stampTime(stamp); ok {
		t.Error("stampTime reported a stamp that was never written")
	}
	if err := touchStamp(stamp, when); err != nil {
		t.Fatalf("touchStamp: %v", err)
	}
	got, ok := stampTime(stamp)
	if !ok {
		t.Fatal("stampTime could not read the stamp touchStamp just wrote")
	}
	// Some filesystems store mtime with reduced precision, so compare with a
	// tolerance rather than for exact equality.
	if diff := got.Sub(when); diff > time.Second || diff < -time.Second {
		t.Errorf("stamp mtime = %v, want %v (touchStamp must set the mtime explicitly)", got, when)
	}

	// Touching an existing stamp must MOVE the mtime: O_WRONLY on an existing
	// file does not update it, and the mtime is the entire value.
	later := time.Now()
	if err := touchStamp(stamp, later); err != nil {
		t.Fatalf("second touchStamp: %v", err)
	}
	got, _ = stampTime(stamp)
	if got.Before(when.Add(time.Second)) {
		t.Errorf("re-touching left the old mtime %v, want ~%v", got, later)
	}
}

func TestShouldRefreshVersionsWithoutCacheDirStaysInMemory(t *testing.T) {
	resetRefreshThrottle(t, "")

	if !ShouldRefreshVersions(Rust) {
		t.Error("the first call must allow a refresh")
	}
	if ShouldRefreshVersions(Rust) {
		t.Error("an immediate second call must be throttled")
	}
	if !ShouldRefreshVersions(NodeJS) {
		t.Error("a different SDK must not inherit another SDK's cooldown")
	}
	// No dir means nowhere to stamp, and the fallback must not invent one.
	if got := versionCacheDir(); got != "" {
		t.Errorf("versionCacheDir() = %q, want empty", got)
	}
}

// TestShouldRefreshVersionsPersistsAcrossProcesses is the regression this change
// exists for: with the old in-memory cooldown, every new process thought it was
// the first one, so a script calling `svc versions rust` twenty times performed
// twenty network refreshes.
func TestShouldRefreshVersionsPersistsAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	resetRefreshThrottle(t, dir)

	if !ShouldRefreshVersions(Rust) {
		t.Fatal("the first call must allow a refresh")
	}
	stamp := refreshStampPath(dir, Rust)
	if _, err := os.Stat(stamp); err != nil {
		t.Fatalf("no stamp was written at %s: %v", stamp, err)
	}
	if ShouldRefreshVersions(Rust) {
		t.Error("an immediate second call in the same process must be throttled")
	}

	forgetInMemoryRefresh()
	if ShouldRefreshVersions(Rust) {
		t.Error("a fresh process must be throttled by the stamp file, not by in-memory state")
	}
}

func TestShouldRefreshVersionsHonoursAForeignStamp(t *testing.T) {
	dir := t.TempDir()
	resetRefreshThrottle(t, dir)

	// A stamp written by somebody else -- the GUI, or a previous CLI run.
	if err := touchStamp(refreshStampPath(dir, Rust), time.Now()); err != nil {
		t.Fatalf("touchStamp: %v", err)
	}
	if ShouldRefreshVersions(Rust) {
		t.Error("a stamp written by another process must throttle this one")
	}
	// A different SDK is unaffected.
	if !ShouldRefreshVersions(NodeJS) {
		t.Error("rust's stamp must not throttle nodejs")
	}
}

func TestShouldRefreshVersionsExpiresWithTheStamp(t *testing.T) {
	dir := t.TempDir()
	resetRefreshThrottle(t, dir)

	if !ShouldRefreshVersions(Rust) {
		t.Fatal("the first call must allow a refresh")
	}
	stamp := refreshStampPath(dir, Rust)
	expired := time.Now().Add(-RefreshCooldown - time.Minute)
	if err := os.Chtimes(stamp, expired, expired); err != nil {
		t.Fatalf("backdate the stamp: %v", err)
	}
	forgetInMemoryRefresh()

	if !ShouldRefreshVersions(Rust) {
		t.Error("a stamp older than the cooldown must allow a refresh")
	}
	// ...and the decision must have re-stamped, so the next process waits too.
	info, err := os.Stat(stamp)
	if err != nil {
		t.Fatalf("stat the stamp: %v", err)
	}
	if age := time.Since(info.ModTime()); age > time.Minute {
		t.Errorf("the stamp still reads %v old; ShouldRefreshVersions must refresh it", age)
	}
	forgetInMemoryRefresh()
	if ShouldRefreshVersions(Rust) {
		t.Error("the re-stamped cooldown was not honoured")
	}
}

// TestShouldRefreshVersionsSurvivesAnUnwritableCacheDir covers the error path:
// a cache dir that cannot hold a stamp must neither panic nor wedge, and this
// process must still throttle itself.
func TestShouldRefreshVersionsSurvivesAnUnwritableCacheDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	resetRefreshThrottle(t, filepath.Join(blocker, "cache"))

	if !ShouldRefreshVersions(Rust) {
		t.Error("the first call must still allow a refresh when the stamp cannot be written")
	}
	if ShouldRefreshVersions(Rust) {
		t.Error("the in-memory half must still throttle this process when stamping fails")
	}
}

// TestShouldRefreshVersionsIgnoresACorruptStamp covers a stamp that exists but
// cannot be stat'd as a file (a directory in its place): it must count as "never
// refreshed" rather than blocking refreshes forever.
func TestShouldRefreshVersionsIgnoresACorruptStamp(t *testing.T) {
	dir := t.TempDir()
	resetRefreshThrottle(t, dir)
	if err := os.MkdirAll(refreshStampPath(dir, Rust), 0755); err != nil {
		t.Fatal(err)
	}
	if !ShouldRefreshVersions(Rust) {
		t.Error("an unreadable stamp must count as never refreshed")
	}
}
