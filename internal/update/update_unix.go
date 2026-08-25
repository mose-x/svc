//go:build !windows

package update

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"svc/internal/apperr"
	"svc/internal/helpers"
)

func getUpdateFilePath() string {
	return filepath.Join(os.TempDir(), "svc_update_new")
}

// backupPath returns <exe>.bak, used by ApplyUpdate to keep the previous
// binary so RollbackUpdate can restore it on failure.
func backupPath(currentExe string) string {
	return currentExe + ".bak"
}

// shellQuote wraps a string in single quotes for safe shell interpolation.
// Single quotes inside the string are escaped via the standard '\” idiom.
// Unlike double quotes, single-quoted strings have no interpolation, so
// paths containing $, `, ", spaces, etc. are safe.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeTempScript writes content to a freshly created temp script with an
// UNPREDICTABLE name and returns the path. os.CreateTemp opens with
// O_CREATE|O_EXCL (never follows a pre-planted symlink, refuses to overwrite
// an existing file) and picks a random suffix, closing the two hazards of the
// old fixed /tmp names (svc_updater.sh / svc_rollback.sh):
//
//  1. symlink attack — a local attacker pre-creates the fixed path as a
//     symlink to an arbitrary file; a plain os.WriteFile then follows it and
//     overwrites the target with the script body;
//  2. TOCTOU swap — the attacker replaces the script between write and exec,
//     injecting commands that run with the user's privileges.
//
// The file is created 0600 and chmod'd to 0700 so /bin/sh can execute it.
func writeTempScript(pattern, content string) (string, error) {
	f, err := os.CreateTemp(os.TempDir(), pattern)
	if err != nil {
		return "", fmt.Errorf("failed to create temp script: %w", err)
	}
	scriptPath := f.Name()
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(scriptPath)
		return "", fmt.Errorf("failed to write temp script: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(scriptPath)
		return "", fmt.Errorf("failed to write temp script: %w", err)
	}
	if err := os.Chmod(scriptPath, 0700); err != nil {
		os.Remove(scriptPath)
		return "", fmt.Errorf("failed to make temp script executable: %w", err)
	}
	return scriptPath, nil
}

// secureUpdatePayload moves the downloaded payload from the fixed, guessable
// download path to an unpredictable temp name (CreateTemp: O_CREATE|O_EXCL,
// random suffix) and returns the new path. The fixed /tmp/svc_update_new name
// could be pre-planted or swapped by a local attacker between the download
// and the updater script's mv; referencing the unpredictable name in the
// script removes that window. The rename happens inside the same temp dir,
// so it is atomic (same filesystem).
func secureUpdatePayload(fixedPath string) (string, error) {
	f, err := os.CreateTemp(os.TempDir(), "svc_update_payload_*")
	if err != nil {
		return "", fmt.Errorf("failed to reserve temp payload name: %w", err)
	}
	securePath := f.Name()
	f.Close()
	if err := os.Rename(fixedPath, securePath); err != nil {
		os.Remove(securePath)
		return "", fmt.Errorf("failed to secure update payload: %w", err)
	}
	return securePath, nil
}

// buildUnixUpdateScript renders the /bin/sh body for ApplyUpdate. Extracted so
// the wait/backup/replace/verify/rollback logic can be unit-tested without
// launching the updater for real (it touches the running binary + quits the
// app). All paths are shell-quoted by the caller's Sprintf arguments.
func buildUnixUpdateScript(pid int, currentExe, bak, newExe, expectedHash string) string {
	exeQ := shellQuote(currentExe)
	bakQ := shellQuote(bak)
	newQ := shellQuote(newExe)
	hashQ := shellQuote(expectedHash)
	return fmt.Sprintf(`#!/bin/sh
expected_hash=%s
echo "Waiting for application to close..."
timeout=60
while kill -0 %d 2>/dev/null; do
    sleep 1
    timeout=$((timeout - 1))
    if [ "$timeout" -le 0 ]; then
        echo "Update timed out waiting for app to exit, aborting"
        exit 1
    fi
done
echo "Cleaning stale backups..."
bak_dir=$(dirname %s)
for oldbak in "$bak_dir"/*.bak; do
    [ -e "$oldbak" ] || continue
    [ "$oldbak" = %s ] && continue
    rm -f "$oldbak"
done
echo "Backing up current binary..."
if ! mv -f %s %s 2>/dev/null; then
    # Cross-device: fall back to cp+rm. mv is atomic on same FS only.
    cp -f %s %s && rm -f %s
    if [ $? -ne 0 ]; then
        echo "Backup failed, aborting update"
        exit 1
    fi
fi
echo "Replacing application..."
if ! mv -f %s %s 2>/dev/null; then
    cp -f %s %s && rm -f %s
    if [ $? -ne 0 ]; then
        echo "Update failed! Restoring backup..."
        mv -f %s %s 2>/dev/null || cp -f %s %s
        exit 1
    fi
fi
chmod +x %s
echo "Verifying integrity..."
hash_output=$(sha256sum %s 2>/dev/null || shasum -a 256 %s)
actual_hash=$(echo "$hash_output" | cut -d' ' -f1)
if [ "$actual_hash" != "$expected_hash" ]; then
    echo "Checksum mismatch! Restoring backup..."
    mv -f %s %s 2>/dev/null || cp -f %s %s
    exit 1
fi
echo "Starting new version..."
nohup %s > /dev/null 2>&1 &
rm -f "$0"
`, hashQ, pid, bakQ, bakQ, exeQ, bakQ, exeQ, bakQ, exeQ, newQ, exeQ, newQ, exeQ, newQ, bakQ, exeQ, bakQ, exeQ, exeQ, exeQ, exeQ, bakQ, exeQ, bakQ, exeQ, exeQ)
}

