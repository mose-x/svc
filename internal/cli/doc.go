// Package cli is svc's command-line shell: the second front end over the same
// service kernel the GUI drives.
//
// The product requirement is that the CLI behaves exactly like the window, so
// this package owns no wording, no formatting rules and no policy of its own.
// Text comes from internal/cli/i18n, which reads the frontend's own catalogs;
// byte sizes and progress lines are line-for-line ports of
// frontend/src/utils/format.ts and the DetailPanel renderer. Where a choice had
// to be made, the comment on it names the frontend source it was taken from.
//
// The output contract is the part that is genuinely CLI-specific:
//
//   - stdout carries results and nothing else, so `svc list --json | jq` works.
//   - stderr carries progress, prompts and diagnostics.
//   - the exit code says why a command failed (see exitcode.go).
//
// Everything here is deliberately impure-free at the edges: parsing, mapping
// and rendering are pure functions over plain values, and the two places that
// touch the process (reading the environment, detecting a terminal) are
// isolated in CollectEnvironment and IsStdoutTTY/IsStdinTTY. That is what makes
// the non-tty and --json paths testable on a CI runner that has no terminal.
//
// No code in this package may start a child process. Spawns belong to
// internal/detach and internal/helpers, the only places that set
// CREATE_NO_WINDOW; a bare exec.Command here would flash a console window on
// Windows, and internal/detach/nospawn_guard_test.go fails the build if one
// appears.
package cli
