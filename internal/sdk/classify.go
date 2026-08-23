package sdk

import (
	"path/filepath"
	"runtime"
	"strings"
)

// PathCopyClassification describes how a PATH-resolved SDK copy must be
// treated by every view (SDK list, PATH modal) and every import entry point.
type PathCopyClassification struct {
	// Hidden: skip detection and display entirely. The Windows python stub in
	// WindowsApps opens the Microsoft Store when executed, so it must never
	// be probed, listed or offered for import.
	Hidden bool
	// SystemProtected: the copy lives in an OS-managed location (/usr/bin,
	// C:\Windows, ...) that can never be copied into the SVC store.
	SystemProtected bool
	// ExternalManager: name of the external version manager owning the copy
	// ("nvm-rust" / "nvm"); empty for standalone copies.
	ExternalManager string
}

// ClassifyPathCopy is the SINGLE source of truth for how a PATH copy of
// sdkType at p (a resolved binary path or a PATH directory) must be treated.
// Every view (sidebar/detail status, PATH modal) and every import guard calls
// this instead of re-implementing checks, so a rule change lands everywhere
// at once.
func ClassifyPathCopy(sdkType SdkType, p string) PathCopyClassification {
	return classifyPathCopy(runtime.GOOS, sdkType, p)
}

// classifyPathCopy is the pure core (goos parameter) so tests can exercise
// all platforms on any host.
func classifyPathCopy(goos string, sdkType SdkType, p string) PathCopyClassification {
	var c PathCopyClassification
	if p == "" {
		return c
	}
	if sdkType == Python && goos == "windows" && IsWindowsStorePython(p) {
		c.Hidden = true
		return c
	}
	if IsSystemSdkPath(goos, sdkType, p) {
		c.SystemProtected = true
	}
	if sdkType == NodeJS {
		c.ExternalManager = resolveNodeExternalManager(p)
	}
	return c
}

// normalizePathForMatch lowercases p and normalizes both path separators to
// "/" so prefix/segment rules written for Unix also match Windows paths on
// any host. Every path-matching rule in the sdk package goes through this.
func normalizePathForMatch(p string) string {
	return strings.ToLower(strings.ReplaceAll(filepath.ToSlash(p), "\\", "/"))
}

// IsSystemSdkPath reports whether p (binary or directory) sits in an
// OS-managed location for the given SDK type. Generic protected directories
// apply to every SDK type; Python additionally treats distro/framework paths
// (see IsSystemPythonPath) as system-managed. goos selects the platform
// rules, keeping the function testable on any host.
func IsSystemSdkPath(goos string, sdkType SdkType, p string) bool {
	if IsProtectedSystemDir(goos, p) {
		return true
	}
	return sdkType == Python && isSystemPythonPath(goos, p)
}

// IsProtectedSystemDir reports whether dir is an OS-managed directory that
// must never be copied into the SVC store as an SDK. Importing such a
// directory would CopyDir an OS tree (/usr, C:\Windows, ...) into the app's
// storage. goos selects the platform rules, keeping the function testable on
// any host.
func IsProtectedSystemDir(goos, dir string) bool {
	if dir == "" {
		return false
	}
	p := normalizePathForMatch(filepath.Clean(dir))
	var prefixes []string
	switch goos {
	case "darwin":
		prefixes = []string{"/usr/bin", "/bin", "/sbin", "/system", "/library/developer"}
	case "linux":
		prefixes = []string{"/usr/bin", "/usr/sbin", "/bin", "/sbin", "/usr/lib", "/lib"}
	case "windows":
		return strings.HasPrefix(p, "c:/windows") ||
			strings.Contains(p, "microsoft/windowsapps")
	default:
		return false
	}
	for _, prefix := range prefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}
