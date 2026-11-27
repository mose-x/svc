//go:build windows

package update

import (
	"strconv"
	"strings"
	"testing"
)

// TestBuildUpdateScript_containsHashAndCertutil pins BUG D's fix: the .bat
// written by ApplyUpdate must (a) embed the pre-copy SHA256, (b) invoke
// certutil -hashfile on the replaced exe to verify the post-copy bytes, and
// (c) roll back to the .bak on mismatch. Without these, a partial copy
// leaves a corrupt binary in place of the working app with no auto-recovery.
func TestBuildUpdateScript_containsHashAndCertutil(t *testing.T) {
	const hash = "deadbeefcafef00dba5eba11cafebabedeadbeefcafef00dba5eba11cafebabe"
	const pid = 4242
	const currentExe = `C:\svc\svc.exe`
	const bak = `C:\svc\svc.exe.bak`
	const newExe = `C:\tmp\svc_update_new.exe`
	script := buildUpdateScript(pid, currentExe, bak, newExe, hash, true)

	checks := []struct {
		name string
		want string
	}{
		{"embeds expected hash", hash},
		{"invokes certutil", `certutil -hashfile "` + currentExe + `" SHA256`},
		{"compares actual vs expected", `if /i not "%actual%"=="%expected%"`},
		{"uses move for replacement", `move /Y "` + newExe + `" "` + currentExe + `"`},
		{"has copy fallback", `copy /Y "` + newExe + `" "` + currentExe + `"`},
		{"rolls back to bak", `copy /Y "` + bak + `" "` + currentExe + `"`},
		{"cleans stale backups before backup", "echo Cleaning stale backups..."},
		{"removes old-named bak files", `*.bak") do if /i not`},
		{"exits non-zero on mismatch", "exit /b 1"},
		// Wait-loop PID match: `findstr /C:" <pid> "` matches the PID as an
		// in-line literal surrounded by spaces (tasklist /NH pads the PID
		// column with spaces), so PID 4242 does not match a row for PID
		// 42421 or 14242.
		{"uses findstr space-delimited PID match", `findstr /C:" ` + strconv.Itoa(pid) + ` " >NUL`},
	}
	for _, c := range checks {
		if !strings.Contains(script, c.want) {
			t.Errorf("buildUpdateScript missing %q\n--- script ---\n%s", c.name, script)
		}
	}
	// The `/B` (beginning-of-line) findstr form must be gone: tasklist /NH
	// lines start with the image name, so /B on the PID can never match and
	// the wait loop becomes dead code (updater overwrites the running exe).
	if strings.Contains(script, "findstr /B") {
		t.Errorf("buildUpdateScript still uses findstr /B (dead wait loop)\n--- script ---\n%s", script)
	}
	// M11: the old substring `find "<pid>"` pattern must NOT be present — it
	// matched any PID containing the same digits (e.g. 4242 vs 42421).
	if strings.Contains(script, `find "`+strconv.Itoa(pid)+`" >NUL`) {
		t.Errorf("buildUpdateScript still uses the old substring `find %%d` pattern (M11 not fixed)\n--- script ---\n%s", script)
	}
	// The loop var must use the batch `%%i` form (a single `%%` in the file is
	// a stray percent that breaks the for-loop); a Go `%%%%i` template becomes
	// `%%i` in the output. Assert the rendered script has exactly `%%i` and
	// not the broken `%i` form anywhere except inside the `delims=` clause.
	if !strings.Contains(script, "%%i") {
		t.Errorf("buildUpdateScript missing batch loop var %%i\n--- script ---\n%s", script)
	}
}

