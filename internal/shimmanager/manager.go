package shimmanager

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"svc/internal/config"
	"svc/internal/logger"
	"svc/internal/shim"
)

// Manager handles shim creation/removal and .svc.rc file management.
type Manager struct {
	cfg *config.Config
}

// New creates a shim Manager.
func New(cfg *config.Config) *Manager {
	return &Manager{cfg: cfg}
}

// EnsureSetup performs one-time initialization:
// 1. Creates the shims directory
// 2. Copies the app binary as the base shim (svc-shim)
// 3. On Unix: adds source line to shell rc (only once)
// 4. On Windows: adds shims dir to registry PATH (only once)
func (m *Manager) EnsureSetup() error {
	shimsDir := m.cfg.ShimsDir()
	if err := os.MkdirAll(shimsDir, 0755); err != nil {
		return fmt.Errorf("failed to create shims directory: %w", err)
	}

	if err := m.ensureShimBinary(); err != nil {
		return fmt.Errorf("failed to install shim binary: %w", err)
	}

	// Ensure the python3 -> python alias is registered + shim created on every
	// startup, not only when the shim binary is updated. Without this, a stale
	// shims.json (missing python3) with a current shim binary would leave
	// python3 unrouteable forever (ensurePython3Alias previously only ran
	// inside rebuildCommandShims, which is skipped when needUpdate=false).
	m.ensurePython3Shim()

	return m.ensurePathEntry()
}

// ensurePython3Shim registers the python3 -> python command alias in
// shims.json (if Python is configured but python3 is missing) and creates the
// python3 shim file. Runs on every startup (EnsureSetup), independent of
// whether the shim binary was updated, so a stale shims.json self-heals. If
// persisting the alias fails, the shim file is NOT created (avoids leaving an
// orphan hardlink that routes to "unknown command"). Windows CPython ships
// python.exe (no python3.exe); without this alias `python3` resolves to the
// Windows Store stub.
func (m *Manager) ensurePython3Shim() {
	// The load+save of shims.json runs under the cross-process lock (item 3)
	// so a concurrent writer can't be clobbered when we persist the alias.
	persisted := false
	err := withShimsConfigLock(m.cfg.SvcDir(), func() error {
		cfg, err := m.loadShimConfig()
		if err != nil {
			return fmt.Errorf("failed to load shim config: %w", err)
		}
		if !m.ensurePython3Alias(&cfg) {
			return nil // nothing to persist
		}
		// Persist BEFORE creating the shim file: if save fails, the on-disk
		// shims.json won't have python3, so a shim file would be an unrouteable
		// orphan (hardlink exists, but shim.Run can't resolve the command).
		if err := m.saveShimConfig(m.cfg.ShimsConfigPath(), cfg); err != nil {
			return fmt.Errorf("failed to persist python3 alias: %w", err)
		}
		persisted = true
		return nil
	})
	if err != nil {
		logger.Warn("Failed to persist python3 alias: %v", err)
		return
	}
	if !persisted {
		return
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	if err := m.createShimFor("python3", ext); err != nil {
		logger.Warn("Failed to create python3 shim: %v", err)
	}
}

// ensureShimBinary installs ~/.svc/shims/svc-shim[.exe] if it is missing or
// outdated (compared by file size + modtime). When the base shim is actually
// replaced, all existing command shims (node.exe, go.exe, ...) are rebuilt so
// they point at the new binary instead of lingering as hardlinks to the
// previous version's svc-shim. On Windows, os.Remove on svc-shim.exe leaves
// existing hardlinks (node.exe) pointing at the old inode, so the old binary
// keeps running until the hardlink is explicitly recreated.
//
// On Windows the shim is the embedded console-subsystem binary
// (embeddedShimBinary), NOT a copy of the app binary: the app binary is built
// with -H windowsgui (no console), so hardlinking node.exe to it makes
// `node -v` print nothing and leaves cmd.exe's prompt stuck. A console
// subsystem shim gets a real stdio handle from cmd.exe and the prompt redraws
// on exit, exactly like the real node.exe. If the embed is empty (dev build
// without the prebuild step), it falls back to copying the app binary; the
// AttachConsole fallback in the shim package then keeps output working, with
// the known prompt-hang trade-off.
func (m *Manager) ensureShimBinary() error {
	appPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine app path: %w", err)
	}

	shimName := "svc-shim"
	if runtime.GOOS == "windows" {
		shimName = "svc-shim.exe"
	}
	shimPath := filepath.Join(m.cfg.ShimsDir(), shimName)

	appInfo, err := os.Stat(appPath)
	if err != nil {
		return fmt.Errorf("cannot stat app binary: %w", err)
	}

	// On Windows, prefer the embedded console-subsystem shim binary over a
	// copy of the GUI-subsystem app binary. Falls back to the app binary when
	// the embed is empty (dev build without the prebuild step).
	useEmbedded := runtime.GOOS == "windows" && len(embeddedShimBinary) > 0

	expectedSize := appInfo.Size()
	if useEmbedded {
		expectedSize = int64(len(embeddedShimBinary))
	}

	needUpdate := true
	if shimInfo, err := os.Stat(shimPath); err == nil {
		if shimInfo.Size() == expectedSize && !shimInfo.ModTime().Before(appInfo.ModTime()) {
			// Size + modtime match, but that's not enough: two builds can
			// land at the same file size with different bytes (e.g. a
			// rebuild after a tiny code change). Without a content check,
			// needUpdate=false skips rebuildCommandShims, and on Windows the
			// existing command hardlinks (node.exe, ...) keep pointing at
			// the OLD svc-shim inode. Compare the on-disk shim bytes to the
			// expected bytes; only when they match can we skip the update.
			var expected []byte
			if useEmbedded {
				expected = embeddedShimBinary
			} else if b, err := os.ReadFile(appPath); err == nil {
				expected = b
			}
			if filesEqual(shimPath, expected) {
				needUpdate = false
			}
		}
	}

	if !needUpdate {
		return nil
	}

	logger.Info("Shim binary is outdated, updating...")

	writeNew := func(dst string) error {
		if useEmbedded {
			return os.WriteFile(dst, embeddedShimBinary, 0755)
		}
		return copyFile(appPath, dst, 0755)
	}
	if err := replaceShimBinary(shimPath, writeNew); err != nil {
		return fmt.Errorf("failed to update shim binary: %w", err)
	}

	logger.Info("Shim binary installed at: %s", shimPath)

	// Rebuild every existing command shim so its hardlink/copy targets the
	// freshly written svc-shim. Without this, command shims created by a
	// previous app version keep pointing at the old binary on Windows
	// (Remove+rewrite of svc-shim.exe leaves prior hardlinks intact on the
	// old inode), so users keep running the stale shim after an update.
	m.rebuildCommandShims()
	return nil
}

