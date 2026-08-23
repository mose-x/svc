package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildWindowsScriptSanitizesPrereleaseVersions pins the fix for
// prerelease tags (e.g. v2.0.1-rc1): NSIS VIProductVersion/VIFileVersion —
// fed by wails.json productVersion through wails_tools.nsh — and the winres
// VERSIONINFO "fixed" fields only accept numeric X.X.X.X. The script must
// strip any "-rc1"/"+build" suffix before deriving those values, otherwise
// makensis aborts with "invalid VIFileVersion format". about.json and the
// asset names keep the full version.
func TestBuildWindowsScriptSanitizesPrereleaseVersions(t *testing.T) {
	root := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "scripts", "build-windows.sh"))
	if err != nil {
		t.Fatalf("failed to read scripts/build-windows.sh: %v", err)
	}
	content := string(data)

	checks := []struct {
		name string
		want string
	}{
		{
			name: "suffix stripped into BASE_VER",
			want: `BASE_VER="${VERSION%%[-+]*}"`,
		},
		{
			name: "WIN_VER derived from the stripped version",
			want: `echo "$BASE_VER"`,
		},
		{
			name: "wails.json productVersion uses the stripped version",
			want: `jq --arg v "$BASE_VER" '.info.productVersion = $v'`,
		},
		{
			name: "about.json keeps the full version",
			want: `jq --arg v "$VERSION" '.version = $v'`,
		},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(content, c.want) {
				t.Errorf("scripts/build-windows.sh missing %q", c.want)
			}
		})
	}

	// Regression guard: the raw version must NOT feed the numeric fields.
	for _, bad := range []string{
		`jq --arg v "$VERSION" '.info.productVersion = $v'`,
		`WIN_VER=$(echo "$VERSION"`,
	} {
		if strings.Contains(content, bad) {
			t.Errorf("scripts/build-windows.sh feeds the raw version into a numeric field: %q", bad)
		}
	}
}
