package pathmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"svc/internal/config"
	"svc/internal/fsutil"
	"svc/internal/sdk"
)

// detectSdkTypeFromPath infers the SDK type from keywords in the path
func detectSdkTypeFromPath(p string) string {
	lower := strings.ToLower(filepath.ToSlash(p))
	switch {
	case strings.Contains(lower, "/node") || strings.Contains(lower, "/nodejs") || strings.Contains(lower, "\\node"):
		return "nodejs"
	case strings.Contains(lower, "/jdk") || strings.Contains(lower, "/java") || strings.Contains(lower, "\\jdk"):
		return "jdk"
	case strings.Contains(lower, "/go/") || strings.Contains(lower, "\\go\\") ||
		strings.HasSuffix(lower, "/go/bin") || strings.HasSuffix(lower, "\\go\\bin"):
		return "go"
	case strings.Contains(lower, "/python") || strings.Contains(lower, "\\python"):
		return "python"
	case strings.Contains(lower, "/rust") || strings.Contains(lower, "\\rust") ||
		strings.Contains(lower, "/cargo") || strings.Contains(lower, "\\cargo"):
		return "rust"
	case strings.Contains(lower, "/ruby") || strings.Contains(lower, "\\ruby"):
		return "ruby"
	case strings.Contains(lower, "/dotnet") || strings.Contains(lower, "\\dotnet"):
		return "dotnet"
	case strings.Contains(lower, "/php") || strings.Contains(lower, "\\php"):
		return "php"
	case strings.Contains(lower, "/perl") || strings.Contains(lower, "\\perl") ||
		strings.Contains(lower, "/strawberry") || strings.Contains(lower, "\\strawberry"):
		return "perl"
	case strings.Contains(lower, "/maven") || strings.Contains(lower, "/mvn") || strings.Contains(lower, "\\maven"):
		return "maven"
	case strings.Contains(lower, "/gradle") || strings.Contains(lower, "\\gradle"):
		return "gradle"
	case strings.Contains(lower, "/flutter") || strings.Contains(lower, "\\flutter"):
		return "flutter"
	case strings.Contains(lower, "/android") || strings.Contains(lower, "\\android"):
		return "android"
	case strings.Contains(lower, "/dart") || strings.Contains(lower, "\\dart"):
		return "dart"
	}
	return ""
}

// detectSdkTypesByBin checks whether a directory contains characteristic executables.
// Returns ALL matching SDK types (not just the first), so a directory like /usr/bin
// with node+python+go+rust gets all four detected.
func detectSdkTypesByBin(dir string) []string {
	dirs := []string{dir, filepath.Join(dir, "bin")}
	checks := []struct {
		bin     string
		sdkType string
	}{
		{"node", "nodejs"},
		{"javac", "jdk"},
		{"go", "go"},
		{"python3", "python"},
		{"python", "python"},
		{"rustc", "rust"},
		{"cargo", "rust"},
		{"ruby", "ruby"},
		{"dotnet", "dotnet"},
		{"php", "php"},
		{"perl", "perl"},
		{"mvn", "maven"},
		{"gradle", "gradle"},
		{"flutter", "flutter"},
		{"sdkmanager", "android"},
		{"dart", "dart"},
	}
	var types []string
	for _, d := range dirs {
		for _, c := range checks {
			for _, ext := range []string{"", ".exe", ".cmd", ".bat"} {
				if _, err := os.Stat(filepath.Join(d, c.bin+ext)); err == nil {
					if !sliceContains(types, c.sdkType) {
						types = append(types, c.sdkType)
					}
					break
				}
			}
		}
	}
	if len(types) == 0 {
		if t := detectSdkTypeFromPath(dir); t != "" {
			return []string{t}
		}
	}
	return types
}

func sliceContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// IsProtectedSystemDir reports whether dir is an OS-managed directory that
// must never be copied into the SVC store as an SDK. Importing such a
// directory would CopyDir an OS tree (/usr, C:\Windows, ...) into the app's
// storage. goos selects the platform rules, keeping the function testable on
// any host.
// buildUnmanagedEntries expands a non-SVC PATH directory into display
// entries: one per SDK type whose binaries the directory provides. SDK types
// already managed by SVC (active version set) are dropped: under the shims
// model the app-managed binaries are not in PATH themselves, so without this
// filter a system copy (e.g. /usr/bin/python3) keeps appearing as importable
// even though SVC already manages that SDK. OS-protected directories and
// copies owned by external version managers are flagged so the UI can block
// or re-label their import action. cfg may be nil in tests.
func buildUnmanagedEntries(p string, cfg *config.Config) []PathEntry {
	sdkTypes := detectSdkTypesByBin(p)
	if len(sdkTypes) == 0 {
		return []PathEntry{{Path: p}}
	}
	var entries []PathEntry
	for _, st := range sdkTypes {
		if cfg != nil && cfg.GetActiveVersion(st) != "" {
			continue // SVC already manages this SDK type
		}
		// Central classification (protected OS dirs, external managers,
		// hidden stubs) -- single source of truth in the sdk package.
		cl := sdk.ClassifyPathCopy(sdk.SdkType(st), p)
		if cl.Hidden {
			continue
		}
		entries = append(entries, PathEntry{
			Path:            p,
			SdkType:         st,
			SystemProtected: cl.SystemProtected,
			ExternalManager: cl.ExternalManager,
		})
	}
	return entries
}

// hasPathPrefix reports whether p equals dir or lies inside dir. Unlike
// strings.HasPrefix(p, dir), it requires a path separator at the boundary so
// /a/b does not match /a/bc (item 4). An empty dir matches nothing: the old
// strings.HasPrefix(p, "") matched EVERY path and would have filtered the
// whole PATH if ever fed an empty dir.
func hasPathPrefix(p, dir string) bool {
	if dir == "" {
		return false
	}
	if p == dir {
		return true
	}
	return strings.HasPrefix(p, dir+string(os.PathSeparator))
}

// hasSvcSegment reports whether p contains ".svc" as a COMPLETE path
// segment, e.g. /home/u/.svc/shims or C:\Users\u\.svc\shims. It replaces
// unanchored strings.Contains(p, ".svc") checks, which also matched
// unrelated paths like /home/u/.svcx or /opt/my.svc.d (item 12).
func hasSvcSegment(p string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".svc" {
			return true
		}
	}
	return false
}

// DetectSdkRoot walks up from the bin directory to find the SDK root.
// For SDK types with a known layout signature (go/jdk/nodejs) the candidate
// is validated and "" is returned when validation FAILS — item 11 previously
// returned the unvalidated candidate, letting a wrong/incomplete directory
// be treated as the SDK root. For SDK types without a validation rule the
// candidate is returned as-is (there is nothing to check against; returning
// "" would break those imports).
func DetectSdkRoot(binDir string, sdkType string) string {
	binDir = filepath.Clean(binDir)
	candidate := binDir
	if strings.ToLower(filepath.Base(candidate)) == "bin" {
		candidate = filepath.Dir(candidate)
	}

	// Verify root directory
	switch sdkType {
	case "go":
		if _, err := os.Stat(filepath.Join(candidate, "bin")); err == nil {
			return candidate
		}
		return "" // validation failed
	case "jdk":
		if _, err := os.Stat(filepath.Join(candidate, "release")); err == nil {
			return candidate
		}
		return "" // validation failed
	case "nodejs":
		// Node.js root contains the node executable
		for _, ext := range []string{"", ".exe"} {
			if _, err := os.Stat(filepath.Join(candidate, "node"+ext)); err == nil {
				return candidate
			}
			if _, err := os.Stat(filepath.Join(candidate, "bin", "node"+ext)); err == nil {
				return candidate
			}
		}
		return "" // validation failed
	}
	// No validation rule for this SDK type: return the candidate unchanged.
	return candidate
}

// DeduplicateEntries dedupes entries by SDK type + path, keeping only one
// record per unique (sdkType, path) pair. The original PATH entry path is
// preserved for display — DetectSdkRoot is NOT applied to e.Path so the
// user sees the real PATH directory (e.g. /usr/bin, not /usr).
func DeduplicateEntries(entries []PathEntry) []PathEntry {
	seen := make(map[string]int) // key -> index in result
	var result []PathEntry

	for _, e := range entries {
		key := e.SdkType + ":" + strings.ToLower(filepath.Clean(e.Path))

		if idx, exists := seen[key]; exists {
			if e.IsManaged {
				result[idx].IsManaged = true
			}
			continue
		}

		seen[key] = len(result)
		result = append(result, e)
	}
	return result
}

