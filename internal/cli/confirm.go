package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Confirmation failures. They are sentinels so the command layer can tell
// "nobody could answer" apart from "somebody said no" and choose a different
// exit code and message for each.
//
// The text on these errors is for logs and for --verbose diagnostics; the
// sentence a user reads is rendered from the i18n catalog by the command layer,
// the same way every other message is.
var (
	// ErrConfirmationRequired means the command changes the system, stdin is
	// not a terminal, and --yes was not given. Guessing an answer for an
	// absent human is how an uninstall happens by accident.
	ErrConfirmationRequired = errors.New("cli: confirmation required; re-run with --yes")
	// ErrConfirmationAborted means the prompt ended without an answer: EOF, a
	// closed stdin, or an interrupt during the read.
	ErrConfirmationAborted = errors.New("cli: confirmation aborted")
)

// yesWords and noWords are the accepted replies. Chinese answers are
// first-class rather than an afterthought: settings.json defaults to zh, so the
// default prompt is Chinese and a Chinese prompt that only understood "y" would
// be a trap.
var yesWords = map[string]bool{
	"y": true, "yes": true,
	"是": true, "好": true, "好的": true, "确认": true, "确定": true,
}

var noWords = map[string]bool{
	"n": true, "no": true,
	"否": true, "不": true, "不要": true, "取消": true,
}

// ParseAnswer classifies one confirmation reply.
//
// An empty reply means "take the default", which is what pressing Enter at
// [Y/n] or [y/N] has always meant. Anything not in the two word lists returns
// ok=false: the caller asks again instead of guessing, because the whole point
// of the prompt is that the answer was not predictable.
func ParseAnswer(input string, defaultYes bool) (accepted bool, ok bool) {
	answer := strings.ToLower(strings.TrimSpace(input))
	if answer == "" {
		return defaultYes, true
	}
	if yesWords[answer] {
		return true, true
	}
	if noWords[answer] {
		return false, true
	}
	return false, false
}

// ConfirmRequest describes one confirmation prompt.
type ConfirmRequest struct {
	// Question is the sentence to show, already rendered from the catalog.
	Question string
	// Yes is --yes: the answer is known before asking.
	Yes bool
	// StdinTTY reports whether a human is present to answer.
	StdinTTY bool
	// DefaultYes decides what a bare Enter means and which suffix is shown.
	DefaultYes bool
}

// Prompt asks the user something and reads the reply.
//
// Both streams are injected: the question goes to Out, which the command layer
// sets to stderr so a prompt can never corrupt a result on stdout, and replies
// come from In. A Prompt is not safe for concurrent use -- it holds a buffered
// reader -- but a CLI asks one question at a time.
type Prompt struct {
	in  *bufio.Reader
	out io.Writer
}

// NewPrompt builds a Prompt. An In that is already buffered is used as-is so a
// caller that reads other input too does not lose bytes to a second buffer.
//
// A nil In reads as an immediately closed stdin and a nil Out discards the
// questions, which is what a non-interactive caller wants when it passes Yes
// anyway. Neither may panic: a command that reaches for a prompt it cannot
// actually ask must fail with ErrConfirmationAborted, not crash the process.
func NewPrompt(in io.Reader, out io.Writer) *Prompt {
	if in == nil {
		in = strings.NewReader("")
	}
	reader, buffered := in.(*bufio.Reader)
	if !buffered {
		reader = bufio.NewReader(in)
	}
	if out == nil {
		out = io.Discard
	}
	return &Prompt{in: reader, out: out}
}

// Confirm asks for a yes/no answer.
//
// --yes short-circuits without writing anything, so a scripted run produces no
// stray text. Without a terminal on stdin there is nobody to ask and the answer
// cannot be defaulted either way, so it fails with ErrConfirmationRequired and
// the command layer turns that into "re-run with --yes". Otherwise the question
// is repeated until a reply is understood; EOF ends it with
// ErrConfirmationAborted rather than accepting the default, because Ctrl-D is a
// withdrawal, not a consent.
func (p *Prompt) Confirm(req ConfirmRequest) (bool, error) {
	if req.Yes {
		return true, nil
	}
	if !req.StdinTTY {
		return false, ErrConfirmationRequired
	}
	if p == nil || p.in == nil {
		return false, ErrConfirmationAborted
	}

	suffix := "[y/N]"
	if req.DefaultYes {
		suffix = "[Y/n]"
	}
	for {
		fmt.Fprintf(p.out, "%s %s ", req.Question, suffix)
		line, err := p.in.ReadString('\n')
		if strings.TrimSpace(line) == "" && err != nil {
			return false, ErrConfirmationAborted
		}
		if accepted, understood := ParseAnswer(line, req.DefaultYes); understood {
			return accepted, nil
		}
		if err != nil {
			return false, ErrConfirmationAborted
		}
	}
}
