package pkgmgr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"svc/internal/apperr"
	"svc/internal/config"
	"svc/internal/helpers"
	"svc/internal/logger"
	"svc/internal/sdk"
)

// Service detects and manages SDK package managers (npm/yarn/pnpm,
// composer, pip): version detection, installation and updates, all run
// with the owning SDK scoped to the front of PATH.
type Service struct {
	cfg      *config.Config
	registry *sdk.Registry
	// newCommandContext overrides runScopedCommand's context factory in
	// tests (e.g. a 100ms bound); nil uses newScopedCommandContext (180s).
	newCommandContext func() (context.Context, context.CancelFunc)
}

// New wires a Service. registry may be nil in tests that only exercise the
// pure helpers (parsePipVersion, nodeSupportsCorepack, scoped contexts).
func New(cfg *config.Config, registry *sdk.Registry) *Service {
	return &Service{cfg: cfg, registry: registry}
}

func (s *Service) GetPackageManagers(sdkType string) []sdk.PackageManagerInfo {
	if err := helpers.ValidatePathSegment(sdkType); err != nil {
		return nil
	}
	active := s.cfg.GetActiveVersion(sdkType)
	if active == "" {
		return nil
	}

	switch sdk.SdkType(sdkType) {
	case sdk.NodeJS:
		return []sdk.PackageManagerInfo{
			s.detectPM("npm", "npm", []string{"--version"}, sdk.NodeJS),
			s.detectPM("yarn", "yarn", []string{"--version"}, sdk.NodeJS),
			s.detectPM("pnpm", "pnpm", []string{"--version"}, sdk.NodeJS),
		}
	case sdk.PHP:
		return []sdk.PackageManagerInfo{
			s.detectPM("composer", "composer", []string{"--version"}, sdk.PHP),
		}
	case sdk.Python:
		if runtime.GOOS == "windows" {
			return []sdk.PackageManagerInfo{
				s.detectPM("pip", "python", []string{"-m", "pip", "--version"}, sdk.Python),
			}
		}
		return []sdk.PackageManagerInfo{
			s.detectPM("pip", "pip", []string{"--version"}, sdk.Python),
		}
	default:
		return nil
	}
}

func (s *Service) detectPM(name, cmd string, args []string, parent sdk.SdkType) sdk.PackageManagerInfo {
	scopedPath := s.buildSdkPath(parent)
	fullPath := resolveInPath(cmd, scopedPath)
	if fullPath == cmd {
		return sdk.PackageManagerInfo{Name: name, Installed: false, ParentSdk: parent}
	}
	// H3: Bound version detection so a hung package manager doesn't block.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := helpers.CreateCmdContext(ctx, fullPath, args...)
	c.Env = helpers.ReplacePathEnv(os.Environ(), scopedPath)
	out, err := c.CombinedOutput()
	if err != nil {
		return sdk.PackageManagerInfo{Name: name, Installed: false, ParentSdk: parent}
	}
	ver := strings.TrimSpace(string(out))
	if strings.Contains(ver, "Composer version") {
		parts := strings.Fields(ver)
		if len(parts) >= 3 {
			ver = parts[2]
		}
	}
	if name == "pip" {
		ver = parsePipVersion(string(out))
	}
	return sdk.PackageManagerInfo{Name: name, Version: ver, Installed: true, ParentSdk: parent}
}

