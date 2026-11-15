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
// if either README stops documenting an install path that actually works:
// `brew trust mose-x/tap` first, then `brew tap`, then the fully qualified
// `mose-x/tap/svc`. Homebrew 7 refuses every entry of an untrusted tap, the
// fully qualified name included, so trusting is no longer a hint for short-name
// users and its position in the block matters.

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
		// The short name only resolves once the tap is both tapped and trusted,
		// and stays ambiguous against any other tap that ships a `svc`. The
		// fully qualified form has neither problem, so that is what gets
		// documented.
		for _, line := range strings.Split(block, "\n") {
			switch cmd := strings.TrimSpace(line); cmd {
			case "brew install svc", "brew install --cask svc":
				t.Errorf("%s: documents the short name %q, which needs a trusted "+
					"and tapped mose-x/tap and is ambiguous besides", readme.file, cmd)
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
			// Homebrew 7 requires trust for the fully qualified name too, and
			// the record is per user rather than per tap checkout. Both facts
			// have to be spelled out, or readers assume one `brew trust` covers
			// every install form and every machine.
			"Homebrew 7",
			"trust.json",
		} {
			if !strings.Contains(section, want) {
				t.Errorf("%s: the Homebrew section must mention %q", readme.file, want)
			}
		}
	}
}

// TestReadmesTrustTheTapBeforeInstalling guards the order of the copy-paste
// block. `brew trust` is idempotent and does not need the tap to exist yet, so
// trusting first is the only ordering that is right both on a machine that
// never saw mose-x/tap and on one that already tapped it.
func TestReadmesTrustTheTapBeforeInstalling(t *testing.T) {
	for _, readme := range homebrewReadmes {
		section := homebrewSection(t, readme.file, readme.sectionEnd)
		block := firstBashBlock(t, readme.file, section)
		trustLine, installLine := -1, -1
		for i, line := range strings.Split(block, "\n") {
			switch strings.TrimSpace(line) {
			case "brew trust mose-x/tap":
				trustLine = i
			case "brew install --cask mose-x/tap/svc":
				installLine = i
			}
		}
		if trustLine < 0 {
			t.Errorf("%s: the Homebrew block must run `brew trust mose-x/tap`, got:\n%s", readme.file, block)
			continue
		}
		if installLine >= 0 && trustLine > installLine {
			t.Errorf("%s: `brew trust mose-x/tap` must precede the install line, got:\n%s", readme.file, block)
		}
	}
}
