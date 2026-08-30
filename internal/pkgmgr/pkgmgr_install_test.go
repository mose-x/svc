package pkgmgr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"svc/internal/config"
	"svc/internal/sdk"
)

// Fake executable bodies. Windows uses .cmd batch scripts (found by
// resolveInPath's .cmd probe and executed via cmd.exe by os/exec); Unix uses
// #!/bin/sh scripts.

const (
	corepackOKWin   = "@echo off\r\nexit /b 0\r\n"
	corepackOKUnix  = "#!/bin/sh\nexit 0\n"
	corepackFailWin = "@echo off\r\necho corepack-fail-text\r\nexit /b 1\r\n"
	corepackFailUx  = "#!/bin/sh\necho corepack-fail-text\nexit 1\n"
	// Fails only on `prepare`, mimicking the pnpm registry signing-key
	// rotation that breaks corepack <= 0.29.4 ("Cannot find matching keyid").
	corepackPrepareFailWin = "@echo off\r\n" +
		"if \"%1\"==\"prepare\" (\r\n" +
		"  echo Cannot find matching keyid\r\n" +
		"  exit /b 1\r\n" +
		")\r\nexit /b 0\r\n"
	corepackPrepareFailUx = "#!/bin/sh\n" +
		"if [ \"$1\" = \"prepare\" ]; then\n" +
		"  echo \"Cannot find matching keyid\"\n" +
		"  exit 1\n" +
		"fi\nexit 0\n"
	// Fails only on `enable`; update flows must never call enable.
	corepackEnableFailWin = "@echo off\r\n" +
		"if \"%1\"==\"enable\" (\r\n" +
		"  echo enable-fail-text\r\n" +
		"  exit /b 1\r\n" +
		")\r\nexit /b 0\r\n"
	corepackEnableFailUx = "#!/bin/sh\n" +
		"if [ \"$1\" = \"enable\" ]; then\n" +
		"  echo enable-fail-text\n" +
		"  exit 1\n" +
		"fi\nexit 0\n"
	npmFailWin   = "@echo off\r\necho npm-fail-text\r\nexit /b 1\r\n"
	npmFailUnix  = "#!/bin/sh\necho npm-fail-text\nexit 1\n"
	failToolWin  = "@echo off\r\necho some-fail-text\r\nexit /b 1\r\n"
	failToolUnix = "#!/bin/sh\necho some-fail-text\nexit 1\n"
	longOutWin   = "@echo off\r\nfor /L %%i in (1,1,60) do echo 0123456789\r\nexit /b 1\r\n"
	longOutUnix  = "#!/bin/sh\ni=0\nwhile [ $i -lt 60 ]; do echo 0123456789; i=$((i+1)); done\nexit 1\n"
)

// newInstallTestService builds a Service rooted at a temp SVC home with the
// given nodejs version active ("" = none). binDir is the scoped node bin
// directory where fake corepack/npm executables go. PATH is scrubbed to
// binDir so a missing fake never resolves to a host binary.
func newInstallTestService(t *testing.T, nodeVersion string) (*Service, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg, err := config.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	cfg.SetHomeDir(home)
	binDir := filepath.Join(home, "fakebin")
	if nodeVersion != "" {
		if err := cfg.SetActiveVersion("nodejs", nodeVersion); err != nil {
			t.Fatalf("SetActiveVersion: %v", err)
		}
		binDir = cfg.SdkVersionDir("nodejs", nodeVersion)
		if !config.IsWindows() {
			binDir = filepath.Join(binDir, "bin") // NodejsFetcher.GetBinDirs()
		}
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("MkdirAll binDir: %v", err)
	}
	t.Setenv("PATH", binDir)
	return New(cfg, sdk.NewRegistry(cfg, nil)), binDir
}

// writeFakeTool writes a fake executable named name into dir: a .cmd batch
// script on Windows, a #!/bin/sh script elsewhere.
func writeFakeTool(t *testing.T, dir, name, winBody, unixBody string) {
	t.Helper()
	if config.IsWindows() {
		path := filepath.Join(dir, name+".cmd")
		if err := os.WriteFile(path, []byte(winBody), 0644); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
		return
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(unixBody), 0755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

// fakeNpmOK writes an npm fake that succeeds and touches marker so tests can
// assert whether the npm fallback actually ran.
func fakeNpmOK(t *testing.T, dir, marker string) {
	t.Helper()
	win := "@echo off\r\necho ran > \"" + marker + "\"\r\nexit /b 0\r\n"
	unix := "#!/bin/sh\necho ran > \"" + marker + "\"\nexit 0\n"
	writeFakeTool(t, dir, "npm", win, unix)
}

func markerFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "npm-ran")
}

