//go:build !windows

package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestWriteTempScript_UnpredictableNameAndExecutable pins the /tmp hardening:
// update/rollback scripts must be created via os.CreateTemp (O_CREATE|O_EXCL,
// random suffix, never follows pre-planted symlinks) instead of the old fixed
// world-guessable names svc_updater.sh / svc_rollback.sh, and must end up
// executable (0700) so /bin/sh can run them.
func TestWriteTempScript_UnpredictableNameAndExecutable(t *testing.T) {
	const content = "#!/bin/sh\necho hello\n"

	p1, err := writeTempScript("svc_updater_*.sh", content)
	if err != nil {
		t.Fatalf("writeTempScript first call: %v", err)
	}
	defer os.Remove(p1)
	p2, err := writeTempScript("svc_updater_*.sh", content)
	if err != nil {
		t.Fatalf("writeTempScript second call: %v", err)
	}
	defer os.Remove(p2)

	if p1 == p2 {
		t.Fatalf("writeTempScript returned the same path twice (%s): names must be unpredictable", p1)
	}
	for _, p := range []string{p1, p2} {
		base := filepath.Base(p)
		if !strings.HasPrefix(base, "svc_updater_") || !strings.HasSuffix(base, ".sh") {
			t.Errorf("script path %q does not match the svc_updater_*.sh pattern", p)
		}
		// The random suffix between prefix and ".sh" must be non-empty.
		if base == "svc_updater_.sh" {
			t.Errorf("script path %q has no random suffix", p)
		}
		// Must NOT be one of the old fixed guessable names.
		if base == "svc_updater.sh" || base == "svc_rollback.sh" {
			t.Errorf("script path %q is the old fixed name", p)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %q: %v", p, err)
		}
		if info.Mode().Perm() != 0700 {
			t.Errorf("script %q perm = %v; want 0700 (owner-executable)", p, info.Mode().Perm())
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %q: %v", p, err)
		}
		if string(got) != content {
			t.Errorf("script %q content = %q; want %q", p, got, content)
		}
	}
}

