package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"svc/internal/appcore"
	"svc/internal/cli"
	"svc/internal/sdk"
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
	content := readReadme(t, file)
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

// TestReadmesSayTheCaskAlsoInstallsTheCommandLine pins the two facts a reader
// needs now that the cask links a binary: where the command comes from, and
// that Gatekeeper kills it until the app has been approved once from Finder.
// Without the second one the documented install looks broken rather than
// unfinished -- `svc list` exits 137 printing nothing at all, which reads as a
// bad build instead of a missing approval.
func TestReadmesSayTheCaskAlsoInstallsTheCommandLine(t *testing.T) {
	for _, readme := range homebrewReadmes {
		section := homebrewSection(t, readme.file, readme.sectionEnd)
		for _, want := range []string{
			"/usr/local/bin/svc",
			"Contents/MacOS/svc",
			"137",
		} {
			if !strings.Contains(section, want) {
				t.Errorf("%s: the Homebrew section must mention %q", readme.file, want)
			}
		}
	}
}

// The CLI chapter documents a command tree that lives in internal/cli, so
// every guard below reads the tree by running it rather than from a list kept
// here. A list would be the third copy of the same names after cobra's and the
// READMEs', and the point of the pair is that there is only one.

// cliReadmes pairs each README with the line that starts its command-line
// chapter and the line that ends it. Both files carry the chapter at the same
// place in the same shape, which TestReadmeCliChaptersStayParallel enforces.
var cliReadmes = []struct {
	file       string
	section    string
	sectionEnd string
	// flags is the subsection that holds the persistent-flag table. It is
	// named separately because a flag mentioned anywhere in the chapter proves
	// far too little: `--no-wait` also turns up in the exit-code table and in
	// the concurrency prose, so a chapter could lose its flag row and still
	// "mention" the flag twice.
	flags string
}{
	{file: "README.md", section: "## Command Line", sectionEnd: "## Comparison with Alternatives", flags: "### Global Flags"},
	{file: "README.ZH_CN.md", section: "## \u547d\u4ee4\u884c", sectionEnd: "## \u4e0e\u540c\u7c7b\u5de5\u5177\u7684\u5bf9\u6bd4\u4f18\u52bf", flags: "### \u5168\u5c40\u53c2\u6570"},
}

// subsection returns the lines of a chapter from one "### " heading up to the
// next heading of any level. Headings are whole lines, so a "###" inside a
// fenced block cannot end the section early -- none of the chapters has one,
// and a future one that does should fail here rather than silently truncate.
func subsection(chapter, heading string) string {
	lines := strings.Split(chapter, "\n")
	start := -1
	for i, line := range lines {
		if line == heading {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	stop := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "#") {
			stop = i
			break
		}
	}
	return strings.Join(lines[start:stop], "\n")
}