func assertMarker(t *testing.T, marker string, wantPresent bool) {
	t.Helper()
	_, err := os.Stat(marker)
	if wantPresent && err != nil {
		t.Fatalf("expected npm fallback to run (marker %s missing)", marker)
	}
	if !wantPresent && err == nil {
		t.Fatalf("npm fallback ran unexpectedly (marker %s present)", marker)
	}
}

func TestInstallPackageManager_Guards(t *testing.T) {
	s, _ := newInstallTestService(t, "")
	for _, name := range []string{"pnpm", "yarn"} {
		err := s.InstallPackageManager(name)
		if err == nil || !strings.HasPrefix(err.Error(), "[svc:need-sdk]") {
			t.Errorf("InstallPackageManager(%q) = %v; want need-sdk marker", name, err)
		}
	}
	if err := s.InstallPackageManager("bogus"); err == nil || !strings.HasPrefix(err.Error(), "[svc:unknown-package-manager]") {
		t.Errorf("InstallPackageManager(bogus) = %v; want unknown-package-manager marker", err)
	}
	if err := s.UpdatePackageManager("bogus"); err == nil || !strings.HasPrefix(err.Error(), "[svc:unknown-package-manager]") {
		t.Errorf("UpdatePackageManager(bogus) = %v; want unknown-package-manager marker", err)
	}
}

func TestInstallPnpm_CorepackSuccess_NoFallback(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeTool(t, binDir, "corepack", corepackOKWin, corepackOKUnix)
	marker := markerFile(t)
	fakeNpmOK(t, binDir, marker)
	if err := s.InstallPackageManager("pnpm"); err != nil {
		t.Fatalf("InstallPackageManager(pnpm) = %v; want nil", err)
	}
	assertMarker(t, marker, false)
}

// The reported bug: corepack prepare fails signature verification
// ("Cannot find matching keyid") because corepack 0.29.4 predates the pnpm
// registry key rotation. Must fall back to npm install -g.
func TestInstallPnpm_PrepareFails_FallsBackToNpm(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeTool(t, binDir, "corepack", corepackPrepareFailWin, corepackPrepareFailUx)
	marker := markerFile(t)
	fakeNpmOK(t, binDir, marker)
	if err := s.InstallPackageManager("pnpm"); err != nil {
		t.Fatalf("InstallPackageManager(pnpm) = %v; want nil via npm fallback", err)
	}
	assertMarker(t, marker, true)
}

func TestInstallPnpm_EnableFails_FallsBackToNpm(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeTool(t, binDir, "corepack", corepackFailWin, corepackFailUx)
	marker := markerFile(t)
	fakeNpmOK(t, binDir, marker)
	if err := s.InstallPackageManager("pnpm"); err != nil {
		t.Fatalf("InstallPackageManager(pnpm) = %v; want nil via npm fallback", err)
	}
	assertMarker(t, marker, true)
}

func TestInstallYarn_PrepareFails_FallsBackToNpm(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeTool(t, binDir, "corepack", corepackPrepareFailWin, corepackPrepareFailUx)
	marker := markerFile(t)
	fakeNpmOK(t, binDir, marker)
	if err := s.InstallPackageManager("yarn"); err != nil {
		t.Fatalf("InstallPackageManager(yarn) = %v; want nil via npm fallback", err)
	}
	assertMarker(t, marker, true)
}

// Node >= 25 no longer ships corepack: the missing binary must fall back.
func TestInstallPnpm_NoCorepackBinary_FallsBackToNpm(t *testing.T) {
	s, binDir := newInstallTestService(t, "25.0.0")
	marker := markerFile(t)
	fakeNpmOK(t, binDir, marker)
	if err := s.InstallPackageManager("pnpm"); err != nil {
		t.Fatalf("InstallPackageManager(pnpm) = %v; want nil via npm fallback", err)
	}
	assertMarker(t, marker, true)
}

func TestInstallPnpm_AllFail_SurfacesExecFailedMarker(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeTool(t, binDir, "corepack", corepackPrepareFailWin, corepackPrepareFailUx)
	writeFakeTool(t, binDir, "npm", npmFailWin, npmFailUnix)
	err := s.InstallPackageManager("pnpm")
	if err == nil {
		t.Fatal("InstallPackageManager(pnpm) = nil; want error")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "[svc:exec-failed]") {
		t.Fatalf("error = %q; want exec-failed marker", msg)
	}
	if !strings.Contains(msg, "npm-fail-text") {
		t.Fatalf("error = %q; want final fallback output in detail", msg)
	}
}

