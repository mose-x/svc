package installer

import (
	"net/http"
	"testing"

	"svc/internal/sdk"
)

// stubFetcher is a minimal VersionFetcher for completePathInfo tests. Its
// verify command never exists on PATH, so version/binary resolution returns
// empty strings deterministically on every CI OS.
type stubFetcher struct{}

func (stubFetcher) FetchRemoteVersions() ([]sdk.VersionInfo, error) { return nil, nil }
func (stubFetcher) GetLocalStatus() (*sdk.SdkStatus, error)         { return &sdk.SdkStatus{}, nil }
func (stubFetcher) GetDownloadURL(string) (string, string, error)   { return "", "", nil }
func (stubFetcher) GetBinDirs() []string                            { return nil }
func (stubFetcher) GetExtraEnvVars() map[string]string              { return nil }
func (stubFetcher) VerifyCommand() (string, []string) {
	return "svc_test_nonexistent_cmd_xyz", []string{"--version"}
}
func (stubFetcher) Type() sdk.SdkType          { return sdk.NodeJS }
func (stubFetcher) SetHTTPClient(*http.Client) {}
func (stubFetcher) StripArchiveTopDir() bool   { return false }

func TestCompletePathInfoFillsEmptyFields(t *testing.T) {
	status := &sdk.SdkStatus{PathConfigured: true}
	completePathInfo(status, stubFetcher{})
	// The verify command does not exist, so both fields resolve to empty
	// without error; the contract is that completePathInfo never panics and
	// leaves non-PathConfigured statuses untouched.
	if status.PathVersion != "" {
		t.Errorf("PathVersion = %q; want empty for a missing command", status.PathVersion)
	}
	if status.PathBinary != "" {
		t.Errorf("PathBinary = %q; want empty for a missing command", status.PathBinary)
	}
}

func TestCompletePathInfoKeepsFetcherValues(t *testing.T) {
	status := &sdk.SdkStatus{
		PathConfigured: true,
		PathVersion:    "24.19.0",
		PathBinary:     "/home/user/.nvm.rust/shims/node",
	}
	completePathInfo(status, stubFetcher{})
	if status.PathVersion != "24.19.0" {
		t.Errorf("PathVersion = %q; want the fetcher-provided value kept", status.PathVersion)
	}
	if status.PathBinary != "/home/user/.nvm.rust/shims/node" {
		t.Errorf("PathBinary = %q; want the fetcher-provided value kept", status.PathBinary)
	}
}

func TestCompletePathInfoIgnoresNonPathStatus(t *testing.T) {
	status := &sdk.SdkStatus{PathConfigured: false, PathVersion: "x"}
	completePathInfo(status, stubFetcher{})
	if status.PathVersion != "x" {
		t.Errorf("PathVersion changed for a configured SDK: %q", status.PathVersion)
	}
	if status.PathBinary != "" {
		t.Errorf("PathBinary filled for a configured SDK: %q", status.PathBinary)
	}
	// nil status must not panic.
	completePathInfo(nil, stubFetcher{})
}