// TestWriteTempScript_RejectsSymlinkOccupiedName verifies the O_EXCL guarantee
// indirectly: CreateTemp never reuses an existing path, so even if an attacker
// pre-creates a file at a guessed name, the script lands elsewhere.
func TestWriteTempScript_NeverCollidesWithExistingFile(t *testing.T) {
	// Occupy many candidate names is impractical; instead assert the returned
	// path never equals a pre-existing file we control.
	pre := filepath.Join(os.TempDir(), "svc_updater_preexisting.sh")
	if err := os.WriteFile(pre, []byte("attacker"), 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(pre)

	p, err := writeTempScript("svc_updater_*.sh", "#!/bin/sh\n")
	if err != nil {
		t.Fatalf("writeTempScript: %v", err)
	}
	defer os.Remove(p)
	if p == pre {
		t.Fatalf("writeTempScript reused the pre-existing path %s", pre)
	}
	got, err := os.ReadFile(pre)
	if err != nil || string(got) != "attacker" {
		t.Fatalf("pre-existing file was clobbered: content=%q err=%v", got, err)
	}
}

// TestBuildUnixUpdateScript pins the shell logic ApplyUpdate relies on: wait
// for the app to exit by exact PID, back up via mv, replace via mv, verify
// the post-copy SHA256 against the embedded pre-copy hash, and restore the
// backup on mismatch.
func TestBuildUnixUpdateScript(t *testing.T) {
	const hash = "deadbeefcafef00dba5eba11cafebabedeadbeefcafef00dba5eba11cafebabe"
	const pid = 4242
	const currentExe = "/opt/svc/svc"
	const bak = "/opt/svc/svc.bak"
	const newExe = "/tmp/svc_update_payload_123456"
	script := buildUnixUpdateScript(pid, currentExe, bak, newExe, hash)

	checks := []struct {
		name string
		want string
	}{
		{"embeds expected hash", "expected_hash='" + hash + "'"},
		{"waits on exact PID", "while kill -0 4242 2>/dev/null; do"},
		{"has wait timeout", `if [ "$timeout" -le 0 ]; then`},
		{"cleans stale backups before backup", `echo "Cleaning stale backups..."`},
		{"skips the current bak when cleaning", `[ "$oldbak" = '/opt/svc/svc.bak' ] && continue`},
		{"removes old-named bak files", `rm -f "$oldbak"`},
		{"backs up via mv", "mv -f '/opt/svc/svc' '/opt/svc/svc.bak'"},
		{"replaces via mv of payload", "mv -f '/tmp/svc_update_payload_123456' '/opt/svc/svc'"},
		{"makes result executable", "chmod +x '/opt/svc/svc'"},
		{"verifies sha256 post-copy", "sha256sum '/opt/svc/svc'"},
		{"compares hashes", `if [ "$actual_hash" != "$expected_hash" ]; then`},
		{"restores backup on mismatch", "mv -f '/opt/svc/svc.bak' '/opt/svc/svc'"},
		{"relaunches new binary", "nohup '/opt/svc/svc' > /dev/null 2>&1 &"},
		{"self-deletes", `rm -f "$0"`},
	}
	for _, c := range checks {
		if !strings.Contains(script, c.want) {
			t.Errorf("buildUnixUpdateScript missing %q\n--- script ---\n%s", c.name, script)
		}
	}
}

// TestBuildUnixUpdateScript_QuotesHostilePaths ensures paths with spaces and
// shell metacharacters are single-quoted so the script cannot be broken or
// injected via the install path.
func TestBuildUnixUpdateScript_QuotesHostilePaths(t *testing.T) {
	exe := `/opt/svc dir/with"space$/svc`
	script := buildUnixUpdateScript(1, exe, exe+".bak", "/tmp/payload", "abc")
	want := shellQuote(exe)
	if !strings.Contains(script, want) {
		t.Errorf("buildUnixUpdateScript does not shell-quote hostile path; want substring %q\n--- script ---\n%s", want, script)
	}
}

// TestBuildUnixRollbackScript pins the rollback shell logic: wait by exact
// PID, restore .bak over the current binary, chmod +x, relaunch.
func TestBuildUnixRollbackScript(t *testing.T) {
	const pid = 4242
	const currentExe = "/opt/svc/svc"
	const bak = "/opt/svc/svc.bak"
	script := buildUnixRollbackScript(pid, bak, currentExe)

	checks := []struct {
		name string
		want string
	}{
		{"waits on exact PID", "while kill -0 4242 2>/dev/null; do"},
		{"restores bak via mv", "mv -f '/opt/svc/svc.bak' '/opt/svc/svc'"},
		{"makes result executable", "chmod +x '/opt/svc/svc'"},
		{"relaunches restored binary", "nohup '/opt/svc/svc' > /dev/null 2>&1 &"},
		{"self-deletes", `rm -f "$0"`},
	}
	for _, c := range checks {
		if !strings.Contains(script, c.want) {
			t.Errorf("buildUnixRollbackScript missing %q\n--- script ---\n%s", c.name, script)
		}
	}
}

// TestSecureUpdatePayload verifies the downloaded payload is moved off the
// fixed guessable name to an unpredictable CreateTemp-based path with its
// content intact.
func TestSecureUpdatePayload(t *testing.T) {
	fixed := filepath.Join(t.TempDir(), "svc_update_new")
	payload := []byte("update-payload-bytes")
	if err := os.WriteFile(fixed, payload, 0644); err != nil {
		t.Fatal(err)
	}

	secure, err := secureUpdatePayload(fixed)
	if err != nil {
		t.Fatalf("secureUpdatePayload: %v", err)
	}
	defer os.Remove(secure)

	base := filepath.Base(secure)
	if !strings.HasPrefix(base, "svc_update_payload_") {
		t.Errorf("secure payload path %q does not use the svc_update_payload_* pattern", secure)
	}
	if base == "svc_update_payload_" {
		t.Errorf("secure payload path %q has no random suffix", secure)
	}
	if secure == fixed {
		t.Fatalf("payload still at the fixed guessable path %s", fixed)
	}
	if _, err := os.Stat(fixed); !os.IsNotExist(err) {
		t.Errorf("fixed payload path still exists after securing (stat err = %v)", err)
	}
	got, err := os.ReadFile(secure)
	if err != nil {
		t.Fatalf("read secured payload: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("secured payload content = %q; want %q", got, payload)
	}
}

// TestSecureUpdatePayload_MissingFile covers the error path: securing a
// payload that was never downloaded must fail cleanly.
func TestSecureUpdatePayload_MissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does_not_exist")
	secure, err := secureUpdatePayload(missing)
	if err == nil {
		os.Remove(secure)
		t.Fatal("secureUpdatePayload should fail when the payload does not exist")
	}
}

// --- macOS bundle version sync ---

// writeFakeSvcBinary writes an executable that answers `version` with
// "svc <ver>", standing in for the real binary so the tests need no build.
func writeFakeSvcBinary(t *testing.T, path, ver string) {
	t.Helper()
	body := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'svc " + ver + "'; fi\n"
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}

func runSh(t *testing.T, snippet string) {
	t.Helper()
	out, err := exec.Command("/bin/sh", "-c", snippet).CombinedOutput()
	if err != nil {
		t.Fatalf("sh -c failed: %v\n--- output ---\n%s", err, out)
	}
}

func plistValue(t *testing.T, plist, key string) string {
	t.Helper()
	out, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :"+key, plist).Output()
	if err != nil {
		t.Fatalf("PlistBuddy Print :%s: %v", key, err)
	}
	return strings.TrimSpace(string(out))
}

// fakeAppBundle builds <dir>/svc.app/Contents/{MacOS/svc,Info.plist} with both
// version keys still at the version the "dmg" shipped, and returns the paths
// of the binary and the plist.
func fakeAppBundle(t *testing.T, staleVersion string) (exe, plist string) {
	t.Helper()
	macOS := filepath.Join(t.TempDir(), "svc.app", "Contents", "MacOS")
	if err := os.MkdirAll(macOS, 0755); err != nil {
		t.Fatal(err)
	}
	exe = filepath.Join(macOS, "svc")
	writeFakeSvcBinary(t, exe, "9.9.9")
	plist = filepath.Join(macOS, "..", "Info.plist")
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>svc</string>
    <key>CFBundleShortVersionString</key>
    <string>` + staleVersion + `</string>
    <key>CFBundleVersion</key>
    <string>` + staleVersion + `</string>
</dict>
</plist>
`
	if err := os.WriteFile(plist, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return exe, plist
}

// TestBuildBundleVersionSync pins the shape of the snippet: the version is read
// back from the binary that just landed (never interpolated from Go), both
// bundle keys are stamped, and the value is charset-checked before it reaches
// PlistBuddy. The plist and PlistBuddy guards are what make it a no-op on
// Linux, where the installed binary has no bundle around it.
func TestBuildBundleVersionSync(t *testing.T) {
	snippet := buildBundleVersionSync(shellQuote("/Applications/svc.app/Contents/MacOS/svc"))
	for _, want := range []string{
		`plist="$(dirname '/Applications/svc.app/Contents/MacOS/svc')/../Info.plist"`,
		`[ -f "$plist" ]`,
		`[ -x /usr/libexec/PlistBuddy ]`,
		`'/Applications/svc.app/Contents/MacOS/svc' version`,
		"CFBundleShortVersionString",
		"CFBundleVersion",
		`*[!A-Za-z0-9._+-]*) new_ver=""`,
	} {
		if !strings.Contains(snippet, want) {
			t.Errorf("buildBundleVersionSync missing %q\n--- snippet ---\n%s", want, snippet)
		}
	}
}

// TestBuildBundleVersionSync_QuotesHostilePaths: the exe path is interpolated
// only through the caller's shellQuote, so a path with spaces and quotes stays
// a single argument.
func TestBuildBundleVersionSync_QuotesHostilePaths(t *testing.T) {
	hostile := `/tmp/o'halloran/svc app/Contents/MacOS/svc`
	snippet := buildBundleVersionSync(shellQuote(hostile))
	want := shellQuote(hostile) + " version"
	if !strings.Contains(snippet, want) {
		t.Errorf("snippet does not quote the hostile path; want substring %q\n--- snippet ---\n%s", want, snippet)
	}
}

