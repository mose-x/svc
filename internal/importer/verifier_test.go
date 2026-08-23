package importer

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"svc/internal/sdk"
)

// fakeVersionFetcher implements sdk.VersionFetcher for postImportVerifier
// tests: VerifyCommand points at a tiny script written into sdkRoot/bin.
type fakeVersionFetcher struct{}

func (fakeVersionFetcher) FetchRemoteVersions() ([]sdk.VersionInfo, error) { return nil, nil }
func (fakeVersionFetcher) GetLocalStatus() (*sdk.SdkStatus, error)         { return nil, nil }
func (fakeVersionFetcher) GetDownloadURL(string) (string, string, error)   { return "", "", nil }
func (fakeVersionFetcher) GetBinDirs() []string                            { return []string{"bin"} }
func (fakeVersionFetcher) GetExtraEnvVars() map[string]string              { return nil }
func (fakeVersionFetcher) VerifyCommand() (string, []string)               { return "vercheck", nil }
func (fakeVersionFetcher) Type() sdk.SdkType                               { return "fakesdk" }
func (fakeVersionFetcher) SetHTTPClient(*http.Client)                      {}
func (fakeVersionFetcher) StripArchiveTopDir() bool                        { return true }

// writeVercheckScript writes an executable that prints "vercheck 9.9.9" so
// detectVersionFromDir parses version 9.9.9 from its output.
func writeVercheckScript(t *testing.T, sdkRoot string) {
	t.Helper()
	binDir := filepath.Join(sdkRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"vercheck 9.9.9\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "vercheck"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestPostImportVerifier(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no shebang-script mechanism to fake an executable
		// binary; the execution-based paths are covered on unix CI hosts.
		// The no-executable error path below still runs on Windows.
		t.Skip("execution-based fake requires a unix shell script")
	}
	sdkRoot := t.TempDir()
	writeVercheckScript(t, sdkRoot)
	s := &Service{}

	t.Run("matching version passes", func(t *testing.T) {
		if err := s.postImportVerifier(fakeVersionFetcher{}, "9.9.9")(sdkRoot); err != nil {
			t.Errorf("verifier = %v; want nil", err)
		}
	})

	t.Run("mismatched version rejected", func(t *testing.T) {
		err := s.postImportVerifier(fakeVersionFetcher{}, "1.0.0")(sdkRoot)
		if err == nil || !strings.Contains(err.Error(), "version mismatch") {
			t.Errorf("verifier = %v; want a version-mismatch error", err)
		}
	})
}

func TestPostImportVerifierMissingExecutable(t *testing.T) {
	s := &Service{}
	err := s.postImportVerifier(fakeVersionFetcher{}, "1.0.0")(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "post-import verification failed") {
		t.Errorf("verifier = %v; want a post-import verification failure", err)
	}
}
