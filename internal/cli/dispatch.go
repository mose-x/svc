package cli

import "strconv"

// Kind is what main should do with the command line.
//
// Splitting the decision out of main.go is what makes the compatibility red
// line testable. `svc` on its own must keep launching the window, and the four
// commands that predate the CLI must keep behaving exactly as they did; both
// are now assertions over a pure function instead of things a reviewer has to
// remember. Mapping each legacy spelling to its own Kind -- rather than to one
// "legacy" bucket that main switches over again -- keeps the argument-to-action
// table in exactly one place.
type Kind int

const (
	// KindGUI launches the Wails application.
	KindGUI Kind = iota
	// KindLegacyInit runs the one-time shim setup.
	KindLegacyInit
	// KindLegacyVersion prints the version line.
	KindLegacyVersion
	// KindLegacyHelp prints the built-in help text.
	KindLegacyHelp
	// KindCommand runs the cobra command tree.
	KindCommand
)

// String names the kind for logs and test failures.
func (k Kind) String() string {
	switch k {
	case KindGUI:
		return "gui"
	case KindLegacyInit:
		return "legacy-init"
	case KindLegacyVersion:
		return "legacy-version"
	case KindLegacyHelp:
		return "legacy-help"
	case KindCommand:
		return "command"
	default:
		return "Kind(" + strconv.Itoa(int(k)) + ")"
	}
}

// legacyCommands maps the exact argument spellings main.go switched on before
// the CLI existed to what each one did. They stay on that code path byte for
// byte: `svc version` in particular is parsed by scripts, so its output is
// frozen, and `svc help` still has nothing to show until the command tree has
// commands in it.
var legacyCommands = map[string]Kind{
	"init":      KindLegacyInit,
	"setup":     KindLegacyInit,
	"version":   KindLegacyVersion,
	"-v":        KindLegacyVersion,
	"--version": KindLegacyVersion,
	"help":      KindLegacyHelp,
	"-h":        KindLegacyHelp,
	"--help":    KindLegacyHelp,
}

// Dispatch decides which shell handles argv, excluding the program name.
//
// Only the first argument is consulted, which is what the original switch did.
// Global flags before a command (`svc --json list`) therefore land in the
// command tree, and an argument that names no legacy command is a CLI command
// -- including a misspelled one, which cobra reports as a usage error rather
// than silently opening a window.
func Dispatch(args []string) Kind {
	if len(args) == 0 {
		return KindGUI
	}
	if kind, ok := legacyCommands[args[0]]; ok {
		return kind
	}
	return KindCommand
}
