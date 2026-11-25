package cli

import (
	"os"
	"strconv"

	"github.com/mattn/go-isatty"
)

// Mode is the output contract a command runs under. The three modes are the
// only three shapes of output svc writes, and picking one is a pure decision
// over two facts: was --json passed, and is anybody watching stdout.
type Mode int

const (
	// ModeTTY is an interactive terminal: a redrawable progress bar, colour
	// where the terminal supports it, and confirmation prompts allowed.
	ModeTTY Mode = iota
	// ModePlain is a pipe, a redirect or CI: one line per milestone, no
	// colour, no cursor tricks, and no prompting.
	ModePlain
	// ModeJSON is machine-readable output for a caller that will parse it.
	ModeJSON
)

// String names the mode for logs and test failures.
func (m Mode) String() string {
	switch m {
	case ModeTTY:
		return "tty"
	case ModePlain:
		return "plain"
	case ModeJSON:
		return "json"
	default:
		return "Mode(" + strconv.Itoa(int(m)) + ")"
	}
}

// ResolveMode picks the mode. --json wins unconditionally: a caller that asked
// for JSON must get JSON even when it happens to be attached to a terminal,
// otherwise the same command would emit two different formats depending on
// whether it was run by hand or from an editor's task runner.
func ResolveMode(jsonFlag, stdoutTTY bool) Mode {
	if jsonFlag {
		return ModeJSON
	}
	if stdoutTTY {
		return ModeTTY
	}
	return ModePlain
}

// IsStdoutTTY reports whether stdout is a terminal. Progress is written to
// stderr, but stdout decides the mode: a command whose results are being piped
// is being consumed by a program, and a program does not want a progress bar.
func IsStdoutTTY() bool { return isTTY(os.Stdout) }

// IsStdinTTY reports whether stdin is a terminal, i.e. whether a human is
// present to answer a confirmation prompt. A destructive command run from a
// script has no way to answer, so it must fail and ask for --yes instead of
// hanging on a read that never returns.
func IsStdinTTY() bool { return isTTY(os.Stdin) }

// isTTY accepts Cygwin/MSYS terminals as well as real ones. Under mintty a
// Windows console handle is a pipe, so IsTerminal alone reports false and the
// CLI would refuse to prompt a user who is plainly sitting at a terminal.
func isTTY(file *os.File) bool {
	if file == nil {
		return false
	}
	fd := file.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// ColorInputs is everything colour depends on. Passing the values in rather
// than reading the environment here keeps the precedence testable on any host
// and in any order.
type ColorInputs struct {
	// NoColorFlag is --no-color.
	NoColorFlag bool
	// JSONMode is --json.
	JSONMode bool
	// StdoutTTY is whether stdout is a terminal.
	StdoutTTY bool
	// NoColorEnv is the raw value of $NO_COLOR.
	NoColorEnv string
	// ForceEnv is the raw value of $CLICOLOR_FORCE.
	ForceEnv string
}

// ResolveColor decides whether to emit ANSI sequences.
//
// The order is the two community conventions with an escape hatch on each end.
// An explicit --no-color and a non-empty $NO_COLOR both disable colour
// unconditionally: no-color.org defines presence, not value, as the signal, so
// NO_COLOR=0 still means "no colour". --json disables it because escape codes
// inside a JSON stream corrupt it. $CLICOLOR_FORCE then forces colour back on
// for the pager and CI-log cases where the output is not a terminal but is
// still destined for something that renders ANSI -- "0" is the documented off
// switch. With none of those in play, colour follows the terminal.
func ResolveColor(in ColorInputs) bool {
	if in.NoColorFlag {
		return false
	}
	if in.NoColorEnv != "" {
		return false
	}
	if in.JSONMode {
		return false
	}
	if in.ForceEnv != "" && in.ForceEnv != "0" {
		return true
	}
	return in.StdoutTTY
}
