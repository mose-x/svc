package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findRepoRoot walks up from the test's working directory (the package dir)
// until it finds go.mod, returning the repository root. go test runs with
// cwd = package directory, so a relative path like "build/..." must be
// anchored at the root explicitly now that this test lives in a subpackage.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repository root (go.mod not found)")
		}
		dir = parent
	}
}

// TestProjectNSI_UpgradeInPlace verifies the NSIS installer template has the
// four upgrade-in-place features: InstallDirRegKey (detect previous),
// SkipDirIfInstalled (skip dir page), taskkill (kill running app), and
// CopyFiles + .bak (backup old version).
func TestProjectNSI_UpgradeInPlace(t *testing.T) {
	nsiPath := filepath.Join(findRepoRoot(t), "build", "windows", "installer", "project.nsi")
	data, err := os.ReadFile(nsiPath)
	if err != nil {
		t.Skipf("project.nsi not found (not on Windows?): %v", err)
	}
	content := string(data)

	checks := []struct {
		desc   string
		substr string
	}{
		{"InstallDirRegKey for auto-detect", `InstallDirRegKey HKLM "${UNINST_KEY}" "InstallLocation"`},
		{"SkipDirIfInstalled function", "Function SkipDirIfInstalled"},
		{"Abort to skip dir page", "Abort"},
		{"silent kill via wscript (no console)", "wscript.exe //B //nologo"},
		{"ships silent kill script", `File /oname=$PLUGINSDIR\svckill.vbs "svckill.vbs"`},
		{"CopyFiles backup", `CopyFiles /SILENT "$INSTDIR\${PRODUCT_EXECUTABLE}" "$INSTDIR\${PRODUCT_EXECUTABLE}.bak"`},
		{"InstallLocation write to registry", `WriteRegStr HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"`},
		// Rename migration (SDK Version Control -> svc): legacy installs are
		// fully retired into the fresh directory (no in-place reuse of the
		// old folder), so folder/shortcuts/registry all carry the new name.
		{"legacy product name define", `!define LEGACY_PRODUCTNAME "SDK Version Control"`},
		{"legacy executable define", `!define LEGACY_EXECUTABLE  "SDK Version Control.exe"`},
		{"legacy uninstall key define", `!define LEGACY_UNINST_KEY  "Software\Microsoft\Windows\CurrentVersion\Uninstall\${INFO_COMPANYNAME}${LEGACY_PRODUCTNAME}"`},
		{"legacy shortcuts removal", `Delete "$DESKTOP\${LEGACY_PRODUCTNAME}.lnk"`},
		{"uninstall deletes new desktop shortcuts via wildcard", `Delete "$DESKTOP\${INFO_PRODUCTNAME}*.lnk"`},
		{"uninstall deletes legacy desktop shortcuts via wildcard", `Delete "$DESKTOP\${LEGACY_PRODUCTNAME}*.lnk"`},
		{"uninstall deletes new start-menu shortcuts via wildcard", `Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}*.lnk"`},
		{"uninstall scans all-users context too", "SetShellVarContext all"},
		{"legacy uninstall key cleanup", `DeleteRegKey HKLM "${LEGACY_UNINST_KEY}"`},
		{"legacy uninstall key cleanup in HKCU too", `DeleteRegKey HKCU "${LEGACY_UNINST_KEY}"`},
		{"legacy dir lookup falls back to HKCU (per-user installs)", `ReadRegStr $1 HKCU "${LEGACY_UNINST_KEY}" "InstallLocation"`},
		{"legacy dir recovered from UninstallString (no InstallLocation written)", `ReadRegStr $2 HKLM "${LEGACY_UNINST_KEY}" "UninstallString"`},
		{"GetParent derives the legacy directory", `${GetParent} "$2" $1`},
		{"FileFunc include for GetParent", `!include "FileFunc.nsh"`},
		{"legacy WebView2 datapath cleanup", `RMDir /r "$AppData\${LEGACY_EXECUTABLE}"`},
		{"legacy directory removal", `RMDir /r "$1"`},
		{"same-dir guard for legacy removal", `${If} $1 != "$INSTDIR"`},
		// Self-updated legacy installs have no registry entry; the Section's
		// else-branch detects the legacy executable at the default old
		// location and removes that folder, and retires any old-named exe
		// found inside the chosen install directory.
		{"self-update legacy folder detection", `IfFileExists "$PROGRAMFILES64\${LEGACY_PRODUCTNAME}\${LEGACY_EXECUTABLE}"`},
		{"self-update legacy folder removal", `RMDir /r "$PROGRAMFILES64\${LEGACY_PRODUCTNAME}"`},
		{"legacy exe in-place retirement", `Delete "$INSTDIR\${LEGACY_EXECUTABLE}"`},
	}

	for _, c := range checks {
		t.Run(c.desc, func(t *testing.T) {
			if !strings.Contains(content, c.substr) {
				t.Errorf("project.nsi missing %q\nin content:\n%s", c.substr, content)
			}
		})
	}
}

// TestSvckillVbs verifies the installer's process-kill helper is fully silent:
// it terminates the current/legacy app via WMI (no taskkill/cmd/powershell
// console window can flash during install).
func TestSvckillVbs(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(findRepoRoot(t), "build", "windows", "installer", "svckill.vbs"))
	if err != nil {
		t.Skipf("svckill.vbs not found: %v", err)
	}
	content := string(data)
	for _, want := range []string{"Win32_Process", "Terminate", "svc", "SDK Version Control"} {
		if !strings.Contains(content, want) {
			t.Errorf("svckill.vbs missing %q", want)
		}
	}
	for _, bad := range []string{"taskkill", "cmd.exe", "powershell"} {
		if strings.Contains(strings.ToLower(content), bad) {
			t.Errorf("svckill.vbs must not spawn a console via %q", bad)
		}
	}
}
