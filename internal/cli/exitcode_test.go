package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"testing"

	"svc/internal/apperr"
)

// apperrCodeRE reads the code declarations straight out of apperr's source. The
// constants are typed `Code = "..."` on their own line, which is the only shape
// the file uses.
var apperrCodeRE = regexp.MustCompile(`(?m)^\s*[A-Za-z0-9_]+\s+Code\s+=\s+"([a-z0-9-]+)"`)

// declaredApperrCodes returns every code internal/apperr declares.
func declaredApperrCodes(t *testing.T) []string {
	t.Helper()
	source, err := os.ReadFile("../apperr/apperr.go")
	if err != nil {
		t.Fatalf("read apperr.go: %v", err)
	}
	matches := apperrCodeRE.FindAllStringSubmatch(string(source), -1)
	if len(matches) == 0 {
		t.Fatal("the apperr code regexp matched nothing; the source shape changed")
	}
	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}
	return codes
}

// TestExitCodesCoverEveryApperrCode is the drift guard for the exit-code table.
// It is bidirectional on purpose: a code apperr declares but this package does
// not map would silently exit 1 forever, and a mapping for a code apperr no
// longer declares is dead weight that misleads the next reader.
func TestExitCodesCoverEveryApperrCode(t *testing.T) {
	declared := declaredApperrCodes(t)

	for _, code := range declared {
		if _, ok := codeExitCodes[code]; !ok {
			t.Errorf("apperr code %q has no exit-code mapping; add it to codeExitCodes", code)
		}
	}
	for code := range codeExitCodes {
		if !slices.Contains(declared, code) {
			t.Errorf("codeExitCodes maps %q, which internal/apperr does not declare", code)
		}
	}
	if len(codeExitCodes) != len(declared) {
		t.Errorf("codeExitCodes has %d entries but apperr declares %d codes", len(codeExitCodes), len(declared))
	}
}

// TestExitCodesAreUsable pins the shape of the table itself: every mapped value
// is one of the documented constants and none of them is success, so no error
// can ever make a command look like it worked.
func TestExitCodesAreUsable(t *testing.T) {
	valid := []int{ExitError, ExitUsage, ExitNotFound, ExitIntegrity, ExitCanceled, ExitNetwork, ExitBusy}
	for code, exit := range codeExitCodes {
		if !slices.Contains(valid, exit) {
			t.Errorf("codeExitCodes[%q] = %d, which is not a documented exit code", code, exit)
		}
		if exit == ExitOK {
			t.Errorf("codeExitCodes[%q] = ExitOK; an error must never exit 0", code)
		}
	}

	constants := map[string]int{
		"ExitOK": ExitOK, "ExitError": ExitError, "ExitUsage": ExitUsage,
		"ExitNotFound": ExitNotFound, "ExitIntegrity": ExitIntegrity,
		"ExitCanceled": ExitCanceled, "ExitNetwork": ExitNetwork, "ExitBusy": ExitBusy,
	}
	seen := make(map[int]string, len(constants))
	for name, value := range constants {
		if other, dup := seen[value]; dup {
			t.Errorf("%s and %s are both %d; exit codes must be distinct", name, other, value)
		}
		seen[value] = name
	}
}

func TestExitCodeForCode(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{"unknown-sdk-type", ExitNotFound},
		{"version-dir-missing", ExitNotFound},
		{"no-asset", ExitNotFound},
		{"no-cached-versions", ExitNotFound},
		{"checksum-mismatch", ExitIntegrity},
		{"sdk-incomplete", ExitIntegrity},
		{"version-mismatch", ExitIntegrity},
		{"http-status", ExitNetwork},
		{"private-ip", ExitNetwork},
		{"scheme-not-allowed", ExitNetwork},
		{"update-http-status", ExitNetwork},
		{"managed-dir", ExitUsage},
		{"jdk-required", ExitUsage},
		{"protected-package", ExitUsage},
		{"cli-no-dialog", ExitUsage},
		{"invalid-package-name", ExitUsage},
		{"operation-in-progress", ExitBusy},
		{"exec-failed", ExitError},
		{"exec-timeout", ExitError},
		{"version-parse-fail", ExitError},
		{"app-not-initialized", ExitError},
		// Unmapped: a code apperr has not declared yet must still fail loudly.
		{"", ExitError},
		{"not-a-real-code", ExitError},
	}
	for _, c := range cases {
		if got := ExitCodeForCode(c.code); got != c.want {
			t.Errorf("ExitCodeForCode(%q) = %d, want %d", c.code, got, c.want)
		}
	}
}

