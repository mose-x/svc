//go:build windows

package update

import (
	"fmt"
	"os"
	"path/filepath"

	"svc/internal/apperr"
	"svc/internal/helpers"
)

func getUpdateFilePath() string {
	return filepath.Join(os.TempDir(), "svc_update_new.exe")
}

// backupPath returns <exe>.bak, used by ApplyUpdate to keep the previous
// binary so RollbackUpdate can restore it on failure.
func backupPath(currentExe string) string {
	return currentExe + ".bak"
}

// ApplyUpdate writes a .bat updater that: waits for the app to exit, backs up
// the running exe to .bak, copies the downloaded update over the running exe,
// verifies the post-copy SHA256 matches the pre-copy hash (rolling back to
// .bak on mismatch so a corrupt binary never replaces a working one), then
// relaunches. The hash is computed in Go BEFORE writing the script so the
// .bat only needs certutil (a built-in Windows tool) for the post-copy check;
// certutil output is parsed in-bat to compare against the embedded hash.
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
	// DownloadUpdate already verified the download against the server-
	// published hash; this separate pre-copy hash is what the .bat compares
	// the post-copy bytes against, catching a partial copy / mid-copy crash
	// that would otherwise leave a corrupt binary in place of a working app.
	expectedHash, err := sha256OfFile(newExe)
	if err != nil {
		return fmt.Errorf("failed to hash update file: %w", err)
	}

	bak := backupPath(currentExe)
	scriptPath := filepath.Join(os.TempDir(), "svc_updater.bat")
	pid := os.Getpid()
	scriptContent := buildUpdateScript(pid, currentExe, bak, newExe, expectedHash)

	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0644); err != nil {
		return fmt.Errorf("failed to create update script: %w", err)
	}

	cmd := helpers.CreateCmd("cmd", "/C", scriptPath)
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch update script: %w", err)
	}

	u.rt.Quit()
	return nil
}

// buildUpdateScript renders the .bat body for ApplyUpdate. Extracted so the
// hash-verify + rollback logic can be unit-tested without launching the
// updater for real (it touches the running exe + quits the app).
//
// The wait loop uses `findstr /C:" <pid> "` (in-line literal match with
// surrounding spaces), NOT `findstr /B`: tasklist /NH lines start with the
// image name, so a beginning-of-line (/B) anchor on the PID never matches and
// the loop would be dead code, letting the script overwrite the still-running
// exe. The space-delimited form cannot false-match PIDs like 42421 or 14242
// for target 4242 because the PID column is always space-padded.
//
// certutil -hashfile prints:
//
//	SHA256 hash of <file>:
//	<hash>
//	CertUtil: -hashfile command completed successfully.
//
// `skip=1 delims=` makes the for-loop body see line 2 first (the hash), with
// no token splitting; `if not defined actual` captures only that first line.
// `if /i` compares case-insensitively because certutil emits uppercase hex on
// some Windows versions and lowercase on others, while sha256OfFile always
// returns lowercase.
func buildUpdateScript(pid int, currentExe, bak, newExe, expectedHash string) string {
	return fmt.Sprintf(`@echo off
echo Waiting for application to close...
set /a timeout=60
:waitloop
tasklist /FI "PID eq %d" /NH 2>NUL | findstr /C:" %d " >NUL
if not errorlevel 1 (
    timeout /t 1 /nobreak >NUL
    set /a timeout-=1
    if %%timeout%% leq 0 (
        echo Update timed out waiting for app to exit, aborting
        exit /b 1
    )
    goto waitloop
)
echo Cleaning stale backups...
for %%%%I in ("%s") do set "BAKDIR=%%%%~dpI"
for %%%%f in ("%%BAKDIR%%*.bak") do if /i not "%%%%f"=="%s" del /F /Q "%%%%f"
echo Backing up current binary...
copy /Y "%s" "%s" >NUL
if errorlevel 1 (
    echo Backup failed, aborting update
    exit /b 1
)
echo Replacing application...
move /Y "%s" "%s" >NUL 2>&1
if errorlevel 1 (
    copy /Y "%s" "%s" >NUL
    if errorlevel 1 (
        echo Update failed!
        exit /b 1
    )
    del /F /Q "%s" >NUL 2>&1
)
echo Verifying integrity...
set "expected=%s"
set "actual="
for /f "skip=1 delims=" %%%%i in ('certutil -hashfile "%s" SHA256') do (
    if not defined actual set "actual=%%%%i"
)
if /i not "%%actual%%"=="%%expected%%" (
    echo Integrity check failed: expected %%expected%%, got %%actual%%
    echo Rolling back to previous version...
    copy /Y "%s" "%s" >NUL
    exit /b 1
)
echo Starting new version...
start "" "%s"
del "%%~f0"
`, pid, pid, bak, bak, currentExe, bak, newExe, currentExe, newExe, currentExe, newExe, expectedHash, currentExe, bak, currentExe, currentExe)
}

// RollbackUpdate restores the .bak binary created by the previous ApplyUpdate.
// Fails with a clear message if no backup exists (first install or user deleted it).
// Uses the same wait-then-rename pattern as ApplyUpdate because the running
// .exe is locked by Windows until the process exits.
func (u *Updater) RollbackUpdate() error {
	currentExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get current program path: %w", err)
	}
	bak := findBackupPath(currentExe)
	if _, err := os.Stat(bak); err != nil {
		return apperr.New(apperr.NoBackup, map[string]string{"path": bak})
	}

	scriptPath := filepath.Join(os.TempDir(), "svc_rollback.bat")
	pid := os.Getpid()
	scriptContent := buildRollbackScript(pid, bak, currentExe)

	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0644); err != nil {
		return fmt.Errorf("failed to create rollback script: %w", err)
	}

	cmd := helpers.CreateCmd("cmd", "/C", scriptPath)
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch rollback script: %w", err)
	}

	u.rt.Quit()
	return nil
}

// buildRollbackScript renders the .bat body for RollbackUpdate. Extracted so
// the wait-loop + restore logic can be unit-tested without launching the
// rollback for real.
//
// The wait loop uses `findstr /C:" <pid> "` (in-line literal match with
// surrounding spaces), NOT `findstr /B`: tasklist /NH lines start with the
// image name, so a beginning-of-line (/B) anchor on the PID never matches and
// the loop would be dead code, letting the script overwrite the still-running
// exe. The space-delimited form cannot false-match PIDs like 42421 or 14242
// for target 4242 because the PID column is always space-padded.
func buildRollbackScript(pid int, bak, currentExe string) string {
	return fmt.Sprintf(`@echo off
echo Waiting for application to close...
set /a timeout=60
:waitloop
tasklist /FI "PID eq %d" /NH 2>NUL | findstr /C:" %d " >NUL
if not errorlevel 1 (
    timeout /t 1 /nobreak >NUL
    set /a timeout-=1
    if %%timeout%% leq 0 (
        echo Rollback timed out waiting for app to exit, aborting
        exit /b 1
    )
    goto waitloop
)
echo Restoring previous version...
copy /Y "%s" "%s" >NUL
if errorlevel 1 (
    echo Rollback failed!
    exit /b 1
)
echo Starting restored version...
start "" "%s"
del "%%~f0"
`, pid, pid, bak, currentExe, currentExe)
}