// TestBuildRollbackScript_findstrPidMatch pins the same wait-loop fix for the
// rollback .bat: the PID must be matched via the space-delimited in-line
// `findstr /C:" <pid> "` form (never the dead `/B` anchored form), otherwise
// RollbackUpdate would restore the .bak over the still-running exe.
func TestBuildRollbackScript_findstrPidMatch(t *testing.T) {
	const pid = 4242
	const currentExe = `C:\svc\svc.exe`
	const bak = `C:\svc\svc.exe.bak`
	script := buildRollbackScript(pid, bak, currentExe, true)

	checks := []struct {
		name string
		want string
	}{
		{"uses findstr space-delimited PID match", `findstr /C:" ` + strconv.Itoa(pid) + ` " >NUL`},
		{"filters tasklist by PID", `tasklist /FI "PID eq ` + strconv.Itoa(pid) + `" /NH`},
		{"restores bak over current exe", `copy /Y "` + bak + `" "` + currentExe + `"`},
		{"relaunches restored exe", `start "" "` + currentExe + `"`},
		{"aborts on timeout", "exit /b 1"},
	}
	for _, c := range checks {
		if !strings.Contains(script, c.want) {
			t.Errorf("buildRollbackScript missing %q\n--- script ---\n%s", c.name, script)
		}
	}
	if strings.Contains(script, "findstr /B") {
		t.Errorf("buildRollbackScript still uses findstr /B (dead wait loop)\n--- script ---\n%s", script)
	}
}

// trivialCommand is a child that starts and exits 0 on this platform, for the
// tests that really launch one.
func trivialCommand() (string, []string) {
	return "cmd", []string{"/C", "exit 0"}
}

// TestBuildRelaunchBlock pins the two shapes of the .bat tail. The relaunching
// one is the pair of lines the GUI has always run, byte for byte; the other is
// nothing at all, which is what keeps `svc self update` from opening a window
// when the swap finishes.
func TestBuildRelaunchBlock(t *testing.T) {
	got := buildRelaunchBlock(true, "Starting new version...", `C:\svc\svc.exe`)
	want := "echo Starting new version...\nstart \"\" \"C:\\svc\\svc.exe\"\n"
	if got != want {
		t.Errorf("buildRelaunchBlock(true) = %q, want %q", got, want)
	}
	if off := buildRelaunchBlock(false, "Starting new version...", `C:\svc\svc.exe`); off != "" {
		t.Errorf("buildRelaunchBlock(false) = %q, want nothing at all", off)
	}
}

// TestBuildUpdateScript_NoRelaunch covers the CLI's half of ApplyOptions: the
// same .bat over the same bytes, minus the launch. Everything that makes the
// swap safe -- the PID wait, the backup, the move-then-copy replacement, the
// certutil verify and its rollback, the stale-backup cleanup, the
// self-delete -- has to survive, because a CLI update that skipped any of them
// would be a different and worse update than the GUI's.
func TestBuildUpdateScript_NoRelaunch(t *testing.T) {
	const (
		currentExe = `C:\svc\svc.exe`
		bak        = `C:\svc\svc.exe.bak`
		newExe     = `C:\tmp\svc_update_new.exe`
	)
	script := buildUpdateScript(4242, currentExe, bak, newExe, "abc", false)

	for _, absent := range []string{
		`start ""`,
		"Starting new version",
	} {
		if strings.Contains(script, absent) {
			t.Errorf("the no-relaunch update .bat still carries %q\n--- script ---\n%s", absent, script)
		}
	}
	for _, present := range []string{
		`tasklist /FI "PID eq 4242" /NH`,
		`copy /Y "` + currentExe + `" "` + bak + `"`,
		`move /Y "` + newExe + `" "` + currentExe + `"`,
		`certutil -hashfile "` + currentExe + `" SHA256`,
		`if /i not "%actual%"=="%expected%"`,
		`del "%~f0"`,
	} {
		if !strings.Contains(script, present) {
			t.Errorf("the no-relaunch update .bat lost %q\n--- script ---\n%s", present, script)
		}
	}
}

// TestBuildRollbackScript_NoRelaunch is the same assertion for the restore
// path, which the CLI reaches through `svc self rollback`.
func TestBuildRollbackScript_NoRelaunch(t *testing.T) {
	const (
		currentExe = `C:\svc\svc.exe`
		bak        = `C:\svc\svc.exe.bak`
	)
	script := buildRollbackScript(4242, bak, currentExe, false)

	for _, absent := range []string{`start ""`, "Starting restored version"} {
		if strings.Contains(script, absent) {
			t.Errorf("the no-relaunch rollback .bat still carries %q\n--- script ---\n%s", absent, script)
		}
	}
	for _, present := range []string{
		`tasklist /FI "PID eq 4242" /NH`,
		`copy /Y "` + bak + `" "` + currentExe + `"`,
		`del "%~f0"`,
	} {
		if !strings.Contains(script, present) {
			t.Errorf("the no-relaunch rollback .bat lost %q\n--- script ---\n%s", present, script)
		}
	}
}
