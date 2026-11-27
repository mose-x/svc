package cli

import (
	"slices"
	"strings"
	"testing"
)

// completionShells are the four scripts cobra's default completion command
// generates. Enumerated rather than discovered so that a cobra upgrade which
// adds a shell, or renames one, shows up here as a decision instead of as a
// script nobody tested.
var completionShells = []string{"bash", "zsh", "fish", "powershell"}

// TestCompletionScriptsAreGenerated is the golden half of §7's P5 row: every
// shell the design lists produces a script, on stdout, with nothing on stderr.
//
// The assertions are deliberately not byte-for-byte goldens. The scripts are
// cobra's, they change when cobra changes, and a golden file would then fail on
// a dependency bump that changed nothing about svc. What is svc's is that the
// script is generated at all, that it names the program it completes for, and
// that it arrives on the result stream -- a script printed to stderr would be
// invisible to `svc completion zsh > ~/.zfunc/_svc`, which is the only way
// anybody installs one.
func TestCompletionScriptsAreGenerated(t *testing.T) {
	for _, shell := range completionShells {
		t.Run(shell, func(t *testing.T) {
			code, s := pipedRun(t, nil, "completion", shell)
			if code != ExitOK {
				t.Fatalf("svc completion %s = exit %d, want %d; stderr:\n%s", shell, code, ExitOK, s.stderr)
			}
			script := s.stdout.String()
			if strings.TrimSpace(script) == "" {
				t.Fatalf("svc completion %s printed no script", shell)
			}
			if !strings.Contains(script, "svc") {
				t.Errorf("the %s script never names svc:\n%s", shell, script)
			}
			if s.stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing: the script is the result", s.stderr)
			}
		})
	}
}

// TestCompletionListsTheCommandTree is the half that actually proves
// completion works. Cobra's scripts are dynamic: they call back into
// `svc __complete` rather than embedding a command list, so a generated script
// can be perfect while the tree it queries offers nothing. Driving `__complete`
// directly asserts the candidates a shell would show.
func TestCompletionListsTheCommandTree(t *testing.T) {
	t.Run("the root offers the commands", func(t *testing.T) {
		code, s := pipedRun(t, nil, "__complete", "")
		if code != ExitOK {
			t.Fatalf("svc __complete = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		candidates := completionCandidates(s)
		for _, want := range []string{"list", "install", "switch", "settings", "endpoints", "doctor", "about", "self", "pkg", "clean", "logs"} {
			if !slices.Contains(candidates, want) {
				t.Errorf("the root offers [%s]; %q is missing", strings.Join(candidates, " "), want)
			}
		}
		// `completion` and `help` ARE offered: both are commands a user is
		// meant to type, and hiding completion would leave
		// `svc completion zsh > ...` discoverable only through the README. The
		// three that are not meant to be typed are the background carrier
		// `svc versions` spawns and cobra's own two request channels, and all
		// three are Hidden -- which is what keeps them out of a candidate list.
		for _, hidden := range []string{"__refresh", "__complete", "__completeNoDesc"} {
			if slices.Contains(candidates, hidden) {
				t.Errorf("the root offers %q, which is not meant to be typed", hidden)
			}
		}
	})

	t.Run("a parent offers its subcommands", func(t *testing.T) {
		code, s := pipedRun(t, nil, "__complete", "self", "")
		if code != ExitOK {
			t.Fatalf("svc __complete self = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		candidates := completionCandidates(s)
		if !slices.Contains(candidates, "check") {
			t.Errorf("`svc self ` offers [%s]; %q is missing", strings.Join(candidates, " "), "check")
		}
	})

	t.Run("a prefix narrows the candidates inside a group", func(t *testing.T) {
		code, s := pipedRun(t, nil, "__complete", "settings", "che")
		if code != ExitOK {
			t.Fatalf("svc __complete settings che = exit %d, want %d", code, ExitOK)
		}
		candidates := completionCandidates(s)
		if !slices.Contains(candidates, "check-proxy") {
			t.Errorf("`svc settings che` offers [%s]; want check-proxy", strings.Join(candidates, " "))
		}
		if slices.Contains(candidates, "show") {
			t.Errorf("`svc settings che` offers [%s]; the prefix narrowed nothing", strings.Join(candidates, " "))
		}
	})

	t.Run("an sdk argument offers the registered identifiers", func(t *testing.T) {
		code, s := pipedRun(t, nil, "__complete", "install", "")
		if code != ExitOK {
			t.Fatalf("svc __complete install = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		candidates := completionCandidates(s)
		if !slices.Contains(candidates, "go") || !slices.Contains(candidates, "nodejs") {
			t.Errorf("`svc install ` offers [%s]; want the registered SDK identifiers", strings.Join(candidates, " "))
		}
	})
}

// TestCompletionWithAnUnknownShellIsHelpLikeEveryOtherParent pins that the one
// parent cobra generates behaves like the ones this repository writes. It does
// not reject the unknown shell: cobra returns flag.ErrHelp for any command that
// has subcommands and no RunE of its own, and returns it BEFORE validating
// arguments, so an unknown child of a parent is help and exit 0 everywhere in
// this tree. Asserting the shared behaviour is the useful form of this test --
// a `completion` that started exiting 2 on an unknown shell would be the odd
// one out, and one that started exiting 1 would be a regression nobody wrote.
func TestCompletionWithAnUnknownShellIsHelpLikeEveryOtherParent(t *testing.T) {
	variants := []string{"completion", "logs", "settings", "pkg"}
	var wantCode int
	var wantHelp bool
	for index, parent := range variants {
		code, s := pipedRun(t, nil, parent, "klingon")
		gotHelp := strings.Contains(s.stdout.String(), "Available Commands:")
		if index > 0 {
			if code != wantCode || gotHelp != wantHelp {
				t.Errorf("svc %s klingon = exit %d with help=%v; `completion` gave exit %d with help=%v",
					parent, code, gotHelp, wantCode, wantHelp)
			}
			continue
		}
		wantCode, wantHelp = code, gotHelp
		if code != ExitOK || !gotHelp {
			t.Fatalf("svc completion klingon = exit %d with help=%v, want exit %d and the subcommand list",
				code, gotHelp, ExitOK)
		}
		if s.stderr.Len() != 0 {
			t.Errorf("stderr = %q, want it empty for help", s.stderr)
		}
	}
}

// completionCandidates splits a `__complete` result into the candidates it
// offers. The last line is cobra's directive -- ":4" for NoFileComp -- which is
// an instruction to the generated script rather than a candidate, so it is
// dropped here and every assertion above reads as a list of words.
func completionCandidates(s *streams) []string {
	lines := outLines(s)
	candidates := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, ":") {
			continue
		}
		// A candidate may carry a description after a tab when cobra's
		// descriptions are enabled; the word the shell inserts is before it.
		if word, _, _ := strings.Cut(line, "\t"); word != "" {
			candidates = append(candidates, word)
		}
	}
	return candidates
}