// buildUnixRollbackScript renders the /bin/sh body for RollbackUpdate.
// Extracted for the same testability reasons as buildUnixUpdateScript.
func buildUnixRollbackScript(pid int, bak, currentExe string) string {
	exeQ := shellQuote(currentExe)
	bakQ := shellQuote(bak)
	return fmt.Sprintf(`#!/bin/sh
echo "Waiting for application to close..."
timeout=60
while kill -0 %d 2>/dev/null; do
    sleep 1
    timeout=$((timeout - 1))
    if [ "$timeout" -le 0 ]; then
        echo "Rollback timed out waiting for app to exit, aborting"
        exit 1
    fi
done
echo "Restoring previous version..."
if ! mv -f %s %s 2>/dev/null; then
    cp -f %s %s && rm -f %s
    if [ $? -ne 0 ]; then
        echo "Rollback failed!"
        exit 1
    fi
fi
chmod +x %s
echo "Starting restored version..."
nohup %s > /dev/null 2>&1 &
rm -f "$0"
`, pid, bakQ, exeQ, bakQ, exeQ, bakQ, exeQ, exeQ)
}

// ApplyUpdate launches a background /bin/sh script that: waits for the
// current process to exit (by PID, not pgrep -f which matches too wide),
// atomically renames the running binary to .bak, renames the downloaded
// payload into place, chmod +x, relaunches, and self-deletes.
//
// Rename (mv) is used instead of cp so the replacement is atomic: a failed
// second step leaves the .bak intact and the current binary untouched,
// rather than overwriting it halfway and leaving a corrupt executable.
// A 60s timeout guards against the wait loop hanging forever if the app
// fails to exit.
//
// Both the script and the payload it references get unpredictable
// CreateTemp-based names (see writeTempScript / secureUpdatePayload) instead
// of the old fixed /tmp paths, which were a symlink/TOCTOU hazard.
func (u *Updater) ApplyUpdate() error {
	currentExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get current program path: %w", err)
	}

	newExe := getUpdateFilePath()
	if _, err := os.Stat(newExe); err != nil {
		return fmt.Errorf("update file does not exist: %w", err)
	}

	// Compute the SHA256 of the downloaded update BEFORE writing the script.
	// DownloadUpdate already verified against the server-published hash;
	// this pre-copy hash is what the shell script compares the post-copy
	// bytes against, catching a partial copy or /tmp TOCTOU swap.
	expectedHash, err := sha256OfFile(newExe)
	if err != nil {
		return fmt.Errorf("failed to hash update file: %w", err)
	}

	// Move the payload off the fixed guessable name before the script
	// references it. On any later failure the payload is restored so the
	// user can retry ApplyUpdate without re-downloading.
	securePayload, err := secureUpdatePayload(newExe)
	if err != nil {
		return err
	}
	restorePayload := func() { os.Rename(securePayload, newExe) }

	bak := backupPath(currentExe)
	pid := os.Getpid()
	scriptContent := buildUnixUpdateScript(pid, currentExe, bak, securePayload, expectedHash)

	scriptPath, err := writeTempScript("svc_updater_*.sh", scriptContent)
	if err != nil {
		restorePayload()
		return fmt.Errorf("failed to create update script: %w", err)
	}

	cmd := helpers.CreateCmd("/bin/sh", scriptPath)
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		os.Remove(scriptPath)
		restorePayload()
		return fmt.Errorf("failed to launch update script: %w", err)
	}

	u.rt.Quit()
	return nil
}

// RollbackUpdate restores the .bak binary created by the previous ApplyUpdate.
// Fails with a clear message if no backup exists (first install or user deleted it).
// Like ApplyUpdate, it shells out to a script that runs after the app closes,
// because the running binary cannot be overwritten on Unix while it's executing
// (the kernel keeps the inode alive, but copy semantics differ across FSes).
// The script gets an unpredictable CreateTemp-based name (see writeTempScript).
func (u *Updater) RollbackUpdate() error {
	currentExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get current program path: %w", err)
	}
	bak := findBackupPath(currentExe)
	if _, err := os.Stat(bak); err != nil {
		return apperr.New(apperr.NoBackup, map[string]string{"path": bak})
	}

	pid := os.Getpid()
	scriptContent := buildUnixRollbackScript(pid, bak, currentExe)

	scriptPath, err := writeTempScript("svc_rollback_*.sh", scriptContent)
	if err != nil {
		return fmt.Errorf("failed to create rollback script: %w", err)
	}

	cmd := helpers.CreateCmd("/bin/sh", scriptPath)
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		os.Remove(scriptPath)
		return fmt.Errorf("failed to launch rollback script: %w", err)
	}

	u.rt.Quit()
	return nil
}
