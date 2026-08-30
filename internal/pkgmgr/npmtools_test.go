package pkgmgr

import (
	"os"
	"strings"
	"testing"

	"svc/internal/sdk"
)

// writeFakeNpmRecord writes an npm fake that records its argv into marker
// (one line) and exits 0. Used to assert the exact commands SVC issues.
func writeFakeNpmRecord(t *testing.T, dir, marker string) {
	t.Helper()
	win := "@echo off\r\necho %* > \"" + marker + "\"\r\nexit /b 0\r\n"
	unix := "#!/bin/sh\necho \"$@\" > \"" + marker + "\"\nexit 0\n"
	writeFakeTool(t, dir, "npm", win, unix)
}

// writeFakeNpmRegistryEcho writes an npm fake that answers
// `config get registry` with the given value and records other invocations.
func writeFakeNpmRegistryEcho(t *testing.T, dir, registry, marker string) {
	t.Helper()
	win := "@echo off\r\n" +
		"if \"%1\"==\"config\" if \"%2\"==\"get\" (\r\n" +
		"  echo " + registry + "\r\n" +
		"  exit /b 0\r\n" +
		")\r\n" +
		"echo %* > \"" + marker + "\"\r\nexit /b 0\r\n"
	unix := "#!/bin/sh\n" +
		"if [ \"$1\" = \"config\" ] && [ \"$2\" = \"get\" ]; then\n" +
		"  echo \"" + registry + "\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo \"$@\" > \"" + marker + "\"\nexit 0\n"
	writeFakeTool(t, dir, "npm", win, unix)
}

func readMarker(t *testing.T, marker string) string {
	t.Helper()
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker %s: %v", marker, err)
	}
	return strings.TrimSpace(string(b))
}

func TestRunScopedCommandCapture_ReturnsOutput(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeTool(t, binDir, "echotool",
		"@echo off\r\necho hello-out\r\nexit /b 0\r\n",
		"#!/bin/sh\necho hello-out\n")
	out, err := s.runScopedCommandCapture("echotool", sdk.NodeJS)
	if err != nil {
		t.Fatalf("runScopedCommandCapture = %v; want nil", err)
	}
	if out != "hello-out" {
		t.Fatalf("output = %q; want trimmed %q", out, "hello-out")
	}
}

func TestRunScopedCommandCapture_FailureReturnsOutputAndMarker(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeTool(t, binDir, "failtool", failToolWin, failToolUnix)
	out, err := s.runScopedCommandCapture("failtool", sdk.NodeJS)
	if err == nil {
		t.Fatal("runScopedCommandCapture = nil; want error")
	}
	if out != "some-fail-text" {
		t.Fatalf("output = %q; want %q even on failure", out, "some-fail-text")
	}
	if !strings.HasPrefix(err.Error(), "[svc:exec-failed]") {
		t.Fatalf("error = %q; want exec-failed marker", err)
	}
}

func TestGetNpmRegistry_NormalizesTrailingSlash(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	marker := markerFile(t)
	writeFakeNpmRegistryEcho(t, binDir, "https://registry.npmjs.org/", marker)
	got, err := s.GetNpmRegistry()
	if err != nil {
		t.Fatalf("GetNpmRegistry = %v", err)
	}
	if got != "https://registry.npmjs.org" {
		t.Fatalf("GetNpmRegistry = %q; want trailing slash stripped", got)
	}
}

func TestGetNpmRegistry_NoActiveNode(t *testing.T) {
	s, _ := newInstallTestService(t, "")
	if _, err := s.GetNpmRegistry(); err == nil || !strings.HasPrefix(err.Error(), "[svc:need-sdk]") {
		t.Fatalf("GetNpmRegistry = %v; want need-sdk marker", err)
	}
}

func TestGetNpmRegistry_NpmMissing(t *testing.T) {
	s, _ := newInstallTestService(t, "23.0.0")
	if _, err := s.GetNpmRegistry(); err == nil || !strings.HasPrefix(err.Error(), "[svc:exec-failed]") {
		t.Fatalf("GetNpmRegistry = %v; want exec-failed marker", err)
	}
}

func TestSetNpmRegistry_WritesConfigSet(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	marker := markerFile(t)
	writeFakeNpmRecord(t, binDir, marker)
	if err := s.SetNpmRegistry("https://registry.npmmirror.com"); err != nil {
		t.Fatalf("SetNpmRegistry = %v", err)
	}
	if got := readMarker(t, marker); got != "config set registry https://registry.npmmirror.com" {
		t.Fatalf("npm argv = %q; want config set registry ...", got)
	}
}

func TestSetNpmRegistry_NoActiveNode(t *testing.T) {
	s, _ := newInstallTestService(t, "")
	if err := s.SetNpmRegistry("https://registry.npmjs.org"); err == nil || !strings.HasPrefix(err.Error(), "[svc:need-sdk]") {
		t.Fatalf("SetNpmRegistry = %v; want need-sdk marker", err)
	}
}

