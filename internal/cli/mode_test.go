package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveMode(t *testing.T) {
	cases := []struct {
		name       string
		jsonFlag   bool
		stdoutTTY  bool
		want       Mode
		wantString string
	}{
		{"terminal without --json is tty", false, true, ModeTTY, "tty"},
		{"pipe without --json is plain", false, false, ModePlain, "plain"},
		// --json must win even on a terminal: the same command has to produce
		// the same shape whether a human ran it by hand or a task runner did.
		{"--json on a terminal is json", true, true, ModeJSON, "json"},
		{"--json on a pipe is json", true, false, ModeJSON, "json"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveMode(c.jsonFlag, c.stdoutTTY)
			if got != c.want {
				t.Errorf("ResolveMode(%v, %v) = %v, want %v", c.jsonFlag, c.stdoutTTY, got, c.want)
			}
			if got.String() != c.wantString {
				t.Errorf("Mode(%d).String() = %q, want %q", int(got), got.String(), c.wantString)
			}
		})
	}
}

func TestModeStringNamesUnknownValues(t *testing.T) {
	if got := Mode(42).String(); got != "Mode(42)" {
		t.Errorf("Mode(42).String() = %q, want %q", got, "Mode(42)")
	}
	if ModePlain.String() != "plain" {
		t.Error("the zero-ish modes must keep their names; String is used in test output")
	}
}

func TestResolveColor(t *testing.T) {
	cases := []struct {
		name string
		in   ColorInputs
		want bool
	}{
		{"terminal, nothing else", ColorInputs{StdoutTTY: true}, true},
		{"pipe, nothing else", ColorInputs{StdoutTTY: false}, false},

		// --no-color is absolute.
		{"--no-color on a terminal", ColorInputs{NoColorFlag: true, StdoutTTY: true}, false},
		{"--no-color beats CLICOLOR_FORCE", ColorInputs{NoColorFlag: true, StdoutTTY: true, ForceEnv: "1"}, false},
		{"--no-color beats NO_COLOR being unset", ColorInputs{NoColorFlag: true}, false},

		// no-color.org: presence, not value, is the signal.
		{"NO_COLOR=1 on a terminal", ColorInputs{StdoutTTY: true, NoColorEnv: "1"}, false},
		{"NO_COLOR=0 still disables", ColorInputs{StdoutTTY: true, NoColorEnv: "0"}, false},
		{"empty NO_COLOR is not set", ColorInputs{StdoutTTY: true, NoColorEnv: ""}, true},
		{"NO_COLOR beats CLICOLOR_FORCE", ColorInputs{StdoutTTY: false, NoColorEnv: "1", ForceEnv: "1"}, false},

		// Escape codes inside a JSON stream corrupt it.
		{"--json on a terminal", ColorInputs{JSONMode: true, StdoutTTY: true}, false},
		{"--json beats CLICOLOR_FORCE", ColorInputs{JSONMode: true, ForceEnv: "1"}, false},

		// CLICOLOR_FORCE is for pagers and CI logs that render ANSI without
		// being a terminal; "0" is its documented off switch.
		{"CLICOLOR_FORCE=1 on a pipe", ColorInputs{ForceEnv: "1"}, true},
		{"CLICOLOR_FORCE=anything on a pipe", ColorInputs{ForceEnv: "yes please"}, true},
		{"CLICOLOR_FORCE=0 on a pipe", ColorInputs{ForceEnv: "0"}, false},
		{"CLICOLOR_FORCE=0 on a terminal", ColorInputs{ForceEnv: "0", StdoutTTY: true}, true},
		{"empty CLICOLOR_FORCE on a pipe", ColorInputs{ForceEnv: ""}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ResolveColor(c.in); got != c.want {
				t.Errorf("ResolveColor(%+v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestIsTTYRejectsNonTerminals covers the half of terminal detection a CI
// runner can reach. The true branch needs a real pty, which no CI OS provides
// portably, so it is exercised by hand rather than asserted here; the risk this
// test carries is the opposite one -- a false positive that makes a piped
// command emit cursor escapes into a log file.
func TestIsTTYRejectsNonTerminals(t *testing.T) {
	if isTTY(nil) {
		t.Error("isTTY(nil) = true, want false")
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()
	if isTTY(reader) || isTTY(writer) {
		t.Error("a pipe is not a terminal")
	}

	regular := filepath.Join(t.TempDir(), "out.txt")
	file, err := os.Create(regular)
	if err != nil {
		t.Fatalf("create %s: %v", regular, err)
	}
	defer file.Close()
	if isTTY(file) {
		t.Error("a regular file is not a terminal")
	}
}

// TestIsStdoutTTYAgreesWithIsTTY pins the two exported probes to the shared
// helper so a future refactor cannot leave one of them reading a different
// descriptor.
func TestIsStdoutTTYAgreesWithIsTTY(t *testing.T) {
	if got, want := IsStdoutTTY(), isTTY(os.Stdout); got != want {
		t.Errorf("IsStdoutTTY() = %v, isTTY(os.Stdout) = %v", got, want)
	}
	if got, want := IsStdinTTY(), isTTY(os.Stdin); got != want {
		t.Errorf("IsStdinTTY() = %v, isTTY(os.Stdin) = %v", got, want)
	}
}

// TestModeStringHasNoANSI is a small guard on a real failure mode: Mode.String
// ends up in error text and logs, and if it ever carried an escape sequence it
// would leak into --json output.
func TestModeStringHasNoANSI(t *testing.T) {
	for _, mode := range []Mode{ModeTTY, ModePlain, ModeJSON, Mode(9)} {
		if strings.Contains(mode.String(), "\x1b") {
			t.Errorf("Mode(%d).String() contains an ANSI escape", int(mode))
		}
	}
}