// replaceShimBinary atomically replaces the shim binary at shimPath using
// the content produced by writeNew. The naive Remove+WriteFile it replaces
// failed on Windows whenever a shim process was running: svc-shim.exe is
// write-locked by the OS while executing, so both Remove and WriteFile fail
// with a sharing violation and EnsureSetup aborted the whole SDK operation.
//
// Strategy:
//  1. Write the new binary to shimPath+".new" (stale leftovers from an
//     interrupted run are removed first).
//  2. Rename over the target. This always works on Unix (rename(2) replaces
//     atomically, even of a running binary) and on Windows while the shim
//     is not running.
//  3. Windows fallback when step 2 hits a sharing violation: a RUNNING exe
//     cannot be deleted or overwritten, but it CAN be renamed. Rename the
//     live binary to shimPath+".old" (best-effort cleanup of any previous
//     leftover first), move the new file into place, then remove the .old
//     file if nothing still holds it.
func replaceShimBinary(shimPath string, writeNew func(dst string) error) error {
	tmpPath := shimPath + ".new"
	os.Remove(tmpPath) // stale from an interrupted previous run
	if err := writeNew(tmpPath); err != nil {
		os.Remove(tmpPath)
		return err
	}

	if err := os.Rename(tmpPath, shimPath); err == nil {
		return nil
	}

	// Locked target (Windows running exe). Rename it away, then install.
	oldPath := shimPath + ".old"
	os.Remove(oldPath) // works once the previous shim process has exited
	if err := os.Rename(shimPath, oldPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("shim binary is locked and could not be moved aside: %w", err)
	}
	if err := os.Rename(tmpPath, shimPath); err != nil {
		// Try to restore the old binary so the shim keeps working.
		os.Rename(oldPath, shimPath)
		return err
	}
	os.Remove(oldPath) // best-effort; stays until the running shim exits
	return nil
}