// readReadme returns one README with its line endings normalized to LF.
//
// .gitattributes forces LF for markdown, but a guard that depends on a
// checkout setting staying the way it is, is a guard that fails on a machine
// nobody tested it on -- and the failure is the confusing kind, because the
// heading it reports as missing is visibly there and only the trailing \r
// differs. Normalizing at the read makes every whole-line comparison below
// platform-independent.
func readReadme(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// sectionBetween returns the lines of a file from the line equal to `from` up
// to (excluding) the line equal to `to`, or to the end of the file when `to`
// never appears. Markdown headings are whole lines, and matching them as whole
// lines is what keeps the Features bullet block -- which is named almost the
// same thing -- out of the result.
func sectionBetween(t *testing.T, file, from, to string) string {
	t.Helper()
	lines := strings.Split(readReadme(t, file), "\n")
	start := -1
	for i, line := range lines {
		if line == from {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s: no line equal to %q", file, from)
	}
	stop := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == to {
			stop = i
			break
		}
	}
	return strings.Join(lines[start:stop], "\n")
}

// cliHelp runs the real command tree in-process and returns what one --help
// invocation printed on stdout.
//
// OpenHome is injected with a stub that fails the test rather than with a
// temporary directory. cobra answers --help before PersistentPreRunE runs, so a
// help invocation has no business resolving a run configuration at all; if a
// cobra upgrade ever reorders that, this fails loudly instead of quietly
// reading the developer's real ~/.svc.
func cliHelp(t *testing.T, path ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args := append(append([]string{}, path...), "--help")
	code := cli.Execute(cli.ExecuteInput{
		Args:      args,
		Catalogs:  cliCatalogs(),
		AboutJSON: aboutJSON,
		Stdin:     strings.NewReader(""),
		Stdout:    &stdout,
		Stderr:    &stderr,
		OpenHome: func([]byte) (*appcore.Home, error) {
			t.Errorf("`svc %s` resolved a run configuration in order to print help; --help must be answered before any SVC home is opened", strings.Join(path, " "))
			return nil, errors.New("printing help needs no SVC home")
		},
	})
	if code != cli.ExitOK {
		t.Fatalf("`svc %s` exited %d, want %d; stderr:\n%s", strings.Join(args, " "), code, cli.ExitOK, stderr.String())
	}
	return stdout.String()
}

// helpBlock returns the indented lines under one "Heading:" line of a help
// output, stopping at the first blank line. It reads both the command list and
// the flag list, which cobra formats the same way.
func helpBlock(help, heading string) []string {
	_, rest, found := strings.Cut(help, heading+"\n")
	if !found {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(rest, "\n") {
		if strings.TrimSpace(line) == "" {
			break
		}
		lines = append(lines, line)
	}
	return lines
}

// subcommandNames lists the commands under one help output's "Available
// Commands:" heading, in cobra's own order. A leaf command has no such heading
// and therefore no subcommands.
func subcommandNames(help string) []string {
	var names []string
	for _, line := range helpBlock(help, "Available Commands:") {
		name, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// longFlagRE finds the long name at the start of one flag row. It is anchored
// to the line and to the row's own layout -- an optional short name, then the
// long one -- so that a "--" inside a description cannot be mistaken for a
// second flag.
var longFlagRE = regexp.MustCompile(`^\s+(?:-\S,\s+)?(--[a-z][a-z0-9-]*)`)

// commandTree walks the tree by asking it about itself, one --help per level,
// and returns every command path below the root, sorted. Hidden commands are
// absent because cobra omits them from "Available Commands:", which is exactly
// right: `svc __refresh` is a carrier no user is meant to type and no README
// should teach.
func commandTree(t *testing.T) []string {
	t.Helper()
	var paths []string
	var walk func(prefix []string)
	walk = func(prefix []string) {
		for _, name := range subcommandNames(cliHelp(t, prefix...)) {
			path := append(append([]string{}, prefix...), name)
			paths = append(paths, strings.Join(path, " "))
			walk(path)
		}
	}
	walk(nil)
	sort.Strings(paths)
	return paths
}

// documentedLines returns only the lines of a chapter that count as
// documenting a command: markdown table rows and the contents of fenced code
// blocks.
//
// Prose is excluded on purpose. A command named only inside a sentence is not
// something a reader can scan for, and crediting prose would let a reference
// row be deleted unnoticed as long as some paragraph still happened to mention
// the command -- which is precisely the edit a guard has to catch, because it
// is the one that looks harmless in review.
func documentedLines(section string) []string {
	var lines []string
	inFence := false
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(line, "|") {
			lines = append(lines, line)
		}
	}
	return lines
}

// mentionsCommand reports whether a section names `svc <path>` as a command.
// The character following the path has to end the name, so documenting
// `svc list` is not credited to some future `svc listener`, while the ways the
// READMEs really do end a command name -- a closing backtick, the space before
// an argument placeholder, the space before a comment inside a fenced block --
// all count.
func mentionsCommand(section, path string) bool {
	needle := "svc " + path
	for at := 0; ; {
		found := strings.Index(section[at:], needle)
		if found < 0 {
			return false
		}
		at += found
		next := at + len(needle)
		at = next
		if next >= len(section) {
			return true
		}
		switch section[next] {
		case '`', ' ', '\n', '.', ',', ')', ':':
			return true
		}
	}
}

// TestReadmesDocumentEveryCliCommand is the guard the CLI chapter exists
// behind: a command added to internal/cli and not to both READMEs fails here,
// and so does a command documented in only one of the two languages. It walks
// the tree rather than listing it, so it cannot go stale the way a copied list
// would.
func TestReadmesDocumentEveryCliCommand(t *testing.T) {
	paths := commandTree(t)
	// A floor rather than an exact count, so the assertion catches a walk that
	// silently stopped descending without failing every time a command lands.
	if len(paths) < 40 {
		t.Fatalf("walked %d command paths, far fewer than this repo ships; the walk is probably broken:\n%s",
			len(paths), strings.Join(paths, "\n"))
	}
	for _, readme := range cliReadmes {
		section := sectionBetween(t, readme.file, readme.section, readme.sectionEnd)
		reference := strings.Join(documentedLines(section), "\n")
		for _, path := range paths {
			if !mentionsCommand(reference, path) {
				t.Errorf("%s: the %q chapter has no table row or example for `svc %s`", readme.file, readme.section, path)
			}
		}
	}
}

// TestReadmesDocumentEveryGlobalFlag pins the persistent-flag table to the
// flags the root command actually declares. A new global flag that changes how
// every command behaves is exactly the kind of thing a script author needs to
// be told about in both languages.
func TestReadmesDocumentEveryGlobalFlag(t *testing.T) {
	var flags []string
	for _, line := range helpBlock(cliHelp(t), "Flags:") {
		if match := longFlagRE.FindStringSubmatch(line); match != nil {
			flags = append(flags, match[1])
		}
	}
	if len(flags) < 7 {
		t.Fatalf("found %d global flags (%v), want at least the 7 the READMEs document", len(flags), flags)
	}
	for _, readme := range cliReadmes {
		section := sectionBetween(t, readme.file, readme.section, readme.sectionEnd)
		table := subsection(section, readme.flags)
		if table == "" {
			t.Fatalf("%s: the %q chapter has no %q subsection", readme.file, readme.section, readme.flags)
		}
		rows := strings.Join(documentedLines(table), "\n")
		for _, flag := range flags {
			if !strings.Contains(rows, flag) {
				t.Errorf("%s: the %q table has no row for the global flag %s", readme.file, readme.flags, flag)
			}
		}
	}
}

// exitCodeRowRE finds the code cell of one row of the exit-code table.
var exitCodeRowRE = regexp.MustCompile(`(?m)^\|\s*(\d+)\s*\|`)

// TestReadmesDocumentEveryExitCode pins both exit-code tables to the constants
// in internal/cli. The codes are the only machine-readable promise the CLI
// makes, so a README that lists seven of the eight teaches a script to mistake
// the missing one for a generic failure.
func TestReadmesDocumentEveryExitCode(t *testing.T) {
	want := []int{
		cli.ExitOK, cli.ExitError, cli.ExitUsage, cli.ExitNotFound,
		cli.ExitIntegrity, cli.ExitCanceled, cli.ExitNetwork, cli.ExitBusy,
	}
	for _, readme := range cliReadmes {
		section := sectionBetween(t, readme.file, readme.section, readme.sectionEnd)
		seen := map[int]bool{}
		for _, match := range exitCodeRowRE.FindAllStringSubmatch(section, -1) {
			code, err := strconv.Atoi(match[1])
			if err != nil {
				t.Fatalf("%s: unreadable exit code %q", readme.file, match[1])
			}
			seen[code] = true
		}
		for _, code := range want {
			if !seen[code] {
				t.Errorf("%s: the %q chapter has no exit-code row for %d", readme.file, readme.section, code)
			}
		}
		// The other direction: a row for a code the CLI does not define teaches
		// a script to wait for a number that can never arrive.
		for code := range seen {
			if !slices.Contains(want, code) {
				t.Errorf("%s: the %q chapter documents exit code %d, which internal/cli does not define", readme.file, readme.section, code)
			}
		}
	}
}

// TestReadmesStateTheRealSdkCount keeps the two places each README states how
// many SDKs exist honest against sdk.AllSdkTypes, and insists that the feature
// bullet lists every one of them by the display name the GUI shows.
func TestReadmesStateTheRealSdkCount(t *testing.T) {
	types := sdk.AllSdkTypes()
	count := strconv.Itoa(len(types))
	readmes := []struct {
		file       string
		bullet     string
		tableRow   string
		bulletMark string
	}{
		{"README.md", "**%s SDKs Supported**", "Unified SDK management (%s types)", " SDKs Supported**"},
		{"README.ZH_CN.md", "**\u652f\u6301 %s \u79cd SDK**", "SDK \u7edf\u4e00\u7ba1\u7406\uff08%s \u79cd\uff09", " \u79cd SDK**"},
	}
	for _, readme := range readmes {
		content := readReadme(t, readme.file)
		bullet := fmt.Sprintf(readme.bullet, count)
		if !strings.Contains(content, bullet) {
			t.Errorf("%s: must state %q; sdk.AllSdkTypes has %d entries", readme.file, bullet, len(types))
		}
		row := fmt.Sprintf(readme.tableRow, count)
		if !strings.Contains(content, row) {
			t.Errorf("%s: the comparison table must state %q", readme.file, row)
		}
		var line string
		for _, candidate := range strings.Split(content, "\n") {
			if strings.Contains(candidate, readme.bulletMark) {
				line = candidate
				break
			}
		}
		if line == "" {
			t.Fatalf("%s: no feature bullet ends with %q", readme.file, readme.bulletMark)
		}
		for _, kind := range types {
			if name := sdk.SdkDisplayName(kind); !strings.Contains(line, name) {
				t.Errorf("%s: the SDK bullet does not name %s (%s)", readme.file, name, kind)
			}
		}
	}
}

// TestReadmesDocumentTheLegacyInvocations ties the compatibility table to
// cli.Dispatch, the single place that decides which spellings stay on the
// pre-CLI code path. The dispatch assertions come first: documenting a
// spelling that no longer behaves the way the README says is worse than not
// documenting it.
func TestReadmesDocumentTheLegacyInvocations(t *testing.T) {
	legacy := []struct {
		args []string
		want cli.Kind
	}{
		{nil, cli.KindGUI},
		{[]string{"init"}, cli.KindLegacyInit},
		{[]string{"setup"}, cli.KindLegacyInit},
		{[]string{"version"}, cli.KindLegacyVersion},
		{[]string{"-v"}, cli.KindLegacyVersion},
		{[]string{"--version"}, cli.KindLegacyVersion},
	}
	for _, readme := range cliReadmes {
		section := sectionBetween(t, readme.file, readme.section, readme.sectionEnd)
		for _, entry := range legacy {
			if got := cli.Dispatch(entry.args); got != entry.want {
				t.Fatalf("cli.Dispatch(%q) = %s, want %s; the READMEs document a dispatch table that no longer exists",
					strings.Join(entry.args, " "), got, entry.want)
			}
			if len(entry.args) == 0 {
				// `svc` on its own is documented as a table row of its own, and
				// a bare "svc" is far too common a substring to search for.
				if !strings.Contains(section, "| `svc` |") {
					t.Errorf("%s: the %q chapter has no row for bare `svc` launching the GUI", readme.file, readme.section)
				}
				continue
			}
			if needle := "`svc " + entry.args[0] + "`"; !strings.Contains(section, needle) {
				t.Errorf("%s: the %q chapter does not document %s", readme.file, readme.section, needle)
			}
		}
	}
}

// TestReadmeCliChaptersStayParallel keeps the two chapters the same shape. The
// per-command and per-flag guards already prove both languages say the same
// things about the tree; this one catches a whole subsection -- a table, a
// fenced example -- added to one file and forgotten in the other, which those
// guards would not notice when the subsection documents no new command name.
func TestReadmeCliChaptersStayParallel(t *testing.T) {
	type shape struct {
		file              string
		subsections       int
		tableSeparatorRow int
		fencedBlocks      int
	}
	var shapes []shape
	for _, readme := range cliReadmes {
		section := sectionBetween(t, readme.file, readme.section, readme.sectionEnd)
		var got shape
		got.file = readme.file
		fences := 0
		for _, line := range strings.Split(section, "\n") {
			switch {
			case strings.HasPrefix(line, "### "):
				got.subsections++
			case strings.HasPrefix(line, "```"):
				fences++
			case strings.HasPrefix(line, "|---"):
				got.tableSeparatorRow++
			}
		}
		if fences%2 != 0 {
			t.Errorf("%s: the %q chapter has %d fence lines, which is not a whole number of blocks", readme.file, readme.section, fences)
		}
		got.fencedBlocks = fences / 2
		shapes = append(shapes, got)
	}
	for _, got := range shapes[1:] {
		if got.subsections != shapes[0].subsections ||
			got.tableSeparatorRow != shapes[0].tableSeparatorRow ||
			got.fencedBlocks != shapes[0].fencedBlocks {
			t.Errorf("the CLI chapters have drifted apart: %+v vs %+v", shapes[0], got)
		}
	}
}