func TestInstallPnpm_OldNode_UsesNpmDirectly(t *testing.T) {
	s, binDir := newInstallTestService(t, "14.17.0")
	writeFakeTool(t, binDir, "corepack", corepackFailWin, corepackFailUx)
	marker := markerFile(t)
	fakeNpmOK(t, binDir, marker)
	if err := s.InstallPackageManager("pnpm"); err != nil {
		t.Fatalf("InstallPackageManager(pnpm) = %v; want nil", err)
	}
	assertMarker(t, marker, true)
}

// Update must not run `corepack enable`; the enable-failing fake would
// trigger the npm fallback if enable were attempted.
func TestUpdatePnpm_CorepackSuccess_NoFallback(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeTool(t, binDir, "corepack", corepackEnableFailWin, corepackEnableFailUx)
	marker := markerFile(t)
	fakeNpmOK(t, binDir, marker)
	if err := s.UpdatePackageManager("pnpm"); err != nil {
		t.Fatalf("UpdatePackageManager(pnpm) = %v; want nil", err)
	}
	assertMarker(t, marker, false)
}

func TestUpdatePnpm_PrepareFails_FallsBackToNpm(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeTool(t, binDir, "corepack", corepackPrepareFailWin, corepackPrepareFailUx)
	marker := markerFile(t)
	fakeNpmOK(t, binDir, marker)
	if err := s.UpdatePackageManager("pnpm"); err != nil {
		t.Fatalf("UpdatePackageManager(pnpm) = %v; want nil via npm fallback", err)
	}
	assertMarker(t, marker, true)
}

func TestUpdatePnpm_NoCorepack_FallsBackToNpm(t *testing.T) {
	s, binDir := newInstallTestService(t, "25.0.0")
	marker := markerFile(t)
	fakeNpmOK(t, binDir, marker)
	if err := s.UpdatePackageManager("pnpm"); err != nil {
		t.Fatalf("UpdatePackageManager(pnpm) = %v; want nil via npm fallback", err)
	}
	assertMarker(t, marker, true)
}

func TestRunScopedCommand_FailureSurfacesExecFailedMarker(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeTool(t, binDir, "failtool", failToolWin, failToolUnix)
	err := s.runScopedCommand("failtool", sdk.NodeJS, "arg1")
	if err == nil {
		t.Fatal("runScopedCommand = nil; want error")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "[svc:exec-failed]") {
		t.Fatalf("error = %q; want exec-failed marker", msg)
	}
	if !strings.Contains(msg, `"cmd":"failtool arg1"`) {
		t.Fatalf("error = %q; want cmd param", msg)
	}
	if !strings.Contains(msg, "some-fail-text") {
		t.Fatalf("error = %q; want output tail in detail", msg)
	}
}

func TestRunScopedCommand_OutputTailTruncated(t *testing.T) {
	s, binDir := newInstallTestService(t, "23.0.0")
	writeFakeTool(t, binDir, "longtool", longOutWin, longOutUnix)
	err := s.runScopedCommand("longtool", sdk.NodeJS)
	if err == nil {
		t.Fatal("runScopedCommand = nil; want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "...23456789") || !strings.HasSuffix(msg, "0123456789\"}") {
		t.Fatalf("error = %q; want truncated tail ending with repeated digits", msg)
	}
	if len(msg) > 1200 {
		t.Fatalf("error length = %d; want bounded detail", len(msg))
	}
}

func TestRunScopedCommand_MissingBinary(t *testing.T) {
	s, _ := newInstallTestService(t, "23.0.0")
	err := s.runScopedCommand("no-such-tool-xyz", sdk.NodeJS)
	if err == nil {
		t.Fatal("runScopedCommand = nil; want error")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "[svc:exec-failed]") {
		t.Fatalf("error = %q; want exec-failed marker", msg)
	}
	if !strings.Contains(msg, "not found") {
		t.Fatalf("error = %q; want not-found detail", msg)
	}
}

func TestRunScopedCommand_Timeout(t *testing.T) {
	s, _ := newInstallTestService(t, "23.0.0")
	s.newCommandContext = func() (context.Context, context.CancelFunc) {
		return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	}
	err := s.runScopedCommand("anytool", sdk.NodeJS)
	if err == nil {
		t.Fatal("runScopedCommand = nil; want timeout error")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "[svc:exec-failed]") {
		t.Fatalf("error = %q; want exec-failed marker", msg)
	}
	if !strings.Contains(msg, "timed out") {
		t.Fatalf("error = %q; want timed out detail", msg)
	}
}
