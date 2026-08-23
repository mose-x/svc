//go:build !windows

package sdk

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	cachedShellPath string
	pathOnce        sync.Once
)

// getPlatformPath returns the system PATH. On Unix, GUI apps launched from
// Dock/Finder inherit only /etc/paths, missing .zshrc/.bashrc additions.
// To match what a terminal session sees, we spawn a login shell and capture
// its PATH. The result is cached (sync.Once) so the shell is spawned only once.
func getPlatformPath() string {
	pathOnce.Do(func() {
		cachedShellPath = detectShellPath()
	})
	if cachedShellPath != "" {
		return cachedShellPath
	}
	return os.Getenv("PATH")
}

// detectShellPath spawns a login interactive shell and captures its $PATH.
// -l = login (sources /etc/profile, ~/.zprofile/.bash_profile),
// -i = interactive (sources ~/.zshrc/.bashrc),
// -c = command. printf (not echo) avoids trailing newline issues.
// A 5s timeout prevents a hung .zshrc (e.g. nvm prompt) from blocking startup.
func detectShellPath() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-lic", "printf '%s' \"$PATH\"")
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	return sanitizeShellPath(string(out))
}

// sanitizeShellPath filters garbage out of a PATH string captured from a
// login shell. Interactive rc files may print banners to stdout (e.g.
// "Restored session: Sun Aug 23 ..."), which end up in the captured output
// as fake PATH segments. Only absolute, whitespace-free directories survive.
func sanitizeShellPath(raw string) string {
	if raw == "" {
		return ""
	}
	var kept []string
	for _, seg := range strings.Split(raw, ":") {
		if seg == "" {
			continue
		}
		if !strings.HasPrefix(seg, "/") {
			continue
		}
		if strings.ContainsAny(seg, " \t\n\r") {
			continue
		}
		kept = append(kept, seg)
	}
	return strings.Join(kept, ":")
}