func parsePipVersion(raw string) string {
	ver := strings.TrimSpace(raw)
	if strings.HasPrefix(ver, "pip ") {
		parts := strings.Fields(ver)
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	return ver
}

// nodeSupportsCorepack returns true if the Node.js version is >= 16.9.0
// (corepack was introduced in Node.js 16.9.0). Falls back to false on parse error.
func nodeSupportsCorepack(version string) bool {
	version = strings.TrimPrefix(version, "v")
	parts := strings.Split(version, ".")
	major, _ := strconv.Atoi(parts[0])
	// M5: Any major > 16 supports corepack. Checked before the len(parts) < 2
	// guard so single-part versions like "18" (no minor) still return true
	// instead of falling through to false.
	if major > 16 {
		return true
	}
	if len(parts) < 2 {
		return false
	}
	minor, _ := strconv.Atoi(parts[1])
	if major == 16 && minor >= 9 {
		return true
	}
	return false
}

func (s *Service) InstallPackageManager(name string) error {
	switch name {
	case "npm":
		if s.cfg.GetActiveVersion("nodejs") == "" {
			return apperr.New(apperr.NeedSdk, map[string]string{"name": name, "sdk": "Node.js"})
		}
		return apperr.New(apperr.NeedSdk, map[string]string{"name": name, "sdk": "Node.js"})
	case "yarn":
		if s.cfg.GetActiveVersion("nodejs") == "" {
			return apperr.New(apperr.NeedSdk, map[string]string{"name": name, "sdk": "Node.js"})
		}
		return s.installWithCorepackFallback("yarn", "latest", true)
	case "pnpm":
		if s.cfg.GetActiveVersion("nodejs") == "" {
			return apperr.New(apperr.NeedSdk, map[string]string{"name": name, "sdk": "Node.js"})
		}
		return s.installWithCorepackFallback("pnpm", "latest", true)
	case "composer":
		if s.cfg.GetActiveVersion("php") == "" {
			return apperr.New(apperr.NeedSdk, map[string]string{"name": name, "sdk": "PHP"})
		}
		return apperr.New(apperr.ComposerManual, map[string]string{"url": "https://getcomposer.org/download/"})
	case "pip":
		if s.cfg.GetActiveVersion("python") == "" {
			return apperr.New(apperr.NeedSdk, map[string]string{"name": name, "sdk": "Python"})
		}
		return apperr.New(apperr.NeedSdk, map[string]string{"name": name, "sdk": "Python"})
	default:
		return apperr.New(apperr.UnknownPackageManager, map[string]string{"name": name})
	}
}

func (s *Service) UpdatePackageManager(name string) error {
	switch name {
	case "npm":
		return s.runScopedCommand("npm", sdk.NodeJS, "install", "-g", "npm@latest")
	case "yarn":
		return s.installWithCorepackFallback("yarn", "latest", false)
	case "pnpm":
		return s.installWithCorepackFallback("pnpm", "latest", false)
	case "composer":
		return s.runScopedCommand("composer", sdk.PHP, "self-update")
	case "pip":
		return s.runScopedCommand("python", sdk.Python, "-m", "pip", "install", "--upgrade", "pip")
	default:
		return apperr.New(apperr.UnknownPackageManager, map[string]string{"name": name})
	}
}

// buildSdkPath builds a PATH containing only the bin directories of the specified SDK's active version
func (s *Service) buildSdkPath(parent sdk.SdkType) string {
	active := s.cfg.GetActiveVersion(string(parent))
	if active == "" {
		return ""
	}
	f := s.registry.Get(parent)
	if f == nil {
		return ""
	}
	versionDir := s.cfg.SdkVersionDir(string(parent), active)
	var paths []string
	for _, binDir := range f.GetBinDirs() {
		if binDir == "" {
			paths = append(paths, versionDir)
		} else {
			paths = append(paths, filepath.Join(versionDir, binDir))
		}
	}
	sep := ":"
	if os.PathListSeparator == ';' {
		sep = ";"
	}
	return strings.Join(paths, sep)
}

// resolveInPath looks up a command in the specified PATH (bypasses system PATH)
func resolveInPath(cmd, searchPath string) string {
	if searchPath == "" {
		return cmd
	}
	sep := ";"
	exts := []string{""}
	if os.PathListSeparator == ':' {
		sep = ":"
	} else {
		exts = []string{"", ".exe", ".cmd", ".bat"}
	}
	for _, dir := range strings.Split(searchPath, sep) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		for _, ext := range exts {
			p := filepath.Join(dir, cmd+ext)
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p
			}
		}
	}
	return cmd
}

