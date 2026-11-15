package main

import (
	"os"
	"strings"
	"testing"
)

// The Homebrew cask lives in mose-x/homebrew-tap, not in this repo. A copy
// checked in here can only go stale: its sha256 values exist only once the
// release assets are published, while the tap recomputes them from those
// assets on a daily schedule. nvm-rust kept two such copies and they drifted
// four minor versions behind the release, one of them shipping
// `sha256 "TO_BE_FILLED"`, which makes `brew install` fail outright.
//
// These tests pin that decision down. They fail if a cask copy comes back, and
// if either README stops documenting the install path that actually works on
// Homebrew 6: the fully qualified `mose-x/tap/svc`, which loads without
// `brew trust`, plus the trust hint for anyone who wants the short name.

// homebrewReadmes pairs each README with the heading that ends its Homebrew
// section, so the assertions only ever look at that one section.
var homebrewReadmes = []struct {
	file       string
	sectionEnd string
}{
	{file: "README.md", sectionEnd: "## Self-Update Mechanism"},
	{file: "README.ZH_CN.md", sectionEnd: "## 自动更新机制"},
}

// homebrewSection returns the `### Homebrew` section of a README, from its
// heading up to (excluding) the heading that ends it.
func homebrewSection(t *testing.T, file, sectionEnd string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	content := string(raw)
	const heading = "### Homebrew"
	from := strings.Index(content, heading)
	if from < 0 {
		t.Fatalf("%s: missing %q section heading", file, heading)
	}
	section := content[from:]
	if to := strings.Index(section, sectionEnd); to >= 0 {
		section = section[:to]
	}
	return section
}

// firstBashBlock returns the first fenced ```bash block of a section, i.e. the
// commands a reader is meant to copy.
func firstBashBlock(t *testing.T, file, section string) string {
	t.Helper()
	_, afterFence, found := strings.Cut(section, "```bash")
	if !found {
		t.Fatalf("%s: the Homebrew section has no ```bash block", file)
	}
	block, _, terminated := strings.Cut(afterFence, "```")
	if !terminated {
		t.Fatalf("%s: unterminated ```bash block", file)
	}
	return block
}

func TestNoHomebrewEntryCopyInRepo(t *testing.T) {
	for _, name := range []string{
		"homebrew",
		"homebrew/svc.rb",
		"Casks",
		"Casks/svc.rb",
		"Formula",
		"Formula/svc.rb",
		"svc.rb",
	} {
		if _, err := os.Stat(name); err == nil {
			t.Errorf("%s must not come back: the cask lives in mose-x/homebrew-tap, "+
				"which recomputes the version and every sha256 from the published "+
				"release assets. A copy in this repo can only go stale.", name)
		}
	}
}

func TestReadmesInstallFromTheTapByItsFullName(t *testing.T) {
	for _, readme := range homebrewReadmes {
		section := homebrewSection(t, readme.file, readme.sectionEnd)
		block := firstBashBlock(t, readme.file, section)
		for _, want := range []string{"brew tap mose-x/tap", "brew install --cask mose-x/tap/svc"} {
			if !strings.Contains(block, want) {
				t.Errorf("%s: the Homebrew block must contain %q, got:\n%s", readme.file, want, block)
			}
		}
		// The bare short name is exactly what Homebrew 6 refuses to load from an
		// untrusted tap, so it must not be the documented install command.
		for _, line := range strings.Split(block, "\n") {
			switch cmd := strings.TrimSpace(line); cmd {
			case "brew install svc", "brew install --cask svc":
				t.Errorf("%s: documents the short name %q, which fails on Homebrew 6 "+
					"until the reader runs `brew trust mose-x/tap`", readme.file, cmd)
			}
		}
	}
}

func TestReadmesExplainTheHomebrewTrustRequirement(t *testing.T) {
	for _, readme := range homebrewReadmes {
		section := homebrewSection(t, readme.file, readme.sectionEnd)
		for _, want := range []string{
			"brew trust mose-x/tap",
			"brew untrust mose-x/tap",
			"mose-x/homebrew-tap",
		} {
			if !strings.Contains(section, want) {
				t.Errorf("%s: the Homebrew section must mention %q", readme.file, want)
			}
		}
	}
}