func TestExitCodeForError(t *testing.T) {
	marker := apperr.New(apperr.UnknownSdkType, map[string]string{"sdk": "go"})
	corrupt := apperr.New(apperr.ChecksumMismatch, nil)
	blocked := apperr.New(apperr.ManagedDir, map[string]string{"dir": "/x"})

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil is success", nil, ExitOK},
		{"plain error", errors.New("dial tcp: connection refused"), ExitError},
		{"marker", marker, ExitNotFound},
		{"marker without params", corrupt, ExitIntegrity},
		{"wrapped marker", fmt.Errorf("install go 1.23: %w", marker), ExitNotFound},
		{"doubly wrapped marker", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", blocked)), ExitUsage},
		{"marker in a joined error", errors.Join(errors.New("first"), blocked), ExitUsage},
		{"marker nested in a joined error", errors.Join(errors.New("first"), fmt.Errorf("second: %w", corrupt)), ExitIntegrity},
		{"cancelled context", context.Canceled, ExitCanceled},
		{"wrapped cancelled context", fmt.Errorf("download aborted: %w", context.Canceled), ExitCanceled},
		{"context deadline is not a cancellation", context.DeadlineExceeded, ExitError},
		{"marker wins over cancellation", errors.Join(context.Canceled, corrupt), ExitIntegrity},
		{"empty wrapped chain", fmt.Errorf("boom: %w", errors.New("cause")), ExitError},

		// The shell's own refusals are recognised ahead of any marker inside
		// them: how the command was written outranks what it happened to wrap.
		{"usage error", &UsageError{errors.New(`unknown flag: --jsonn`)}, ExitUsage},
		{"usage error wrapping a marker", &UsageError{marker}, ExitUsage},
		{"wrapped usage error", fmt.Errorf("parse: %w", &UsageError{errNoCommand}), ExitUsage},
		{"confirmation required", ErrConfirmationRequired, ExitUsage},
		{"wrapped confirmation required", fmt.Errorf("uninstall go: %w", ErrConfirmationRequired), ExitUsage},
		{"confirmation declined", ErrConfirmationDeclined, ExitCanceled},
		{"wrapped confirmation declined", fmt.Errorf("switch go: %w", ErrConfirmationDeclined), ExitCanceled},
		{"confirmation aborted", ErrConfirmationAborted, ExitCanceled},
		{"wrapped confirmation aborted", fmt.Errorf("uninstall go: %w", ErrConfirmationAborted), ExitCanceled},
		{"aborted wins over a joined marker", errors.Join(ErrConfirmationAborted, corrupt), ExitCanceled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExitCodeForError(c.err); got != c.want {
				t.Errorf("ExitCodeForError(%v) = %d, want %d", c.err, got, c.want)
			}
		})
	}
}

// TestExitCodeForErrorIsStableAcrossWrapDepth guards the depth-first walk: the
// outermost message is prose written by the caller and never a marker, so a
// walk that stopped at the first error it saw would classify every wrapped
// failure as ExitError.
func TestExitCodeForErrorIsStableAcrossWrapDepth(t *testing.T) {
	inner := apperr.New(apperr.NoAsset, map[string]string{"asset": "node.tar.gz"})
	wrapped := error(inner)
	for depth := 0; depth < 6; depth++ {
		if got := ExitCodeForError(wrapped); got != ExitNotFound {
			t.Fatalf("at wrap depth %d: ExitCodeForError = %d, want %d", depth, got, ExitNotFound)
		}
		wrapped = fmt.Errorf("layer %d: %w", depth, wrapped)
	}
}
