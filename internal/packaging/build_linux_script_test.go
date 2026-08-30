package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildLinuxScriptSortsPrereleaseBelowStable pins the fix for rc package
// ordering: apt/dnf sort "2.0.2-rc2" ABOVE the final "2.0.2" (an absent
// debian revision sorts before any present one; rpmvercmp behaves the same),
// which strands rc users with no upgrade path to the stable release. The
// script must convert the first hyphen to a tilde ("2.0.2~rc2") before
// feeding fpm, because the tilde sorts below everything in both dpkg and
// rpmvercmp. Asset file names and about.json/wails.json keep the full
// version; only the deb/rpm metadata uses the tilde form.
func TestBuildLinuxScriptSortsPrereleaseBelowStable(t *testing.T) {
	root := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "scripts", "build-linux.sh"))
	if err != nil {
		t.Fatalf("failed to read scripts/build-linux.sh: %v", err)
	}
	content := string(data)

	checks := []struct {
		name string
		want string
	}{
		{
			name: "hyphen converted to tilde into PKG_VER via sed",
			want: `PKG_VER="$(printf '%s' "$VERSION" | sed 's/-/~/')"`,
		},
		{
			name: "deb fpm uses the tilde version",
			want: `-n svc -v "${PKG_VER}" -a "${DEB_ARCH}"`,
		},
		{
			name: "rpm fpm uses the tilde version",
			want: `-n svc -v "${PKG_VER}" -a "${RPM_ARCH}"`,
		},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(content, c.want) {
				t.Errorf("scripts/build-linux.sh missing %q", c.want)
			}
		})
	}

	// Execute the conversion the way the script does and assert the actual
	// output: the earlier ${VERSION/-/~} form tilde-expanded the replacement
	// at runtime (yielding $HOME paths) and broke the release build.
	bashPath, err := exec.LookPath("bash")
	if err == nil {
		for in, want := range map[string]string{
			"2.0.2-rc3": "2.0.2~rc3",
			"2.0.2":     "2.0.2",
		} {
			out, err := exec.Command(bashPath, "-c",
				`VERSION="`+in+`"; printf '%s' "$VERSION" | sed 's/-/~/'`).Output()
			if err != nil {
				t.Fatalf("conversion run failed for %s: %v", in, err)
			}
			if got := strings.TrimSpace(string(out)); got != want {
				t.Fatalf("conversion(%q) = %q; want %q", in, got, want)
			}
		}
	}

	// Regression guard: the raw version (with hyphen) must not feed fpm.
	if strings.Contains(content, `-n svc -v "${VERSION}"`) {
		t.Error("scripts/build-linux.sh feeds the raw hyphenated version into fpm")
	}
}

// TestBuildScriptsBashSyntax runs `bash -n` over every shell script in
// scripts/ (agents.md: shell changes require at least a syntax check).
// Skipped where bash is unavailable.
func TestBuildScriptsBashSyntax(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}
	root := findRepoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "scripts"))
	if err != nil {
		t.Fatalf("read scripts dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			script := filepath.Join(root, "scripts", e.Name())
			out, err := exec.Command(bashPath, "-n", script).CombinedOutput()
			if err != nil {
				t.Fatalf("bash -n %s failed: %v\n%s", e.Name(), err, out)
			}
		})
	}
}
