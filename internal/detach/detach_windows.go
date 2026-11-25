//go:build windows

package detach

import (
	"os/exec"
	"syscall"
)

// applyDetach re-asserts CREATE_NO_WINDOW from CreationFlags and adds
// HideWindow. helpers.CreateCmd already sets the flag; restating it here means
// the no-flash guarantee does not depend on remembering what a helper in another
// package did.
//
// DETACHED_PROCESS is deliberately absent: MSDN documents it as mutually
// exclusive with CREATE_NO_WINDOW, and a Windows child already outlives its
// parent without it.
func applyDetach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags = CreationFlags("windows")
	cmd.SysProcAttr.HideWindow = true
}