// rebuildCommandShims recreates the shim file for every command registered in
// shims.json. The command→sdkType mapping and binDirs are preserved; only the
// on-disk shim file (hardlink to svc-shim) is rebuilt.
func (m *Manager) rebuildCommandShims() {
	cfg, _ := m.loadShimConfig()
	if len(cfg.Commands) == 0 {
		return
	}
	// NOTE: the python3 alias is ensured separately by ensurePython3Shim
	// (called from EnsureSetup on every startup, independent of whether the
	// shim binary was updated). This function only rebuilds the on-disk shim
	// files for commands already registered in shims.json.
	rebuilt := 0
	for cmd := range cfg.Commands {
		// Reconstruct the extension of any existing shim variant. Normally
		// .exe on Windows / no extension on Unix; legacy installs may still
		// have .cmd/.bat batch wrappers, which createShimFor migrates to
		// .exe (see shimExtFor).
		ext := ""
		if runtime.GOOS == "windows" {
			for _, candidate := range []string{".exe", ".cmd", ".bat"} {
				if _, err := os.Stat(filepath.Join(m.cfg.ShimsDir(), cmd+candidate)); err == nil {
					ext = candidate
					break
				}
			}
			if ext == "" {
				ext = ".exe"
			}
		}
		if err := m.createShimFor(cmd, ext); err != nil {
			logger.Warn("Failed to rebuild shim for %s: %v", cmd, err)
			continue
		}
		rebuilt++
	}
	if rebuilt > 0 {
		logger.Info("Rebuilt %d command shims to match updated svc-shim", rebuilt)
	}
}

// ensurePython3Alias registers the python3 -> python command alias in cfg if
// Python is configured but python3 isn't registered yet. Returns true if cfg
// was modified (caller persists it and the rebuild loop creates the shim file).
// Windows CPython ships python.exe (no python3.exe); without this alias
// `python3` resolves to the Windows Store stub. Called from
// rebuildCommandShims so a self-update + restart auto-adds python3 without
// reinstalling Python.
func (m *Manager) ensurePython3Alias(cfg *shim.ShimConfig) bool {
	if _, ok := cfg.SdkTypes["python"]; !ok {
		return false
	}
	if _, ok := cfg.Commands["python3"]; ok {
		return false
	}
	if cfg.Commands == nil {
		cfg.Commands = make(map[string]string)
	}
	cfg.Commands["python3"] = "python"
	return true
}

// ConfigureSdk creates shims for all executables across the SDK's bin
// directories, updates shims.json with the command→sdkType mapping, and
// updates .svc.rc. binDirs are relative to versionDir; "" means versionDir
// itself. Earlier binDirs win on command-name conflicts (first match wins).
func (m *Manager) ConfigureSdk(sdkType string, versionDir string, binDirs []string, extraEnvVars map[string]string) error {
	// Scan all bin directories for executables and create shims.
	// Commands are deduped by name across dirs (first dir wins).
	createdShims, err := m.createShimsForDirs(versionDir, binDirs, sdkType)
	if err != nil {
		logger.Warn("Failed to create some shims for %s: %v", sdkType, err)
	}

	// Update shims.json with the SDK config
	if err := m.updateShimConfig(sdkType, binDirs, extraEnvVars, createdShims); err != nil {
		return fmt.Errorf("failed to update shim config: %w", err)
	}

	// Update .svc.rc with env vars
	if err := m.updateRcFile(); err != nil {
		logger.Warn("Failed to update .svc.rc: %v", err)
	}

	logger.Info("Configured shims for %s: %d commands", sdkType, len(createdShims))
	return nil
}

// RemoveSdk removes all shims for the given SDK type and updates config files.
func (m *Manager) RemoveSdk(sdkType string, extraEnvVars map[string]string) error {
	// Find and remove all shims belonging to this SDK type
	commands, err := m.getCommandsForSdkType(sdkType)
	if err != nil {
		return err
	}

	for _, cmd := range commands {
		m.removeShim(cmd)
	}

	// Drop the SDK's env vars from the OS-level store (Windows registry) so a
	// leftover JAVA_HOME doesn't point at a now-uninstalled JDK. No-op on
	// Unix where .svc.rc regeneration is the only path.
	//
	// Item 5: the keys come from the UNION of the caller-provided extraEnvVars
	// and the EnvVars recorded in shims.json for this SDK type. Relying only
	// on extraEnvVars leaks registry vars when the caller passes an empty or
	// stale map (the shims.json entry is the durable record of what was set).
	// Read the stored config BEFORE removeSdkFromConfig deletes the entry.
	storedCfg, _ := m.loadShimConfig()
	var storedEnv map[string]string
	if entry, ok := storedCfg.SdkTypes[sdkType]; ok {
		storedEnv = entry.EnvVars
	}
	m.removeEnvVarsFromSystem(mergeEnvVarKeys(storedEnv, extraEnvVars))

	// Remove SDK type from shims.json
	if err := m.removeSdkFromConfig(sdkType); err != nil {
		return fmt.Errorf("failed to remove SDK from shim config: %w", err)
	}

	// Update .svc.rc (remove env vars for this SDK)
	if err := m.updateRcFile(); err != nil {
		logger.Warn("Failed to update .svc.rc: %v", err)
	}

	logger.Info("Removed shims for %s: %d commands", sdkType, len(commands))
	return nil
}

