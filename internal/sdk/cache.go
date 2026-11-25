package sdk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"svc/internal/logger"
)

// versionCache is a two-tier (in-memory + on-disk) cache of remote version
// lists keyed by SDK type. It makes the "refresh versions" UX feel instant:
// the frontend always gets a list back immediately (from memory, or from the
// on-disk cache after an app restart), while the backend refreshes in the
// background and pushes an event when a fresher list lands.
//
// On-disk cache survives restarts, so even the first launch after a restart
// can show a stale-but-useful list instead of a spinner. No TTL is enforced --
// a stale cache is strictly better than nothing, and every explicit "refresh"
// click triggers a background fetch that overwrites the cache.
//
// The on-disk root dir is set once via InitVersionCacheDir before first use;
// until then the cache is memory-only (calls degrade gracefully).
type versionCache struct {
	mu      sync.RWMutex
	entries map[SdkType]versionCacheEntry
	dir     string
}

type versionCacheEntry struct {
	Versions  []VersionInfo `json:"versions"`
	FetchedAt time.Time     `json:"fetched_at"`
}

var globalVersionCache = &versionCache{entries: make(map[SdkType]versionCacheEntry)}

// InitVersionCacheDir sets the on-disk root directory for the version cache.
// Must be called once at startup (after the config/data dir is known). Safe to
// call multiple times; the latest dir wins. If dir is empty, the cache stays
// memory-only.
func InitVersionCacheDir(dir string) {
	globalVersionCache.mu.Lock()
	defer globalVersionCache.mu.Unlock()
	globalVersionCache.dir = dir
}

func (c *versionCache) cacheFile(t SdkType) string {
	// dir is written by InitVersionCacheDir under the write lock; read it
	// under the read lock so concurrent initialization cannot race.
	c.mu.RLock()
	dir := c.dir
	c.mu.RUnlock()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "versions_"+string(t)+".json")
}

// GetCachedVersions returns the cached version list for sdkType and whether a
// cache entry exists. It checks memory first; on a memory miss it falls back
// to the on-disk JSON file (so a freshly restarted app still serves the last
// known list). The returned slice is a defensive copy.
func GetCachedVersions(t SdkType) ([]VersionInfo, bool) {
	globalVersionCache.mu.RLock()
	e, ok := globalVersionCache.entries[t]
	globalVersionCache.mu.RUnlock()
	if ok {
		return copyVersions(e.Versions), true
	}
	// Memory miss: try the on-disk file (populates memory as a side effect).
	return globalVersionCache.loadFromDisk(t)
}

// loadFromDisk reads the on-disk cache file for t. On success it also stores
// the entry in memory (so subsequent reads skip the disk) and returns the list.
// H5: Re-checks memory after acquiring the write lock to prevent TOCTOU:
// a concurrent SetCachedVersions might have populated the in-memory entry
// between the RUnlock in GetCachedVersions and the Lock here; in that case
// the fresher in-memory entry wins over the stale disk data.
func (c *versionCache) loadFromDisk(t SdkType) ([]VersionInfo, bool) {
	path := c.cacheFile(t)
	if path == "" {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var e versionCacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, false
	}
	c.mu.Lock()
	// Re-check memory: a concurrent SetCachedVersions might have populated it.
	if memEntry, ok := c.entries[t]; ok {
		c.mu.Unlock()
		return copyVersions(memEntry.Versions), true
	}
	c.entries[t] = e
	c.mu.Unlock()
	return copyVersions(e.Versions), true
}

// SetCachedVersions stores versions for sdkType, replacing any existing entry.
// It writes to both memory and disk (so the cache survives a restart). The
// slice is copied so later mutation by the caller does not affect the cache.
// A disk write failure is logged but does not affect the in-memory cache --
// the next refresh will retry the write.
//
// An EMPTY list is never cached: an empty result is almost always a fetch
// anomaly (upstream hiccup, parse failure, rate limit returning no entries),
// and overwriting the last good list with nothing would leave the UI empty
// until the next successful refresh -- both in memory and on disk.
func SetCachedVersions(t SdkType, versions []VersionInfo) {
	if len(versions) == 0 {
		return
	}
	e := versionCacheEntry{Versions: copyVersions(versions), FetchedAt: time.Now()}
	globalVersionCache.mu.Lock()
	globalVersionCache.entries[t] = e
	globalVersionCache.mu.Unlock()
	path := globalVersionCache.cacheFile(t)
	if path == "" {
		return
	}
	// Best-effort persist; a write failure only means the next restart starts
	// from the previous on-disk cache, which is acceptable.
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	if data, err := json.Marshal(e); err == nil {
		// M3: atomic write via temp file + os.Rename so a partial write
		// doesn't leave a corrupt cache file on disk.
		tmpPath := path + ".tmp"
		if werr := os.WriteFile(tmpPath, data, 0644); werr == nil {
			_ = os.Rename(tmpPath, path)
		}
	}
}

func copyVersions(in []VersionInfo) []VersionInfo {
	if in == nil {
		return nil
	}
	out := make([]VersionInfo, len(in))
	copy(out, in)
	return out
}

// RefreshCooldown throttles background version refreshes per SDK. Exported so
// the GUI's in-process refresh and the CLI's detached refresh subprocess can
// never disagree about the window.
const RefreshCooldown = 30 * time.Second

