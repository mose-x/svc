//go:build !windows

package detach

import (
	"os/exec"
	"syscall"
)

// applyDetach puts the child in its own session. Two consequences matter: a
// Ctrl-C aimed at the foreground svc goes to the foreground process group only,
// so it cannot take the background refresh with it; and the child is not tied to
// the parent's controlling terminal, so it survives the terminal closing.
func applyDetach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
