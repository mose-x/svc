package main

import (
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"svc/internal/cli"
)

// The agent skill in skills/svc is documentation an AI agent reads instead of
// --help, which makes it the one file in the repo that can be confidently
// wrong: an agent follows what it says rather than checking. These guards hold
// it to the same standard the READMEs are held to, and they run in both
// directions -- a command the skill invents is a command an agent will try,
// and a command the skill omits is one no agent will ever reach for.

// skillFiles are the two markdown files that name commands. agents/openai.yaml
// is UI metadata and names none.
var skillFiles = []string{
	"skills/svc/SKILL.md",
	"skills/svc/references/commands.md",
}

// skillCommands is the reference table: the file the completeness guard reads.
const skillCommands = "skills/svc/references/commands.md"

// legacyInvocations name commands main.go answers before cli.Dispatch sees
// argv, so they are real invocations that commandTree cannot report.
var legacyInvocations = []string{"init", "setup", "version"}

func readSkillFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// Same reason readReadme normalizes: a guard that compares whole lines must
	// not depend on how the file happened to be checked out.
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// skillCommandRE finds one `svc <words...>` mention. The word pattern is the
// shape every command name has -- lowercase, digits and hyphens -- so an
// argument placeholder (`<sdk>`), a flag (`--json`), a version (22.11.0, which
// starts with a digit but is followed by a dot) and a shell operator all end
// the match where the command name ends.
var skillCommandRE = regexp.MustCompile(`svc ((?:[a-z][a-z0-9-]*)(?: [a-z][a-z0-9-]*)*)`)

// knownCommand reports whether words name something svc really answers: a path
// in the live tree, or one of the legacy invocations main.go keeps.
func knownCommand(tree []string, words string) bool {
	parts := strings.Fields(words)
	for len(parts) > 0 {
		if slices.Contains(tree, strings.Join(parts, " ")) {
			return true
		}
		if len(parts) == 1 && slices.Contains(legacyInvocations, parts[0]) {
			return true
		}
		parts = parts[:len(parts)-1]
	}
	return false
}

// TestSkillNamesOnlyCommandsThatExist is the guard that matters most. The tree
// is walked rather than listed, so a command renamed or removed in internal/cli
// fails here instead of teaching an agent to run something that opens a window
// and blocks.
func TestSkillNamesOnlyCommandsThatExist(t *testing.T) {
	tree := commandTree(t)
	for _, file := range skillFiles {
		body := readSkillFile(t, file)
		matches := skillCommandRE.FindAllStringSubmatch(body, -1)
		if len(matches) == 0 {
			t.Fatalf("%s: no `svc <command>` mention at all; the skill cannot be doing its job", file)
		}
		for _, match := range matches {
			if !knownCommand(tree, match[1]) {
				t.Errorf("%s names `svc %s`, which is not in the command tree and not a legacy invocation", file, match[1])
			}
		}
	}
}

// TestSkillReferenceDocumentsEveryCliCommand is the other direction, and it is
// why the reference can stay the only place the full tree is spelled out for an
// agent: a command added to internal/cli and not to the skill fails here.
func TestSkillReferenceDocumentsEveryCliCommand(t *testing.T) {
	tree := commandTree(t)
	reference := strings.Join(documentedLines(readSkillFile(t, skillCommands)), "\n")
	for _, path := range tree {
		if !mentionsCommand(reference, path) {
			t.Errorf("%s has no table row for `svc %s`", skillCommands, path)
		}
	}
}

// TestSkillListsTheRealExitCodes keeps the skill's exit-code table honest in
// both directions, for the same reason the READMEs' table is guarded: an agent
// that keys a retry on a number the CLI never exits with will wait forever, and
// one that never sees 7 will report a busy lock as a failure.
func TestSkillListsTheRealExitCodes(t *testing.T) {
	want := []int{
		cli.ExitOK, cli.ExitError, cli.ExitUsage, cli.ExitNotFound,
		cli.ExitIntegrity, cli.ExitCanceled, cli.ExitNetwork, cli.ExitBusy,
	}
	body := readSkillFile(t, "skills/svc/SKILL.md")
	seen := map[int]bool{}
	for _, match := range exitCodeRowRE.FindAllStringSubmatch(body, -1) {
		code, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("unreadable exit code %q in the skill", match[1])
		}
		seen[code] = true
	}
	for _, code := range want {
		if !seen[code] {
			t.Errorf("skills/svc/SKILL.md has no exit-code row for %d", code)
		}
	}
	for code := range seen {
		if !slices.Contains(want, code) {
			t.Errorf("skills/svc/SKILL.md documents exit code %d, which internal/cli does not define", code)
		}
	}
}

// TestSkillFrontmatterIsLoadable pins the two fields a skill loader routes on.
// A description that spans lines or a name that disagrees with the folder makes
// the skill silently unselectable, which no runtime error would ever report.
func TestSkillFrontmatterIsLoadable(t *testing.T) {
	body := readSkillFile(t, "skills/svc/SKILL.md")
	if !strings.HasPrefix(body, "---\n") {
		t.Fatal("skills/svc/SKILL.md does not open with a frontmatter delimiter")
	}
	rest := body[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		t.Fatal("skills/svc/SKILL.md never closes its frontmatter")
	}
	fields := map[string]string{}
	for _, line := range strings.Split(rest[:end], "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found || strings.HasPrefix(line, " ") {
			// A wrapped or indented line means the value is not one line, which
			// is exactly what a loader that reads `key: value` cannot parse.
			if strings.TrimSpace(line) != "" {
				t.Errorf("frontmatter line %q is not a single-line `key: value` pair", line)
			}
			continue
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if fields["name"] != "svc" {
		t.Errorf("the skill's name is %q, want %q: the folder is skills/svc and the loader installs by that name", fields["name"], "svc")
	}
	if len(fields["description"]) < 40 {
		t.Errorf("the description is %d characters, too short to route on: %q", len(fields["description"]), fields["description"])
	}
}
