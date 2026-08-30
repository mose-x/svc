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

// TestProjectNSI_RegistryView64 pins the registry-view symmetry fix: the
// uninstall key is written under SetRegView 64 (wails.writeUninstaller), so
// every detection read must happen in the 64-bit view too. The installer is
// a 32-bit WOW64 process whose default view is the 32-bit hive; without an
// explicit SetRegView 64 before the reads, upgrade-in-place detection misses
// the key on x64 (the v2.0.2-rc1 -> rc2 "previous install not found" bug).
func TestProjectNSI_RegistryView64(t *testing.T) {
	nsiPath := filepath.Join(findRepoRoot(t), "build", "windows", "installer", "project.nsi")
	data, err := os.ReadFile(nsiPath)
	if err != nil {
		t.Skipf("project.nsi not found: %v", err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")

	// .onInit establishes the 64-bit view after the architecture check so
	// SkipDirIfInstalled (a later page callback) reads the right hive.
	onInitStart := strings.Index(content, "Function .onInit")
	if onInitStart < 0 {
		t.Fatal(".onInit not found")
	}
	onInitEnd := strings.Index(content[onInitStart:], "FunctionEnd")
	if onInitEnd < 0 {
		t.Fatal(".onInit has no FunctionEnd")
	}
	onInit := content[onInitStart : onInitStart+onInitEnd]
	archIdx := strings.Index(onInit, "wails.checkArchitecture")
	viewIdx := strings.Index(onInit, "SetRegView 64")
	if archIdx < 0 || viewIdx < 0 {
		t.Fatalf(".onInit must run checkArchitecture and SetRegView 64:\n%s", onInit)
	}
	if archIdx > viewIdx {
		t.Fatal("SetRegView 64 must come after wails.checkArchitecture in .onInit")
	}

	// The view must be established before the SkipDirIfInstalled read.
	firstView := strings.Index(content, "SetRegView 64")
	detectRead := strings.Index(content, `ReadRegStr $0 HKLM "${UNINST_KEY}" "InstallLocation"`)
	if detectRead < 0 {
		t.Fatal("SkipDirIfInstalled registry read not found")
	}
	if firstView < 0 || firstView > detectRead {
		t.Fatal("SetRegView 64 must precede the SkipDirIfInstalled registry read")
	}

	// The main install Section restates the view so the legacy-migration
	// reads and the InstallLocation write do not rely on macro side effects.
	sectionStart := strings.Index(content, "\nSection\n")
	if sectionStart < 0 {
		t.Fatal("main install Section not found")
	}
	sectionEnd := strings.Index(content[sectionStart:], "SectionEnd")
	if sectionEnd < 0 {
		t.Fatal("main install Section has no SectionEnd")
	}
	section := content[sectionStart : sectionStart+sectionEnd]
	if !strings.Contains(section, "SetRegView 64") {
		t.Fatal("main Section must restate SetRegView 64")
	}

	// Every uninstall-key read (current + legacy) must sit after the view
	// switch.
	for _, key := range []string{`"${UNINST_KEY}"`, `"${LEGACY_UNINST_KEY}"`} {
		pos := 0
		for {
			i := strings.Index(content[pos:], "ReadRegStr")
			if i < 0 {
				break
			}
			i += pos
			lineEnd := strings.Index(content[i:], "\n")
			if lineEnd < 0 {
				lineEnd = len(content) - i
			}
			line := content[i : i+lineEnd]
			if strings.Contains(line, key) && i < firstView {
				t.Fatalf("uninstall-key read precedes SetRegView 64: %s", line)
			}
			pos = i + 1
		}
	}

	// Nothing may drop back to the 32-bit (or lastused) view.
	for _, bad := range []string{"SetRegView 32", "SetRegView lastused"} {
		if strings.Contains(content, bad) {
			t.Errorf("project.nsi must not contain %q", bad)
		}
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
