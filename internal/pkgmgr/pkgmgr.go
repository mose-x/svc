package pkgmgr

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"svc/internal/apperr"
	"svc/internal/config"
	"svc/internal/helpers"
	"svc/internal/sdk"
)

// Service detects and manages SDK package managers (npm/yarn/pnpm,
// composer, pip): version detection, installation and updates, all run
// with the owning SDK scoped to the front of PATH.
type Service struct {
	cfg      *config.Config
	registry *sdk.Registry
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
		if nodeSupportsCorepack(s.cfg.GetActiveVersion("nodejs")) {
			if err := s.runScopedCommand("corepack", sdk.NodeJS, "enable"); err != nil {
				return err
			}
			return s.runScopedCommand("corepack", sdk.NodeJS, "prepare", "yarn@latest", "--activate")
		}
		return s.runScopedCommand("npm", sdk.NodeJS, "install", "-g", "yarn")
	case "pnpm":
		if s.cfg.GetActiveVersion("nodejs") == "" {
			return apperr.New(apperr.NeedSdk, map[string]string{"name": name, "sdk": "Node.js"})
		}
		if nodeSupportsCorepack(s.cfg.GetActiveVersion("nodejs")) {
			if err := s.runScopedCommand("corepack", sdk.NodeJS, "enable"); err != nil {
				return err
			}
			return s.runScopedCommand("corepack", sdk.NodeJS, "prepare", "pnpm@latest", "--activate")
		}
		return s.runScopedCommand("npm", sdk.NodeJS, "install", "-g", "pnpm")
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
		if nodeSupportsCorepack(s.cfg.GetActiveVersion("nodejs")) {
			return s.runScopedCommand("corepack", sdk.NodeJS, "prepare", "yarn@latest", "--activate")
		}
		return s.runScopedCommand("npm", sdk.NodeJS, "install", "-g", "yarn@latest")
	case "pnpm":
		if nodeSupportsCorepack(s.cfg.GetActiveVersion("nodejs")) {
			return s.runScopedCommand("corepack", sdk.NodeJS, "prepare", "pnpm@latest", "--activate")
		}
		return s.runScopedCommand("npm", sdk.NodeJS, "install", "-g", "pnpm@latest")
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

// runScopedCommand runs a command within the PATH scope of the specified SDK
func (s *Service) runScopedCommand(name string, parent sdk.SdkType, args ...string) error {
	scopedPath := s.buildSdkPath(parent)
	fullPath := resolveInPath(name, scopedPath)
	// H3: Bound install/update commands so a hung process doesn't block forever.
	ctx, cancel := newScopedCommandContext()
	defer cancel()
	cmd := helpers.CreateCmdContext(ctx, fullPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = helpers.ReplacePathEnv(os.Environ(), scopedPath)
	return cmd.Run()
}