// TestBundleVersionSync_NoOpOutsideAppBundle runs on every Unix CI host: with
// no Info.plist next to the binary's parent the snippet must exit 0 and touch
// nothing, which is what keeps the shared Linux update path unaffected.
func TestBundleVersionSync_NoOpOutsideAppBundle(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "svc")
	writeFakeSvcBinary(t, exe, "9.9.9")

	runSh(t, "set -e\n"+buildBundleVersionSync(shellQuote(exe)))

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("snippet created files next to a bare binary: %v; want only the binary", names)
	}
}

// TestBundleVersionSync_StampsInfoPlist is the end-to-end half on the only
// platform that has both a bundle and PlistBuddy.
func TestBundleVersionSync_StampsInfoPlist(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Info.plist stamping is macOS-only")
	}
	if _, err := os.Stat("/usr/libexec/PlistBuddy"); err != nil {
		t.Skip("PlistBuddy unavailable")
	}
	exe, plist := fakeAppBundle(t, "2.0.2-rc1")

	runSh(t, buildBundleVersionSync(shellQuote(exe)))

	for _, key := range []string{"CFBundleShortVersionString", "CFBundleVersion"} {
		if got := plistValue(t, plist, key); got != "9.9.9" {
			t.Errorf("%s = %q; want the version the binary reports (9.9.9)", key, got)
		}
	}
	if got := plistValue(t, plist, "CFBundleName"); got != "svc" {
		t.Errorf("unrelated key CFBundleName = %q; want svc (only the version keys may change)", got)
	}
}

