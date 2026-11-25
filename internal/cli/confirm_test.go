package cli

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestParseAnswer(t *testing.T) {
	cases := []struct {
		input      string
		defaultYes bool
		wantAccept bool
		wantOK     bool
	}{
		// A bare Enter means "take the default", whichever way it points.
		{"", true, true, true},
		{"", false, false, true},
		{"\n", true, true, true},
		{"   ", false, false, true},
		{"\t\r\n", true, true, true},

		// English, in the casing and padding a terminal actually delivers.
		{"y", true, true, true},
		{"Y", true, true, true},
		{"yes", false, true, true},
		{"YES", false, true, true},
		{" Yes \n", false, true, true},
		{"n", true, false, true},
		{"N", true, false, true},
		{"no", true, false, true},
		{"NO\n", true, false, true},

		// Chinese: the default catalog language, so these are the answers a
		// default install will actually type.
		{"是", false, true, true},
		{"好", false, true, true},
		{"好的", false, true, true},
		{"确认", false, true, true},
		{"确定", false, true, true},
		{"否", true, false, true},
		{"不", true, false, true},
		{"不要", true, false, true},
		{"取消", true, false, true},

		// Everything else is not an answer, and guessing one is the failure
		// this function exists to avoid.
		{"maybe", true, false, false},
		{"yess", true, false, false},
		{"yeah", true, false, false},
		{"1", true, false, false},
		{"0", true, false, false},
		{"是的", true, false, false},
		{"ok", true, false, false},
		{"y n", true, false, false},
	}
	for _, c := range cases {
		accepted, ok := ParseAnswer(c.input, c.defaultYes)
		if ok != c.wantOK || accepted != c.wantAccept {
			t.Errorf("ParseAnswer(%q, %v) = (%v, %v), want (%v, %v)",
				c.input, c.defaultYes, accepted, ok, c.wantAccept, c.wantOK)
		}
	}
}

func TestPromptConfirmWithYesFlag(t *testing.T) {
	// --yes must not touch either stream: a scripted run gets no stray prompt
	// text in its log, and no read that could block on a closed stdin.
	for _, stdinTTY := range []bool{true, false} {
		out := &bytes.Buffer{}
		prompt := NewPrompt(strings.NewReader(""), out)
		accepted, err := prompt.Confirm(ConfirmRequest{Question: "Uninstall go?", Yes: true, StdinTTY: stdinTTY})
		if err != nil {
			t.Fatalf("Confirm with --yes and stdinTTY=%v: %v", stdinTTY, err)
		}
		if !accepted {
			t.Errorf("Confirm with --yes and stdinTTY=%v = false, want true", stdinTTY)
		}
		if out.Len() != 0 {
			t.Errorf("Confirm with --yes wrote %q, want nothing", out.String())
		}
	}
}

func TestPromptConfirmWithoutATerminal(t *testing.T) {
	out := &bytes.Buffer{}
	prompt := NewPrompt(strings.NewReader("y\n"), out)
	accepted, err := prompt.Confirm(ConfirmRequest{Question: "Uninstall go?", StdinTTY: false})

	if !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("Confirm without a terminal: err = %v, want ErrConfirmationRequired", err)
	}
	if accepted {
		t.Error("Confirm without a terminal = true; an absent human must never be answered for")
	}
	if out.Len() != 0 {
		t.Errorf("Confirm without a terminal wrote %q; the question is useless to a script", out.String())
	}
}

func TestPromptConfirmInteractive(t *testing.T) {
	cases := []struct {
		name       string
		stdin      string
		defaultYes bool
		wantAccept bool
		wantErr    error
		wantAsks   int
	}{
		{"y accepts", "y\n", true, true, nil, 1},
		{"n declines", "n\n", true, false, nil, 1},
		{"bare enter takes the yes default", "\n", true, true, nil, 1},
		{"bare enter takes the no default", "\n", false, false, nil, 1},
		{"chinese yes", "好的\n", false, true, nil, 1},
		{"chinese no", "取消\n", true, false, nil, 1},
		{"case and padding are ignored", "  YES  \n", false, true, nil, 1},
		{"an unrecognised answer is asked again", "maybe\nnonsense\ny\n", false, true, nil, 3},
		{"no trailing newline still answers", "y", true, true, nil, 1},
		// Ctrl-D is a withdrawal, not a consent -- even when the default is yes.
		{"eof before any answer aborts", "", true, false, ErrConfirmationAborted, 1},
		{"eof after an unrecognised answer aborts", "maybe", false, false, ErrConfirmationAborted, 1},
		// A blank line is an answer, not an abort: pressing Enter at [Y/n] has
		// always meant "take the default".
		{"a blank line takes the default", "\n\n", true, true, nil, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			prompt := NewPrompt(strings.NewReader(c.stdin), out)
			accepted, err := prompt.Confirm(ConfirmRequest{
				Question:   "Uninstall go 1.23.0?",
				StdinTTY:   true,
				DefaultYes: c.defaultYes,
			})

			if !errors.Is(err, c.wantErr) {
				t.Fatalf("Confirm = %v, want %v", err, c.wantErr)
			}
			if accepted != c.wantAccept {
				t.Errorf("Confirm = %v, want %v", accepted, c.wantAccept)
			}
			if asks := strings.Count(out.String(), "Uninstall go 1.23.0?"); asks != c.wantAsks {
				t.Errorf("the question was asked %d times, want %d (output %q)", asks, c.wantAsks, out.String())
			}
		})
	}
}

