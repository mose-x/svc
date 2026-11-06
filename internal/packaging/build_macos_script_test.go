package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildMacosScriptQuotesSipsSizeArgs pins the R-补 shellcheck fix (SC2086):
// the sips resize dimensions must be double-quoted. They are integers so runtime
// behavior is unchanged, but unquoted expansions trip shellcheck and are fragile
// under word splitting/globbing.
func TestBuildMacosScriptQuotesSipsSizeArgs(t *testing.T) {
	root := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "scripts", "build-macos.sh"))
	if err != nil {
		t.Fatalf("failed to read scripts/build-macos.sh: %v", err)
	}
	content := string(data)

	for _, w := range []string{
		`sips -z "$sz" "$sz" "$SRC"`,
		`sips -z "$((sz*2))" "$((sz*2))" "$SRC"`,
	} {
		if !strings.Contains(content, w) {
			t.Errorf("scripts/build-macos.sh missing quoted sips form %q", w)
		}
	}

	// Regression guards: the unquoted forms must be gone.
	for _, bad := range []string{
		`sips -z $sz $sz `,
		`sips -z $((sz*2)) $((sz*2)) `,
	} {
		if strings.Contains(content, bad) {
			t.Errorf("scripts/build-macos.sh still has unquoted sips args: %q", bad)
		}
	}
}