// installWithCorepackFallback installs/updates name (yarn|pnpm) at
// versionSpec via corepack when the active Node.js supports it, falling back
// to `npm install -g name@versionSpec` when corepack is missing or fails.
// Node >= 25 no longer ships corepack, and corepack prepare breaks when a
// package's npm registry signing key rotates (corepack 0.29.4 was the last
// release). A successful `corepack enable` followed by a failed prepare may
// leave corepack shims behind; the npm fallback overwrites them. The worst
// case is two bounded commands (corepack 180s + npm 180s). enableCorepack is
// true for fresh installs, false for updates.
func (s *Service) installWithCorepackFallback(name, versionSpec string, enableCorepack bool) error {
	if nodeSupportsCorepack(s.cfg.GetActiveVersion("nodejs")) {
		corepackOK := true
		if enableCorepack {
			if err := s.runScopedCommand("corepack", sdk.NodeJS, "enable"); err != nil {
				logger.Warn("corepack enable failed (%v); falling back to npm install -g %s@%s", err, name, versionSpec)
				corepackOK = false
			}
		}
		if corepackOK {
			if err := s.runScopedCommand("corepack", sdk.NodeJS, "prepare", name+"@"+versionSpec, "--activate"); err == nil {
				return nil
			} else {
				logger.Warn("corepack prepare %s@%s failed (%v); falling back to npm install -g", name, versionSpec, err)
			}
		}
	}
	return s.runScopedCommand("npm", sdk.NodeJS, "install", "-g", name+"@"+versionSpec)
}

// scopedCommandTimeout bounds package-manager install/update commands so a
// hung process doesn't block forever. 180s (not 60s): corepack prepare and
// npm install -g routinely exceed a minute on slow networks or registries,
// and a mid-install timeout leaves the package manager in a half-done state.
const scopedCommandTimeout = 180 * time.Second

// newScopedCommandContext returns a context bounded by scopedCommandTimeout
// for runScopedCommand. Extracted so the bound is unit-testable.
func newScopedCommandContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), scopedCommandTimeout)
}

// outputTailLen bounds how much command output rides along in an exec-failed
// marker; long npm/corepack transcripts must not bloat the error JSON.
const outputTailLen = 500

// truncatedTail returns the last maxLen characters of s (whitespace-trimmed),
// prefixed with "..." when truncated. The cut snaps forward past UTF-8
// continuation bytes so the result never contains a split rune.
func truncatedTail(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	cut := len(s) - maxLen
	for cut < len(s) && s[cut]&0xC0 == 0x80 {
		cut++
	}
	return "..." + s[cut:]
}

// runScopedCommandCapture runs a command within the PATH scope of the
// specified SDK and returns its whitespace-trimmed combined output. Failures
// return the output alongside an apperr.ExecFailed marker carrying the
// command line and a bounded detail (timeout note, output tail, or the exec
// error such as "executable file not found") so the frontend can translate
// them instead of showing raw "exit status 1".
func (s *Service) runScopedCommandCapture(name string, parent sdk.SdkType, args ...string) (string, error) {
	scopedPath := s.buildSdkPath(parent)
	fullPath := resolveInPath(name, scopedPath)
	// H3: Bound install/update commands so a hung process doesn't block forever.
	newCtx := s.newCommandContext
	if newCtx == nil {
		newCtx = newScopedCommandContext
	}
	ctx, cancel := newCtx()
	defer cancel()
	cmd := helpers.CreateCmdContext(ctx, fullPath, args...)
	cmd.Env = helpers.ReplacePathEnv(os.Environ(), scopedPath)
	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	cmdLine := strings.TrimSpace(name + " " + strings.Join(args, " "))
	if err == nil {
		if trimmed != "" {
			logger.Info("%s succeeded: %s", cmdLine, truncatedTail(trimmed, outputTailLen))
		}
		return trimmed, nil
	}
	var detail string
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		detail = fmt.Sprintf("timed out after %v", scopedCommandTimeout)
	case trimmed != "":
		detail = truncatedTail(trimmed, outputTailLen)
	default:
		detail = err.Error()
	}
	return trimmed, apperr.New(apperr.ExecFailed, map[string]string{"cmd": cmdLine, "detail": detail})
}

// runScopedCommand runs a command within the PATH scope of the specified SDK,
// discarding its output. See runScopedCommandCapture for error semantics.
func (s *Service) runScopedCommand(name string, parent sdk.SdkType, args ...string) error {
	_, err := s.runScopedCommandCapture(name, parent, args...)
	return err
}
