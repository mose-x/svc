//go:build !windows

package detach

import (
	"os/exec"
	"syscall"
	"testing"
)

// TestApplyDetachStartsANewSession covers the Unix half of "the child outlives
// the parent invisibly": Setsid puts it in its own session, so a Ctrl-C aimed at
// the foreground svc cannot reach it and closing the terminal cannot take it
// down.
func TestApplyDetachStartsANewSession(t *testing.T) {
	cmd := &exec.Cmd{}
	applyDetach(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("applyDetach left SysProcAttr nil")
	}
	if !cmd.SysProcAttr.Setsid {
		t.Error("Setsid is not set; the child would stay in the parent's session and die with its terminal")
	}

	// A pre-existing SysProcAttr must be preserved, not replaced.
	other := &exec.Cmd{}
	other.SysProcAttr = &syscall.SysProcAttr{Noctty: true}
	applyDetach(other)
	if !other.SysProcAttr.Setsid {
		t.Error("Setsid was not applied to an existing SysProcAttr")
	}
	if !other.SysProcAttr.Noctty {
		t.Error("applyDetach replaced an existing SysProcAttr instead of extending it")
	}
}