// GetDesktopDir returns the current user's desktop directory (cross-platform).
// If no Desktop directory exists it falls back to the home directory WITHOUT
// creating one — item 13 previously created ~/Desktop as a side effect, which
// surprises users on machines that intentionally have no Desktop (servers,
// headless boxes, custom layouts).
func GetDesktopDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	desktop := filepath.Join(home, "Desktop")
	if info, err := os.Stat(desktop); err == nil && info.IsDir() {
		return desktop, nil
	}

	// No usable Desktop: fall back to home. Do not create the directory.
	return home, nil
}

// AlignImportLayout fixes the directory layout of an imported SDK so its
// GetBinDirs() resolve correctly.
//
// Download-install extracts the archive verbatim, so for SDKs whose
// StripArchiveTopDir()=false the top-level wrapper dir is preserved and
// GetBinDirs() carries it (e.g. Go: "go/bin", Python: "python/bin",
// Dart: "dart-sdk/bin"). Import-install, however, calls DetectSdkRoot which
// returns the wrapper dir itself (e.g. GOROOT for Go), and CopyDir copies its
// *contents* — so the wrapper dir is lost and binDirs like "go/bin" no longer
// resolve (the real files land flat under "bin/").
//
// This function detects that mismatch and re-wraps the content: if binDirs
// expects "<top>/<...>" but targetDir already has "<...>" at the top level,
// it creates "<top>/" and moves everything into it. No-op when the layout
// already matches (download-install path, or SDKs with StripArchiveTopDir=true
// whose binDirs are just "bin"/"").
func AlignImportLayout(targetDir string, binDirs []string) error {
	for _, bd := range binDirs {
		if bd == "" {
			continue
		}
		parts := strings.Split(filepath.ToSlash(bd), "/")
		if len(parts) < 2 {
			continue // single-segment binDir (e.g. "bin") — nothing to wrap
		}
		top := parts[0]
		// Expected wrapper dir already present → layout is correct.
		if _, err := os.Stat(filepath.Join(targetDir, top)); err == nil {
			return nil
		}
		// Wrapper missing but the inner path exists flat → re-wrap.
		// Verify the flat layout actually exists (e.g. targetDir/bin/go.exe).
		flatCheck := filepath.Join(targetDir, parts[len(parts)-1])
		if _, err := os.Stat(flatCheck); err != nil {
			// Flat layout doesn't exist either; nothing we can safely do.
			continue
		}
		// Move everything in targetDir into targetDir/<top>/.
		wrapperDir := filepath.Join(targetDir, top)
		if err := os.MkdirAll(wrapperDir, 0755); err != nil {
			return fmt.Errorf("align layout: create wrapper dir: %w", err)
		}
		entries, err := os.ReadDir(targetDir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Name() == top {
				continue // don't move the wrapper into itself
			}
			src := filepath.Join(targetDir, e.Name())
			dst := filepath.Join(wrapperDir, e.Name())
			if err := os.Rename(src, dst); err != nil {
				return fmt.Errorf("align layout: move %s: %w", e.Name(), err)
			}
		}
		return nil
	}
	return nil
}

// BackupDir copies a directory to the desktop with a timestamped name
func BackupDir(src string) (string, error) {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return "", fmt.Errorf("source directory does not exist: %s", src)
	}

	desktop, err := GetDesktopDir()
	if err != nil {
		return "", fmt.Errorf("failed to get desktop directory: %w", err)
	}

	baseName := filepath.Base(src)
	timestamp := time.Now().Format("20060102-150405")
	backupName := fmt.Sprintf("%s_backup_%s", baseName, timestamp)
	backupPath := filepath.Join(desktop, backupName)

	if _, err := os.Stat(backupPath); err == nil {
		// Edge case: multiple backups in the same second; add a random suffix
		backupPath = filepath.Join(desktop, fmt.Sprintf("%s_%d", backupName, time.Now().UnixNano()%10000))
	}

	if err := fsutil.CopyDir(src, backupPath); err != nil {
		return "", fmt.Errorf("failed to backup directory: %w", err)
	}

	return backupPath, nil
}