func TestSetNpmRegistry_RejectsBadURLs(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	marker := markerFile(t)
	writeFakeNpmRecord(t, binDir, marker)
	for _, bad := range []string{"", "   ", "not-a-url", "file:///x"} {
		err := s.SetNpmRegistry(bad)
		if err == nil {
			t.Fatalf("SetNpmRegistry(%q) = nil; want error", bad)
		}
		if strings.HasPrefix(err.Error(), "[svc:need-sdk]") {
			t.Fatalf("SetNpmRegistry(%q) = %v; wrong marker", bad, err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("npm ran despite rejected URLs")
	}
}

func TestSetNpmRegistry_RejectsNonHTTPSchemes(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeNpmRecord(t, binDir, markerFile(t))
	err := s.SetNpmRegistry("ftp://example.com")
	if err == nil || !strings.HasPrefix(err.Error(), "[svc:scheme-not-allowed]") {
		t.Fatalf("SetNpmRegistry(ftp://...) = %v; want scheme-not-allowed marker", err)
	}
}

func TestSetNpmRegistry_AcceptsCustomMirror(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	marker := markerFile(t)
	writeFakeNpmRecord(t, binDir, marker)
	if err := s.SetNpmRegistry("  https://my.mirror.corp/repo/  "); err != nil {
		t.Fatalf("SetNpmRegistry = %v", err)
	}
	if got := readMarker(t, marker); got != "config set registry https://my.mirror.corp/repo/" {
		t.Fatalf("npm argv = %q; want trimmed URL preserved", got)
	}
}

func TestValidateRegistryURL(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"https://registry.npmjs.org", "https://registry.npmjs.org", false},
		{"  https://registry.npmmirror.com  ", "https://registry.npmmirror.com", false},
		{"http://localhost:4873/", "http://localhost:4873/", false},
		{"", "", true},
		{"   ", "", true},
		{"no-scheme.example.com", "", true},
		{"ftp://example.com", "", true},
		{"file:///tmp/x", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := validateRegistryURL(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateRegistryURL(%q) error = %v; wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("validateRegistryURL(%q) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateNpmPackageName(t *testing.T) {
	valid := []string{"typescript", "@types/react", "eslint-plugin-x.y", "a-b_c.d", "0install"}
	for _, name := range valid {
		if err := validateNpmPackageName(name); err != nil {
			t.Errorf("validateNpmPackageName(%q) = %v; want nil", name, err)
		}
	}
	long := strings.Repeat("a", 215)
	invalid := []string{"", "Typescript", ".hidden", "_x", "a b", "foo;rm", "../x", "@/x", "@scope/", long}
	for _, name := range invalid {
		err := validateNpmPackageName(name)
		if err == nil || !strings.HasPrefix(err.Error(), "[svc:invalid-package-name]") {
			t.Errorf("validateNpmPackageName(%q) = %v; want invalid-package-name marker", name, err)
		}
	}
}

func TestParseGlobalPackages(t *testing.T) {
	t.Run("happy path sorted with scoped", func(t *testing.T) {
		in := `{"name":"prefix","dependencies":{"zeta":{"version":"1.0.0"},"@scope/pkg":{"version":"2.1.0"},"alpha":{"version":"0.1.0"}}}`
		got, err := parseGlobalPackages(in)
		if err != nil {
			t.Fatalf("parseGlobalPackages = %v", err)
		}
		if len(got) != 3 || got[0].Name != "@scope/pkg" || got[1].Name != "alpha" || got[2].Name != "zeta" || got[2].Version != "1.0.0" {
			t.Fatalf("parsed = %+v; want sorted 3 entries", got)
		}
	})
	t.Run("empty and missing dependencies", func(t *testing.T) {
		for _, in := range []string{`{"name":"prefix"}`, `{"dependencies":{}}`} {
			got, err := parseGlobalPackages(in)
			if err != nil || len(got) != 0 {
				t.Fatalf("parseGlobalPackages(%s) = %+v, %v; want empty", in, got, err)
			}
		}
	})
	t.Run("skips versionless entries", func(t *testing.T) {
		got, err := parseGlobalPackages(`{"dependencies":{"broken":{"invalid":true},"ok":{"version":"1.0.0"}}}`)
		if err != nil || len(got) != 1 || got[0].Name != "ok" {
			t.Fatalf("parsed = %+v, %v; want only ok", got, err)
		}
	})
	t.Run("malformed json", func(t *testing.T) {
		if _, err := parseGlobalPackages("not json"); err == nil {
			t.Fatal("parseGlobalPackages(not json) = nil; want error")
		}
	})
}

// writeFakeNpmLs writes an npm fake answering `ls` with jsonOut and the given
// exit code, recording other invocations into marker.
func writeFakeNpmLs(t *testing.T, dir, jsonOut string, exitCode int, marker string) {
	t.Helper()
	win := "@echo off\r\n" +
		"if \"%1\"==\"ls\" (\r\n" +
		"  echo " + jsonOut + "\r\n" +
		"  exit /b " + fmtItoa(exitCode) + "\r\n" +
		")\r\n" +
		"echo %* > \"" + marker + "\"\r\nexit /b 0\r\n"
	unix := "#!/bin/sh\n" +
		"if [ \"$1\" = \"ls\" ]; then\n" +
		"  echo '" + jsonOut + "'\n" +
		"  exit " + fmtItoa(exitCode) + "\n" +
		"fi\n" +
		"echo \"$@\" > \"" + marker + "\"\nexit 0\n"
	writeFakeTool(t, dir, "npm", win, unix)
}

func fmtItoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n == 1 {
		return "1"
	}
	panic("unsupported exit code in test helper")
}

const lsJSON = `{"name":"prefix","dependencies":{"typescript":{"version":"5.5.0"},"nrm":{"version":"1.2.6"}}}`

func TestGetGlobalPackages_ParsesList(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeNpmLs(t, binDir, lsJSON, 0, markerFile(t))
	got, err := s.GetGlobalPackages("nodejs")
	if err != nil {
		t.Fatalf("GetGlobalPackages = %v", err)
	}
	if len(got) != 2 || got[0].Name != "nrm" || got[1].Name != "typescript" {
		t.Fatalf("packages = %+v; want nrm + typescript", got)
	}
}

func TestGetGlobalPackages_ExitOneWithValidJSON(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeNpmLs(t, binDir, lsJSON, 1, markerFile(t))
	got, err := s.GetGlobalPackages("nodejs")
	if err != nil {
		t.Fatalf("GetGlobalPackages = %v; want packages despite exit 1", err)
	}
	if len(got) != 2 {
		t.Fatalf("packages = %+v; want 2 entries", got)
	}
}

func TestGetGlobalPackages_GarbageOutput(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeNpmLs(t, binDir, "garbage-not-json", 1, markerFile(t))
	if _, err := s.GetGlobalPackages("nodejs"); err == nil || !strings.HasPrefix(err.Error(), "[svc:exec-failed]") {
		t.Fatalf("GetGlobalPackages = %v; want exec-failed marker", err)
	}
}

func TestGetGlobalPackages_NonNodeOrInactive(t *testing.T) {
	s, _ := newInstallTestService(t, "")
	got, err := s.GetGlobalPackages("nodejs")
	if err != nil || len(got) != 0 {
		t.Fatalf("inactive node = %+v, %v; want empty list", got, err)
	}
	s2, _ := newInstallTestService(t, "23.0.0")
	got, err = s2.GetGlobalPackages("python")
	if err != nil || len(got) != 0 {
		t.Fatalf("non-nodejs = %+v, %v; want empty list", got, err)
	}
}

func TestGlobalPackageOps_Argv(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	marker := markerFile(t)
	writeFakeNpmRecord(t, binDir, marker)
	if err := s.InstallGlobalPackage("typescript"); err != nil {
		t.Fatalf("InstallGlobalPackage = %v", err)
	}
	if got := readMarker(t, marker); got != "install -g typescript" {
		t.Fatalf("install argv = %q", got)
	}
	if err := s.UpdateGlobalPackage("@types/react"); err != nil {
		t.Fatalf("UpdateGlobalPackage = %v", err)
	}
	if got := readMarker(t, marker); got != "install -g @types/react@latest" {
		t.Fatalf("update argv = %q", got)
	}
	if err := s.UninstallGlobalPackage("@scope/pkg"); err != nil {
		t.Fatalf("UninstallGlobalPackage = %v", err)
	}
	if got := readMarker(t, marker); got != "uninstall -g @scope/pkg" {
		t.Fatalf("uninstall argv = %q", got)
	}
}

func TestGlobalPackageOps_Guards(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	marker := markerFile(t)
	writeFakeNpmRecord(t, binDir, marker)
	if err := s.InstallGlobalPackage("Bad Name"); err == nil || !strings.HasPrefix(err.Error(), "[svc:invalid-package-name]") {
		t.Fatalf("invalid name = %v; want invalid-package-name", err)
	}
	if err := s.UninstallGlobalPackage("npm"); err == nil || !strings.HasPrefix(err.Error(), "[svc:protected-package]") {
		t.Fatalf("uninstall npm = %v; want protected-package", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("npm ran despite guard rejection")
	}
	s2, _ := newInstallTestService(t, "")
	if err := s2.InstallGlobalPackage("typescript"); err == nil || !strings.HasPrefix(err.Error(), "[svc:need-sdk]") {
		t.Fatalf("no active node = %v; want need-sdk", err)
	}
}
