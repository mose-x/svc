package cli

import (
	"context"
	"errors"

	"svc/internal/cli/i18n"
)

// Process exit codes. They are the only machine-readable promise a shell makes
// to whatever called it, so they are stable: scripts key on the number, and
// renumbering one is a breaking change even when the text stays the same.
//
// 0 and 1 are the conventional success/failure pair; everything above 1 exists
// so a script can tell "you asked for something that is not there" apart from
// "the download is corrupt" apart from "another svc is running" without
// parsing a localized sentence.
const (
	// ExitOK is success.
	ExitOK = 0
	// ExitError is the catch-all: an unmapped apperr code, a wrapped
	// low-level cause, or an internal failure.
	ExitError = 1
	// ExitUsage is a mistake in how the command was written: a missing
	// argument, an unknown command or flag, or an operation the SDK layout
	// forbids.
	ExitUsage = 2
	// ExitNotFound means the subject of the command does not exist.
	ExitNotFound = 3
	// ExitIntegrity means the bytes or the installed tree failed a check.
	ExitIntegrity = 4
	// ExitCanceled means the user stopped it (SIGINT) or declined a prompt.
	ExitCanceled = 5
	// ExitNetwork is a transport failure: HTTP status, proxy policy, scheme.
	ExitNetwork = 6
	// ExitBusy means another svc process holds the operation lock.
	ExitBusy = 7
)

// codeExitCodes maps every apperr.Code to an exit code.
//
// The table is exhaustive on purpose and the exhaustiveness is enforced:
// exitcode_test.go parses internal/apperr/apperr.go and fails when a declared
// code has no entry here, or when an entry here names a code apperr no longer
// declares. Without that guard a new error code would silently start exiting 1
// and a script waiting for 3 would hang.
var codeExitCodes = map[string]int{
	// Not found: the thing the command was pointed at is not there.
	"unknown-sdk-type":    ExitNotFound,
	"path-not-exist":      ExitNotFound,
	"not-in-path":         ExitNotFound,
	"exec-not-found":      ExitNotFound,
	"version-dir-missing": ExitNotFound,
	"no-backup":           ExitNotFound,
	"no-asset":            ExitNotFound,

	// Integrity: something was found but did not pass a check. Retrying is
	// pointless until the download or the tree is repaired.
	"sdk-incomplete":    ExitIntegrity,
	"version-mismatch":  ExitIntegrity,
	"checksum-mismatch": ExitIntegrity,

	// Network: the transport failed, so a retry on a better connection may
	// well succeed.
	"update-http-status": ExitNetwork,
	"private-ip":         ExitNetwork,
	"http-status":        ExitNetwork,
	"scheme-not-allowed": ExitNetwork,

	// Usage: the command itself was wrong, or the operation is forbidden by
	// the layout svc manages. These are the codes a caller can act on by
	// changing what it asked for.
	"hidden-copy":             ExitUsage,
	"managed-copy":            ExitUsage,
	"protected-import":        ExitUsage,
	"protected-dir":           ExitUsage,
	"managed-dir":             ExitUsage,
	"jdk-required":            ExitUsage,
	"target-exists":           ExitUsage,
	"nested-dirs":             ExitUsage,
	"path-not-absolute":       ExitUsage,
	"system-dir":              ExitUsage,
	"need-sdk":                ExitUsage,
	"composer-manual":         ExitUsage,
	"unknown-package-manager": ExitUsage,
	"invalid-package-name":    ExitUsage,
	"protected-package":       ExitUsage,

	// Error: a genuine failure that is not one of the classes above.
	"app-not-initialized": ExitError,
	"exec-timeout":        ExitError,
	"exec-failed":         ExitError,
	"version-parse-fail":  ExitError,
}

// ExitCodeForCode maps an apperr code to its exit code. An unrecognised code
// yields ExitError rather than ExitOK: an error must never look like success,
// even one this table has not been taught about yet.
func ExitCodeForCode(code string) int {
	if exit, ok := codeExitCodes[code]; ok {
		return exit
	}
	return ExitError
}

// ExitCodeForError maps an error to the exit code the process should report.
// A nil error is success.
//
// The apperr marker is anchored ("^[svc:code]..."), so a service that wraps it
// with fmt.Errorf("install go: %w", err) hides it from a plain string match.
// The error tree is therefore walked -- both the %w chain and errors.Join
// branches -- and the first marker found decides. context.Canceled is mapped
// too, because SIGINT reaches services as a cancelled context and "the user
// stopped it" must not exit 1.
func ExitCodeForError(err error) int {
	if err == nil {
		return ExitOK
	}
	if code, ok := firstMarkerCode(err); ok {
		return ExitCodeForCode(code)
	}
	if errors.Is(err, context.Canceled) {
		return ExitCanceled
	}
	return ExitError
}

// firstMarkerCode searches an error tree depth-first for an apperr marker and
// returns its code. Depth-first matters: the outermost wrap is prose added by
// the caller and never a marker itself, while the innermost cause is.
func firstMarkerCode(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if code, ok := i18n.ErrorCode(err.Error()); ok {
		return code, true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, sub := range joined.Unwrap() {
			if code, ok := firstMarkerCode(sub); ok {
				return code, true
			}
		}
		return "", false
	}
	return firstMarkerCode(errors.Unwrap(err))
}
