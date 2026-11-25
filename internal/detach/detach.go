// Package detach starts a background svc process that outlives its parent
// without the user ever noticing it happened.
//
// The one job it has is the version refresh behind `svc versions`: return the
// cached list immediately, then fetch a fresh one in a child that survives this
// process exiting, so the next run -- GUI or CLI -- sees updated data instead of
// a refresh that evaporated with its parent.
//
// Two rules are load-bearing and both come from product requirements rather than
// taste:
//
//   - No window, ever. On Windows every spawn goes through helpers.CreateCmd,
//     which sets CREATE_NO_WINDOW. DETACHED_PROCESS is NOT added: MSDN documents
//     the two flags as mutually exclusive, and combining them either fails or
//     gives the child a brand-new console -- the exact flash this package exists
//     to prevent. A Windows child already survives its parent without it.
//   - No inherited stdio. A background child writing into the pipe the user fed
//     to `jq` would corrupt the foreground command's output, so stdout/stderr go
//     to a log file or to the null device and stdin comes from nowhere.
//
// The child's exit status is deliberately unobservable: a failed background
// refresh must never change the exit code of the command the user ran.
package detach

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"svc/internal/helpers"
	"svc/internal/logger"
)

// createNoWindow is CREATE_NO_WINDOW: run the child without attaching a console
// window. It is the only creation flag this package sets -- see CreationFlags
// for why DETACHED_PROCESS must never be added to it.
const createNoWindow = 0x08000000

// CreationFlags returns the Windows process-creation flags for a detached
// background child. It is parameterised by goos instead of being split per
// platform so all six release targets can be checked on any host.
//
// Windows gets CREATE_NO_WINDOW and nothing else; every other platform gets 0
// because the flag is a Windows-only concept.
func CreationFlags(goos string) uint32 {
	if goos != "windows" {
		return 0
	}
	return createNoWindow
}

// Options configures a detached spawn.
type Options struct {
	// LogFile receives the child's stdout and stderr, appended. Empty sends
	// both to the null device. The parent's descriptors are never inherited.
	LogFile string
	// Dir is the child's working directory. Empty keeps the parent's.
	Dir string
	// Env is the child's environment. Nil inherits the parent's.
	Env []string
}

// Spawn starts exe with argv in the background and returns as soon as the child
// is running. Nothing waits for the child and its exit status is not
// observable, so whatever it does cannot affect the caller's outcome.
func Spawn(exe string, argv []string, opts Options) error {
	if exe == "" {
		return errors.New("detach: an executable path is required")
	}

	// helpers.CreateCmd is mandatory, not incidental: it is the only place
	// CREATE_NO_WINDOW is set. A bare exec.Command here would compile, pass
	// review, and then flash a console window on a user's machine.
	cmd := helpers.CreateCmd(exe, argv...)
	applyDetach(cmd)
	cmd.Dir = opts.Dir
	if opts.Env != nil {
		cmd.Env = opts.Env
	}

	cleanup, err := configureStdio(cmd, opts.LogFile)
	if err != nil {
		return err
	}
	// Start dups the descriptor for the child, so the parent's copy can go as
	// soon as the spawn has happened. exec.Cmd does not take ownership of an
	// *os.File writer, so closing here cannot race with Wait below.
	defer cleanup()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("detach: start %s: %w", exe, err)
	}

	// Reap in the background so a long-lived parent does not accumulate
	// zombies. If the parent exits first the goroutine dies with it and the OS
	// reaps the child instead; either way nobody observes the status.
	go func() { _ = cmd.Wait() }()

	logger.Info("Detached background process started: %s %v (log: %s)", exe, argv, opts.LogFile)
	return nil
}

// configureStdio wires the child's descriptors and returns a cleanup that
// closes whatever it opened. Split out from Spawn so the "nothing is inherited"
// promise is directly testable without spawning anything.
//
// A nil descriptor is os/exec's "connect to the null device", which is the
// default this package promises: the child must never inherit the parent's
// stdin/stdout/stderr, or a background refresh could scribble into the pipe the
// user is feeding to another program.
func configureStdio(cmd *exec.Cmd, logFile string) (func(), error) {
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if logFile == "" {
		return func() {}, nil
	}
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("detach: open the log file %s: %w", logFile, err)
	}
	cmd.Stdout = file
	cmd.Stderr = file
	return func() { file.Close() }, nil
}