// TestPromptConfirmSuffix checks the convention a user reads the default off:
// the capitalised letter is what a bare Enter does.
func TestPromptConfirmSuffix(t *testing.T) {
	cases := []struct {
		defaultYes bool
		wantSuffix string
	}{
		{true, "[Y/n] "},
		{false, "[y/N] "},
	}
	for _, c := range cases {
		out := &bytes.Buffer{}
		prompt := NewPrompt(strings.NewReader("n\n"), out)
		if _, err := prompt.Confirm(ConfirmRequest{Question: "Proceed?", StdinTTY: true, DefaultYes: c.defaultYes}); err != nil {
			t.Fatalf("Confirm: %v", err)
		}
		if !strings.HasSuffix(out.String(), c.wantSuffix) {
			t.Errorf("defaultYes=%v: prompt = %q, want it to end with %q", c.defaultYes, out.String(), c.wantSuffix)
		}
		if strings.Contains(out.String(), "\n") {
			t.Errorf("the prompt must stay on one line so the answer is typed after it: %q", out.String())
		}
	}
}

// TestPromptReusesAnAlreadyBufferedReader covers the byte-loss failure a second
// bufio.Reader would cause: NewPrompt must not wrap a reader that is already
// buffered, or the bytes it read ahead for the first question would vanish
// before the second one is asked.
func TestPromptReusesAnAlreadyBufferedReader(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("y\nn\n"))
	out := &bytes.Buffer{}
	prompt := NewPrompt(reader, out)

	first, err := prompt.Confirm(ConfirmRequest{Question: "First?", StdinTTY: true})
	if err != nil || !first {
		t.Fatalf("first Confirm = (%v, %v), want (true, nil)", first, err)
	}
	second, err := prompt.Confirm(ConfirmRequest{Question: "Second?", StdinTTY: true, DefaultYes: true})
	if err != nil || second {
		t.Fatalf("second Confirm = (%v, %v), want (false, nil) -- the n was lost", second, err)
	}
}

// TestPromptWritesOnlyToItsOwnStreams pins the stdout/stderr split for prompts:
// a question is written to Out and nowhere else, so a piped result can never
// contain "Are you sure?".
func TestPromptWritesOnlyToItsOwnStreams(t *testing.T) {
	promptOut := &bytes.Buffer{}
	other := &bytes.Buffer{}
	prompt := NewPrompt(strings.NewReader("y\n"), promptOut)
	if _, err := prompt.Confirm(ConfirmRequest{Question: "Proceed?", StdinTTY: true}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !strings.Contains(promptOut.String(), "Proceed?") {
		t.Errorf("the question did not reach the prompt writer: %q", promptOut.String())
	}
	if other.Len() != 0 {
		t.Errorf("a stream the prompt was never given holds %q", other.String())
	}
}

// TestPromptWithoutStreams must not panic: a command wired up with nil streams
// and --yes is a legitimate (if unusual) configuration, and one that answers
// without a terminal gets a clean error rather than a nil dereference.
func TestPromptWithoutStreams(t *testing.T) {
	if accepted, err := NewPrompt(nil, nil).Confirm(ConfirmRequest{Question: "Proceed?", Yes: true}); err != nil || !accepted {
		t.Errorf("Confirm with --yes over nil streams = (%v, %v), want (true, nil)", accepted, err)
	}
	if _, err := NewPrompt(nil, nil).Confirm(ConfirmRequest{Question: "Proceed?", StdinTTY: false}); !errors.Is(err, ErrConfirmationRequired) {
		t.Errorf("Confirm without a terminal = %v, want ErrConfirmationRequired", err)
	}

	var nilPrompt *Prompt
	if _, err := nilPrompt.Confirm(ConfirmRequest{Question: "Proceed?", StdinTTY: true}); !errors.Is(err, ErrConfirmationAborted) {
		t.Errorf("Confirm on a nil prompt = %v, want ErrConfirmationAborted", err)
	}
}

// TestNewPromptSurvivesNilStreams: bufio.NewReader(nil) would panic on the
// first read, so the constructor has to be the thing that keeps a nil reader
// from reaching it. A nil stdin behaves as one that is already closed.
func TestNewPromptSurvivesNilStreams(t *testing.T) {
	prompt := NewPrompt(nil, io.Discard)
	if prompt == nil || prompt.in == nil || prompt.out == nil {
		t.Fatal("NewPrompt(nil, io.Discard) left a nil field")
	}
	if _, err := prompt.Confirm(ConfirmRequest{Question: "Proceed?", StdinTTY: true}); !errors.Is(err, ErrConfirmationAborted) {
		t.Errorf("Confirm over a nil reader = %v, want ErrConfirmationAborted rather than a panic", err)
	}
}
