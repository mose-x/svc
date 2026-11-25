//go:build windows

package detach

import (
	"os/exec"
	"syscall"
	"testing"
)

// TestApplyDetachHidesTheWindow covers the Windows half of the no-flash red
// line. It runs on windows-latest in CI, so the assertion is made against the
// real syscall.SysProcAttr shape rather than a cross-compiled stand-in.
func TestApplyDetachHidesTheWindow(t *testing.T) {
	cmd := &exec.Cmd{}
	applyDetach(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("applyDetach left SysProcAttr nil")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Errorf("CreationFlags = %#x, missing CREATE_NO_WINDOW", cmd.SysProcAttr.CreationFlags)
	}
	if cmd.SysProcAttr.CreationFlags&detachedProcessFlag != 0 {
		t.Errorf("CreationFlags = %#x, must NOT contain DETACHED_PROCESS (mutually exclusive with CREATE_NO_WINDOW)", cmd.SysProcAttr.CreationFlags)
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("HideWindow is not set")
	}

	// A pre-existing SysProcAttr must be extended, not replaced.
	other := &exec.Cmd{}
	other.SysProcAttr = &syscall.SysProcAttr{NoInheritHandles: true}
	applyDetach(other)
	if !other.SysProcAttr.NoInheritHandles {
		t.Error("applyDetach replaced an existing SysProcAttr instead of extending it")
	}
}