// TestBundleVersionSync_RejectsHostileVersionOutput proves the charset filter:
// a binary whose `version` output carries shell metacharacters must leave the
// plist alone instead of injecting into the PlistBuddy command line.
func TestBundleVersionSync_RejectsHostileVersionOutput(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Info.plist stamping is macOS-only")
	}
	if _, err := os.Stat("/usr/libexec/PlistBuddy"); err != nil {
		t.Skip("PlistBuddy unavailable")
	}
	dir := t.TempDir()
	macOS := filepath.Join(dir, "svc.app", "Contents", "MacOS")
	if err := os.MkdirAll(macOS, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "injected")
	exe := filepath.Join(macOS, "svc")
	hostile := "#!/bin/sh\necho 'svc 1.0;touch " + marker + "'\n"
	if err := os.WriteFile(exe, []byte(hostile), 0755); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(macOS, "..", "Info.plist")
	stale := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleShortVersionString</key>
    <string>2.0.2-rc1</string>
    <key>CFBundleVersion</key>
    <string>2.0.2-rc1</string>
</dict>
</plist>
`
	if err := os.WriteFile(plist, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}

	runSh(t, buildBundleVersionSync(shellQuote(exe)))

	if _, err := os.Stat(marker); err == nil {
		t.Error("hostile `version` output reached the shell; the charset filter must blank it")
	}
	if got := plistValue(t, plist, "CFBundleShortVersionString"); got != "2.0.2-rc1" {
		t.Errorf("CFBundleShortVersionString = %q; want it left alone for an unusable version", got)
	}
}

// TestBuildUnixUpdateScript_SyncsBundleVersion pins the placement: the stamp
// must run after the checksum verify (never on a payload that failed it) and
// before the relaunch, so LaunchServices sees the new version at launch.
func TestBuildUnixUpdateScript_SyncsBundleVersion(t *testing.T) {
	exe := "/Applications/svc.app/Contents/MacOS/svc"
	script := buildUnixUpdateScript(1, exe, exe+".bak", "/tmp/payload", "abc")

	verify := strings.Index(script, "Verifying integrity")
	stamp := strings.Index(script, "Info.plist")
	relaunch := strings.Index(script, "Starting new version")
	if verify < 0 || stamp < 0 || relaunch < 0 {
		t.Fatalf("update script is missing a stage (verify=%d plist=%d relaunch=%d)\n--- script ---\n%s",
			verify, stamp, relaunch, script)
	}
	if !(verify < stamp && stamp < relaunch) {
		t.Errorf("Info.plist sync must sit between the checksum verify and the relaunch (verify=%d plist=%d relaunch=%d)\n--- script ---\n%s",
			verify, stamp, relaunch, script)
	}
}

// TestBuildUnixRollbackScript_SyncsBundleVersion: a rollback restores the .bak
// binary, so the bundle must go back to the version that binary reports too.
func TestBuildUnixRollbackScript_SyncsBundleVersion(t *testing.T) {
	exe := "/Applications/svc.app/Contents/MacOS/svc"
	script := buildUnixRollbackScript(1, exe+".bak", exe)

	restore := strings.Index(script, "Restoring previous version")
	stamp := strings.Index(script, "Info.plist")
	relaunch := strings.Index(script, "Starting restored version")
	if restore < 0 || stamp < 0 || relaunch < 0 {
		t.Fatalf("rollback script is missing a stage (restore=%d plist=%d relaunch=%d)\n--- script ---\n%s",
			restore, stamp, relaunch, script)
	}
	if !(restore < stamp && stamp < relaunch) {
		t.Errorf("Info.plist sync must sit between the restore and the relaunch (restore=%d plist=%d relaunch=%d)\n--- script ---\n%s",
			restore, stamp, relaunch, script)
	}
}
