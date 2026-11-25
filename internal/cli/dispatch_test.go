package cli

import (
	"slices"
	"strings"
	"testing"
)

func TestDispatch(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want Kind
	}{
		// The one behaviour of the original dispatcher that must not change.
		{"no arguments launches the GUI", nil, KindGUI},
		{"an empty slice launches the GUI", []string{}, KindGUI},

		{"init stays on the legacy path", []string{"init"}, KindLegacyInit},
		{"setup is an alias of init", []string{"setup"}, KindLegacyInit},
		{"init with extra arguments is still init", []string{"init", "--force"}, KindLegacyInit},

		{"version", []string{"version"}, KindLegacyVersion},
		{"-v", []string{"-v"}, KindLegacyVersion},
		{"--version", []string{"--version"}, KindLegacyVersion},
		{"version with a flag", []string{"version", "--verbose"}, KindLegacyVersion},

		{"help", []string{"help"}, KindLegacyHelp},
		{"-h", []string{"-h"}, KindLegacyHelp},
		{"--help", []string{"--help"}, KindLegacyHelp},

		// Everything else belongs to the command tree, including a global flag
		// in first position and including a misspelling: opening a window
		// because someone typo'd `instal` is the failure this table prevents.
		{"list", []string{"list"}, KindCommand},
		{"install with arguments", []string{"install", "nodejs", "20.11.0"}, KindCommand},
		{"a global flag first", []string{"--json", "list"}, KindCommand},
		{"a misspelled command", []string{"instal"}, KindCommand},
		{"the hidden refresh carrier", []string{"__refresh", "go"}, KindCommand},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Dispatch(c.args); got != c.want {
				t.Errorf("Dispatch(%q) = %v, want %v", c.args, got, c.want)
			}
		})
	}
}

func TestKindString(t *testing.T) {
	want := map[Kind]string{
		KindGUI:           "gui",
		KindLegacyInit:    "legacy-init",
		KindLegacyVersion: "legacy-version",
		KindLegacyHelp:    "legacy-help",
		KindCommand:       "command",
		Kind(42):          "Kind(42)",
	}
	for kind, name := range want {
		if got := kind.String(); got != name {
			t.Errorf("Kind(%d).String() = %q, want %q", int(kind), got, name)
		}
	}
}

// TestLegacyCommandsMatchMainGuardsTheRedLine pins the legacy spelling list to
// the exact set main.go switched on before the CLI existed. Adding a spelling
// here routes an argument away from the command tree; removing one sends a
// long-standing invocation to cobra, where it becomes "unknown command".
func TestLegacyCommandsMatchMain(t *testing.T) {
	// Byte order, which puts both dash-dash spellings ahead of the short ones.
	want := []string{"--help", "--version", "-h", "-v", "help", "init", "setup", "version"}
	got := make([]string, 0, len(legacyCommands))
	for name := range legacyCommands {
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("legacyCommands = %v, want exactly %v", got, want)
	}
}

// TestNoCommandNameCollidesWithAnSdkBinary is the other half of §4.6: a new
// subcommand must not share a name with a command an SDK installs, or a user
// reading `svc --help` cannot tell whether `svc go` means the SDK or a
// subcommand. The shim itself dispatches on argv[0], so this is about clarity
// rather than a functional clash -- which is exactly why it needs a test
// instead of a reviewer's memory.
func TestNoCommandNameCollidesWithAnSdkBinary(t *testing.T) {
	sdkBinaries := []string{
		"node", "npm", "npx", "yarn", "pnpm", "go", "gofmt", "cargo", "rustc", "rustup",
		"java", "javac", "mvn", "gradle", "python", "python3", "pip", "pip3", "php",
		"composer", "dotnet", "flutter", "dart", "perl", "cpan", "adb", "sdkmanager",
	}

	r := &runner{in: ExecuteInput{Catalogs: shippedCatalogs(t)}}
	r.applyDefaults()
	root := r.newRootCommand()
	// cobra adds its own help and completion commands during Execute, not at
	// construction time, so ask for them explicitly before enumerating.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd("completion")

	for _, command := range root.Commands() {
		names := append([]string{command.Name()}, command.Aliases...)
		for _, name := range names {
			if slices.Contains(sdkBinaries, name) {
				t.Errorf("the %q command shadows an SDK binary of the same name", name)
			}
			if _, isLegacy := legacyCommands[name]; isLegacy {
				t.Errorf("the %q command shadows a legacy spelling that must keep its original behaviour", name)
			}
		}
	}

	// An empty tree would pass forever, so assert the enumeration really saw
	// something. Every command P3 adds is then covered without touching this
	// test again.
	names := make([]string, 0, len(root.Commands()))
	for _, command := range root.Commands() {
		names = append(names, command.Name())
	}
	slices.Sort(names)
	// Only `completion` is assertable today: cobra's InitDefaultHelpCmd returns
	// early when the root has no subcommands, so `help` joins the list on its
	// own once P3 registers the first real command.
	if !slices.Contains(names, "completion") {
		t.Errorf("the root command exposes [%s]; the enumeration saw nothing worth checking", strings.Join(names, " "))
	}
}
