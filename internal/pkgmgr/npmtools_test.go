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
