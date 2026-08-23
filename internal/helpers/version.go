package helpers

import (
	"context"
	"os"
	"regexp"
	"time"

	"svc/internal/sdk"
)

// versionPattern matches a version string like "1.2" or "1.2.3". Hoisted to a
// package-level var so it is compiled once at init, not recompiled per call
// (L1). ExtractVersionFromString is on the hot path (system-detected SDK
// version scans + every imported SDK directory probe).
var versionPattern = regexp.MustCompile(`(\d+\.\d+(?:\.\d+)?)`)

// ExtractVersionFromString applies the version-extraction regex to a command's
// raw output. Used by ExtractVersionFromOutput (system-detected SDKs) and the
// importer's directory probes. Returns "" if no version pattern found.
func ExtractVersionFromString(s string) string {
	return versionPattern.FindString(s)
}

// ExtractVersionFromOutput resolves cmd on the system PATH and runs it with
// args, extracting a version string from its output. Returns "" when the
// command is missing, fails, or prints no recognizable version.
func ExtractVersionFromOutput(cmd string, args []string) string {
	fullPath := sdk.ResolveSystemCommand(cmd)
	if fullPath == "" {
		return ""
	}
	// H2: Bound the version-detection command so a hung binary (e.g. waiting
	// on stdin) doesn't block the UI forever.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := CreateCmdContext(ctx, fullPath, args...)
	sysPath := sdk.GetSystemPath()
	if sysPath != "" {
		c.Env = ReplacePathEnv(os.Environ(), sysPath)
	}
	out, err := c.CombinedOutput()
	if err != nil {
		return ""
	}
	return ExtractVersionFromString(string(out))
}