// mergeEnvVarKeys returns the sorted union of the env-var keys of two maps
// (either may be nil). Sorting makes the deletion order deterministic for
// tests and logs. Pure logic — no filesystem — so it is trivially testable.
func mergeEnvVarKeys(a, b map[string]string) []string {
	set := make(map[string]bool, len(a)+len(b))
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// createShimsForDirs scans all bin directories (relative to versionDir) and
// creates a shim for each executable. Commands are deduped by name across all
// dirs: the first dir (in binDirs order) that provides a command wins. This
// matters for SDKs like Rust where cargo/bin hardlinks to rustc/bin/rustc;
// we want cargo/bin's copy to be the one shimmed (it is the merged entry).
func (m *Manager) createShimsForDirs(versionDir string, binDirs []string, sdkType string) ([]string, error) {
	created := make(map[string]bool)
	var result []string
	for _, binDir := range binDirs {
		binPath := versionDir
		if binDir != "" {
			binPath = filepath.Join(versionDir, binDir)
		}
		entries, err := os.ReadDir(binPath)
		if err != nil {
			logger.Warn("Cannot read bin directory %s: %v", binPath, err)
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()

			cmdName, ext, ok := classifyExecutable(name)
			if !ok {
				continue
			}
			if runtime.GOOS != "windows" {
				info, err := entry.Info()
				if err != nil {
					continue
				}
				if info.Mode().Perm()&0111 == 0 {
					continue
				}
			}
			if created[cmdName] {
				continue
			}
			if err := m.createShimFor(cmdName, ext); err != nil {
				logger.Warn("Failed to create shim for %s: %v", name, err)
				continue
			}
			created[cmdName] = true
			result = append(result, cmdName)
		}
	}

	// Python on Windows ships python.exe but no python3.exe (python3 is a Unix
	// convention). Alias python3 -> python so `python3` works on Windows like on
	// Unix. On Unix the bin usually already has a python3 symlink (created
	// above), so the !created guard skips it.
	if sdkType == "python" && !created["python3"] {
		ext := ""
		if runtime.GOOS == "windows" {
			ext = ".exe"
		}
		if err := m.createShimFor("python3", ext); err != nil {
			logger.Warn("Failed to create python3 shim: %v", err)
		} else {
			created["python3"] = true
			result = append(result, "python3")
		}
	}

	return result, nil
}

// classifyExecutable splits a filename into command name (without extension),
// extension, and whether it is shim-able on the current OS.
func classifyExecutable(name string) (cmdName, ext string, ok bool) {
	if runtime.GOOS == "windows" {
		lower := strings.ToLower(name)
		switch {
		case strings.HasSuffix(lower, ".exe"):
			return name[:len(name)-len(".exe")], ".exe", true
		case strings.HasSuffix(lower, ".cmd"):
			return name[:len(name)-len(".cmd")], ".cmd", true
		case strings.HasSuffix(lower, ".bat"):
			return name[:len(name)-len(".bat")], ".bat", true
		default:
			return "", "", false
		}
	}
	return name, "", true
}

// shimExtFor returns the on-disk extension for a shim file. .cmd/.bat
// targets also get a .exe shim (a hardlink to svc-shim.exe): naming the
// shim itself .cmd would make cmd.exe interpret the PE bytes as a batch
// script, and a batch wrapper's %* expansion RE-PARSES arguments -- a `^&`
// that correctly survived the caller's first parse gets split into two
// commands on the wrapper's second parse. The .exe shim receives argv
// intact (hardlink/argv[0] mode) and forwards through exec_windows'
// cmd-safe escaping when the real target is a .cmd/.bat. Pure for
// testability.
func shimExtFor(ext string) string {
	if ext == ".cmd" || ext == ".bat" {
		return ".exe"
	}
	return ext
}

// safeShimName reports whether name is safe to use as a shim filename.
//
// Shim names come from an SDK's bin/ listing and are persisted in shims.json,
// so they are untrusted input that must not be able to escape the shims
// directory. Path separators, whitespace and shell metacharacters are
// rejected, as are empty names and names that do not start with an
// alphanumeric -- which rules out "." , "..", hidden files and option-like
// names in one go.
//
// Interior dots, underscores, plus signs and hyphens are legitimate: real
// toolchains ship python3.13, pip3.13, pydoc3.13, 2to3-3.13 and
// x86_64-linux-gnu-g++. The previous [A-Za-z0-9-] rule rejected all of them,
// so those commands silently never got a shim (and, once the shims were
// refreshed, kept pointing at a superseded svc-shim inode).
//
// Pure for testability.
func safeShimName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '+', c == '-':
			// A leading dot or hyphen would make the shim a hidden file or an
			// option-like name; both are refused (this also covers "." and "..").
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// createShimFor creates a shim for a command as a hardlink to the base shim
// binary (falling back to a copy). On Unix, if the base shim is itself a
// symlink (rare), resolve it first so the hardlink points at the real file,
// not a possibly-stale symlink node. This matters when SDKs ship commands as
// symlinks (pip -> pip3). The shim is always named <cmd><shimExtFor(ext)>:
// .cmd/.bat targets get a .exe shim too (see shimExtFor).
func (m *Manager) createShimFor(cmdName, ext string) error {
	// M8: Validate cmdName before any path construction to prevent path
	// traversal via tampered shims.json.
	if !safeShimName(cmdName) {
		return fmt.Errorf("refusing to create shim for unsafe command name: %s", cmdName)
	}
	ext = shimExtFor(ext)
	// Purge ALL variants of cmdName first. The previous code only removed
	// the target variant (os.Remove(cmdName+ext)); switching extensions
	// (e.g. a legacy .cmd wrapper existed and now we create .exe, or vice
	// versa) left the old variant on disk as a stale, conflicting shim.
	// removeShim already does multi-variant removal on Windows
	// (bare+.exe+.cmd+.bat) and bare-only on Unix, so reuse it.
	m.removeShim(cmdName)

	shimPath := m.getShimBinaryPath()
	linkPath := filepath.Join(m.cfg.ShimsDir(), cmdName+ext)
	// Resolve symlinks on Unix so the hardlink targets the real file.
	// On Windows os.Stat already follows; SymlinkTarget is not needed.
	target := shimPath
	if runtime.GOOS != "windows" {
		if resolved, err := filepath.EvalSymlinks(shimPath); err == nil {
			target = resolved
		}
	}
	if err := os.Link(target, linkPath); err == nil {
		return nil
	}
	return copyFile(target, linkPath, 0755)
}

// removeShim removes a shim and any platform-specific variants for the command.
func (m *Manager) removeShim(name string) {
	dir := m.cfg.ShimsDir()
	os.Remove(filepath.Join(dir, name))
	if runtime.GOOS == "windows" {
		os.Remove(filepath.Join(dir, name+".exe"))
		os.Remove(filepath.Join(dir, name+".cmd"))
		os.Remove(filepath.Join(dir, name+".bat"))
	}
}

// getShimBinaryPath returns the path to the base shim binary.
func (m *Manager) getShimBinaryPath() string {
	name := "svc-shim"
	if runtime.GOOS == "windows" {
		name = "svc-shim.exe"
	}
	return filepath.Join(m.cfg.ShimsDir(), name)
}

// ShimBinaryInstalled reports whether the base shim binary that every command
// shim links to is in place. EnsureSetup installs it; callers that re-run
// ConfigureSdk outside the normal install/switch flow check this first, because
// without it createShimFor fails for every command and the config update would
// record an empty command set — dropping the SDK's shims instead of refreshing
// them.
func (m *Manager) ShimBinaryInstalled() bool {
	_, err := os.Stat(m.getShimBinaryPath())
	return err == nil
}

// updateShimConfig updates shims.json with the SDK type config and its commands.
// The whole read-modify-write cycle runs under the cross-process shims.json
// lock (item 3): the GUI can run in two instances and the CLI can run in
// parallel; without the lock a concurrent writer's changes could be lost
// when this function saves its stale in-memory copy.
func (m *Manager) updateShimConfig(sdkType string, binDirs []string, envVars map[string]string, commands []string) error {
	return withShimsConfigLock(m.cfg.SvcDir(), func() error {
		return m.updateShimConfigLocked(sdkType, binDirs, envVars, commands)
	})
}

func (m *Manager) updateShimConfigLocked(sdkType string, binDirs []string, envVars map[string]string, commands []string) error {
	cfgPath := m.cfg.ShimsConfigPath()
	// M7: loadShimConfig can fail on a corrupt shims.json. Check the error
	// before writing back — silently using an empty config would wipe valid
	// command mappings on save.
	cfg, err := m.loadShimConfig()
	if err != nil {
		return fmt.Errorf("failed to load shim config: %w", err)
	}

	if cfg.Commands == nil {
		cfg.Commands = make(map[string]string)
	}
	if cfg.SdkTypes == nil {
		cfg.SdkTypes = make(map[string]shim.SdkShimEntry)
	}

	// Remove old commands for this SDK type (in case executables changed)
	for cmd, st := range cfg.Commands {
		if st == sdkType {
			delete(cfg.Commands, cmd)
		}
	}

	// Add new commands
	for _, cmd := range commands {
		// Strip .exe extension for the command name (the shim handles this)
		cmdName := cmd
		if runtime.GOOS == "windows" {
			cmdName = strings.TrimSuffix(cmdName, ".exe")
		}
		cfg.Commands[cmdName] = sdkType
	}

	// Update SDK type config
	cfg.SdkTypes[sdkType] = shim.SdkShimEntry{
		BinDirs: binDirs,
		EnvVars: envVars,
	}

	return m.saveShimConfig(cfgPath, cfg)
}

// removeSdkFromConfig removes an SDK type and its commands from shims.json.
// Runs under the cross-process shims.json lock (item 3) for the same
// lost-update reason as updateShimConfig.
func (m *Manager) removeSdkFromConfig(sdkType string) error {
	return withShimsConfigLock(m.cfg.SvcDir(), func() error {
		return m.removeSdkFromConfigLocked(sdkType)
	})
}

func (m *Manager) removeSdkFromConfigLocked(sdkType string) error {
	cfgPath := m.cfg.ShimsConfigPath()
	// M7: loadShimConfig can fail on a corrupt shims.json. Check the error
	// before writing back — silently using an empty config would wipe valid
	// command mappings for other SDKs on save.
	cfg, err := m.loadShimConfig()
	if err != nil {
		return fmt.Errorf("failed to load shim config: %w", err)
	}

	for cmd, st := range cfg.Commands {
		if st == sdkType {
			delete(cfg.Commands, cmd)
		}
	}
	delete(cfg.SdkTypes, sdkType)

	return m.saveShimConfig(cfgPath, cfg)
}

// getCommandsForSdkType returns all command names registered for a given SDK type.
func (m *Manager) getCommandsForSdkType(sdkType string) ([]string, error) {
	cfg, _ := m.loadShimConfig()
	var commands []string
	for cmd, st := range cfg.Commands {
		if st == sdkType {
			commands = append(commands, cmd)
		}
	}
	return commands, nil
}

// loadShimConfig reads shims.json. On file-not-found it returns an empty config
// with a nil error (first install, no config yet). On JSON unmarshal failure
// it returns the error so the caller can abort WITHOUT overwriting the
// (corrupt) file with an empty config — silently returning an empty config
// previously wiped valid config on save (M7).
func (m *Manager) loadShimConfig() (shim.ShimConfig, error) {
	cfgPath := m.cfg.ShimsConfigPath()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return shim.ShimConfig{}, nil
		}
		return shim.ShimConfig{}, err
	}
	var cfg shim.ShimConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return shim.ShimConfig{}, fmt.Errorf("failed to parse %s: %w", cfgPath, err)
	}
	return cfg, nil
}

// saveShimConfig writes shims.json atomically (temp file + rename).
// On failure, the existing shims.json is preserved — no silent empty fallback.
func (m *Manager) saveShimConfig(path string, cfg shim.ShimConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// copyFile copies a file from src to dst with the given permissions.
func copyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, perm)
}

// filesEqual reports whether the bytes at path equal expected. Used by
// ensureShimBinary to catch same-size-different-content binaries that the
// size+modtime heuristic would miss (a rebuild with coincidentally identical
// file size but different bytes). A read error is treated as not-equal so the
// caller falls through to the safe "rebuild" path.
func filesEqual(path string, expected []byte) bool {
	got, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Equal(got, expected)
}