// refreshState is the in-process half of the throttle. Every cache hit in
// GetRemoteVersions triggers a background refresh; without throttling, rapidly
// switching SDK panels (py -> rust -> py -> ...) would spawn a goroutine per
// switch, each emitting an "install:versions-refreshed" event. That floods the
// frontend with events and amplifies any stale-listener timing issue into
// visible list corruption. A 30s cooldown per SDK keeps the list fresh enough
// for normal use while capping the goroutine/event rate.
//
// It cannot be the only half. The CLI performs its background refresh in a
// detached subprocess, so a purely in-memory cooldown is per-process and would
// throttle nothing at all across runs. The stamp file below is the cross-process
// authority; this map is the fallback for callers that never ran
// InitVersionCacheDir, and a secondary guard for when a stamp cannot be written.
var refreshState = struct {
	sync.Mutex
	last map[SdkType]time.Time
}{last: make(map[SdkType]time.Time)}

// ShouldRefreshVersions reports whether a background refresh should be started
// for t now. It returns false when t was refreshed within RefreshCooldown (so
// rapid re-entry is a no-op), and records the time when it returns true.
//
// The timestamp is persisted as the mtime of <cacheDir>/refresh-<sdk>.stamp, so
// the GUI and the CLI throttle each other. Callers that could race on this must
// serialise externally -- the CLI's refresh subprocess takes a single-flight
// lock before doing any network work. The check-then-stamp window here is fine
// for throttling, but it is not a mutex.
func ShouldRefreshVersions(t SdkType) bool {
	now := time.Now()
	if dir := versionCacheDir(); dir != "" {
		return shouldRefreshStamped(t, dir, now)
	}
	// No cache dir configured (tests, or a caller that asks before startup has
	// finished): keep the original in-process behaviour exactly.
	return shouldRefreshInMemory(t, now)
}

// shouldRefreshInMemory is the pre-stamp behaviour, kept for the no-cache-dir
// case.
func shouldRefreshInMemory(t SdkType, now time.Time) bool {
	refreshState.Lock()
	defer refreshState.Unlock()
	last, ok := refreshState.last[t]
	if !refreshDue(last, now, RefreshCooldown, ok) {
		return false
	}
	refreshState.last[t] = now
	return true
}

// shouldRefreshStamped applies the cooldown across processes, using whichever is
// more recent: the stamp file's mtime or this process's own record.
func shouldRefreshStamped(t SdkType, dir string, now time.Time) bool {
	path := refreshStampPath(dir, t)
	last, haveLast := stampTime(path)
	if memLast, ok := lastRefreshInMemory(t); ok && (!haveLast || memLast.After(last)) {
		last, haveLast = memLast, true
	}
	if !refreshDue(last, now, RefreshCooldown, haveLast) {
		return false
	}

	rememberRefresh(t, now)
	if err := touchStamp(path, now); err != nil {
		// Neither fatal nor a reason to give up on throttling: the in-memory
		// record above already covers this process, and an unwritable cache dir
		// must not turn into "refresh on every single call".
		logger.Warn("Cannot write the version-refresh stamp %s: %v", path, err)
	}
	return true
}

// refreshDue is the cooldown decision. Pure, so the whole policy -- including
// the exactly-at-the-boundary and clock-went-backwards edges -- is testable
// without a filesystem or a real clock.
func refreshDue(last time.Time, now time.Time, cooldown time.Duration, haveLast bool) bool {
	if !haveLast {
		return true
	}
	// >= rather than >: the original code refused only while
	// time.Since(last) < cooldown, so a refresh exactly at the boundary is due.
	// A stamp from the future (the clock moved back) yields a negative elapsed
	// time, which is less than any positive cooldown, so it correctly waits.
	return now.Sub(last) >= cooldown
}

// refreshStampPath is the file whose mtime records when t was last refreshed.
// Pure, so the layout is testable without touching the filesystem.
func refreshStampPath(dir string, t SdkType) string {
	return filepath.Join(dir, "refresh-"+string(t)+".stamp")
}

// stampTime reads a stamp's mtime. Anything that is not a readable regular file
// -- missing, a directory sitting on the path, unreadable -- counts as "never
// refreshed". Failing open towards refreshing is deliberate: a first run must
// always get a real version list, and a corrupt stamp must never wedge the
// version list into permanent staleness.
func stampTime(path string) (time.Time, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// touchStamp records now as the refresh time. The mtime is set explicitly
// because opening an existing file O_WRONLY does not update it, and the mtime IS
// the value: the stamp's contents are deliberately empty so that `ls -l
// ~/.svc/cache` on its own tells you when each SDK was last refreshed.
func touchStamp(path string, now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chtimes(path, now, now)
}

// versionCacheDir returns the dir InitVersionCacheDir was pointed at, or "" while
// the cache is still memory-only.
func versionCacheDir() string {
	globalVersionCache.mu.RLock()
	defer globalVersionCache.mu.RUnlock()
	return globalVersionCache.dir
}

// lastRefreshInMemory reads this process's own record for t.
func lastRefreshInMemory(t SdkType) (time.Time, bool) {
	refreshState.Lock()
	defer refreshState.Unlock()
	last, ok := refreshState.last[t]
	return last, ok
}

// rememberRefresh records now against t in this process.
func rememberRefresh(t SdkType, now time.Time) {
	refreshState.Lock()
	defer refreshState.Unlock()
	refreshState.last[t] = now
}

// LookupCachedDownloadURL returns the cached download URL and filename for the
// given SDK type + version, or ("", "", false) when not cached. It lets
// GetDownloadURL skip a fresh FetchRemoteVersions round-trip when the version
// list was already fetched (e.g. the user just opened the version panel, which
// populated the cache, then clicked Install on one of the listed versions).
//
// On a cache miss the caller MUST fall back to FetchRemoteVersions -- a cache
// miss is the normal first-install path and is not an error.
func LookupCachedDownloadURL(t SdkType, version string) (string, string, bool) {
	versions, ok := GetCachedVersions(t)
	if !ok {
		return "", "", false
	}
	for _, v := range versions {
		if v.Version == version {
			return v.DownloadURL, v.FileName, true
		}
	}
	return "", "", false
}
