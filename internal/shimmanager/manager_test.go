package shimmanager

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"svc/internal/config"
	"svc/internal/shim"
)

// newTestManager returns a Manager backed by a temp SVC dir (shims dir +
// shims.json under t.TempDir()) with a fake svc-shim binary in place so
// createShimFor can hardlink to it.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.SetHomeDir(dir) // RcFilePath derives from homeDir, not svcDir.
	cfg.SetSvcDir(dir)  // ShimsDir / ShimsConfigPath derive from svcDir.
	m := New(cfg)
	shimsDir := cfg.ShimsDir()
	if err := os.MkdirAll(shimsDir, 0755); err != nil {
		t.Fatal(err)
	}
	shimName := "svc-shim"
	if runtime.GOOS == "windows" {
		shimName = "svc-shim.exe"
	}
	if err := os.WriteFile(filepath.Join(shimsDir, shimName), []byte("fake-svc-shim"), 0755); err != nil {
		t.Fatal(err)
	}
	return m
}

func shimExt() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// --- ensurePython3Alias (pure logic) ---

func TestEnsurePython3Alias_addsWhenMissing(t *testing.T) {
	m := &Manager{}
	cfg := shim.ShimConfig{
		SdkTypes: map[string]shim.SdkShimEntry{"python": {}},
		Commands: map[string]string{"python": "python"},
	}
	if !m.ensurePython3Alias(&cfg) {
		t.Fatal("ensurePython3Alias = false; want true (should add python3)")
	}
	if got := cfg.Commands["python3"]; got != "python" {
		t.Fatalf(`Commands["python3"] = %q; want "python"`, got)
	}
}

func TestEnsurePython3Alias_noOpWhenPresent(t *testing.T) {
	m := &Manager{}
	cfg := shim.ShimConfig{
		SdkTypes: map[string]shim.SdkShimEntry{"python": {}},
		Commands: map[string]string{"python": "python", "python3": "python"},
	}
	if m.ensurePython3Alias(&cfg) {
		t.Fatal("ensurePython3Alias = true; want false (python3 already present)")
	}
}

func TestEnsurePython3Alias_noOpWhenNoPython(t *testing.T) {
	m := &Manager{}
	cfg := shim.ShimConfig{
		SdkTypes: map[string]shim.SdkShimEntry{"go": {}},
		Commands: map[string]string{"go": "go"},
	}
	if m.ensurePython3Alias(&cfg) {
		t.Fatal("ensurePython3Alias = true; want false (Python not in SdkTypes)")
	}
}

// --- ensurePython3Shim (filesystem: registers alias + creates shim) ---

func TestEnsurePython3Shim_addsAliasAndCreatesShim(t *testing.T) {
	m := newTestManager(t)
	seed := shim.ShimConfig{
		SdkTypes: map[string]shim.SdkShimEntry{"python": {BinDirs: []string{"python"}}},
		Commands: map[string]string{"python": "python"},
	}
	if err := m.saveShimConfig(m.cfg.ShimsConfigPath(), seed); err != nil {
		t.Fatal(err)
	}
	m.ensurePython3Shim()
	loaded, _ := m.loadShimConfig()
	if got := loaded.Commands["python3"]; got != "python" {
		t.Fatalf(`shims.json Commands["python3"] = %q; want "python"`, got)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.ShimsDir(), "python3"+shimExt())); err != nil {
		t.Fatalf("python3 shim file not created: %v", err)
	}
}

// --- createShimsForDirs (python3 alias on install/switch) ---

func TestCreateShimsForDirs_python3Alias(t *testing.T) {
	m := newTestManager(t)
	versionDir := t.TempDir()
	binName := "python" + shimExt()
	if err := os.WriteFile(filepath.Join(versionDir, binName), []byte("fake-python"), 0755); err != nil {
		t.Fatal(err)
	}
	created, err := m.createShimsForDirs(versionDir, []string{""}, "python")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range created {
		if c == "python3" {
			found = true
		}
	}
	if !found {
		t.Errorf("createShimsForDirs result = %v; want to contain python3", created)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.ShimsDir(), "python3"+shimExt())); err != nil {
		t.Errorf("python3 shim not created: %v", err)
	}
}

// --- RemoveSdk (cleans python3) ---

func TestRemoveSdk_cleansPython3(t *testing.T) {
	m := newTestManager(t)
	shimsDir := m.cfg.ShimsDir()
	seed := shim.ShimConfig{
		SdkTypes: map[string]shim.SdkShimEntry{"python": {BinDirs: []string{"python"}}},
		Commands: map[string]string{"python": "python", "python3": "python"},
	}
	if err := m.saveShimConfig(m.cfg.ShimsConfigPath(), seed); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"python", "python3"} {
		if err := os.WriteFile(filepath.Join(shimsDir, name+shimExt()), []byte("x"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.RemoveSdk("python", nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"python", "python3"} {
		if _, err := os.Stat(filepath.Join(shimsDir, name+shimExt())); !os.IsNotExist(err) {
			t.Errorf("%s%s still exists after RemoveSdk; want removed", name, shimExt())
		}
	}
	loaded, _ := m.loadShimConfig()
	if _, ok := loaded.Commands["python"]; ok {
		t.Error(`shims.json Commands["python"] still present after RemoveSdk`)
	}
	if _, ok := loaded.Commands["python3"]; ok {
		t.Error(`shims.json Commands["python3"] still present after RemoveSdk`)
	}
	if _, ok := loaded.SdkTypes["python"]; ok {
		t.Error(`shims.json SdkTypes["python"] still present after RemoveSdk`)
	}
}

// --- rebuildCommandShims (rebuilds existing command shims incl. python3) ---

func TestRebuildCommandShims_rebuildsPython3(t *testing.T) {
	m := newTestManager(t)
	shimsDir := m.cfg.ShimsDir()
	seed := shim.ShimConfig{
		SdkTypes: map[string]shim.SdkShimEntry{"python": {BinDirs: []string{"python"}}},
		Commands: map[string]string{"python": "python", "python3": "python"},
	}
	if err := m.saveShimConfig(m.cfg.ShimsConfigPath(), seed); err != nil {
		t.Fatal(err)
	}
	m.rebuildCommandShims()
	for _, name := range []string{"python", "python3"} {
		if _, err := os.Stat(filepath.Join(shimsDir, name+shimExt())); err != nil {
			t.Errorf("%s shim not rebuilt by rebuildCommandShims: %v", name, err)
		}
	}
}

// --- createShimFor (BUG F: removes other variants) ---

// TestCreateShimFor_removesOtherVariants pins BUG F: creating a .exe shim
// must also clean up any stale .cmd/.bat wrappers (and the bare file) so the
// old variant doesn't linger and shadow the new one. The previous code only
// removed the target variant (os.Remove(cmdName+ext)), so switching
// extensions left a stale sibling on disk.
func TestCreateShimFor_removesOtherVariants(t *testing.T) {
	m := newTestManager(t)
	shimsDir := m.cfg.ShimsDir()
	name := "node"
	ext := shimExt() // ".exe" on Windows, "" on Unix

	// Pre-create every variant that removeShim should purge as stale.
	// On Windows: bare + .exe + .cmd + .bat. On Unix: bare only (removeShim
	// is bare-only on Unix; .exe/.cmd/.bat are Windows-only cleanup).
	staleVariants := []string{name}
	if runtime.GOOS == "windows" {
		staleVariants = append(staleVariants, name+".exe", name+".cmd", name+".bat")
	}
	for _, v := range staleVariants {
		if err := os.WriteFile(filepath.Join(shimsDir, v), []byte("stale-"+v), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.createShimFor(name, ext); err != nil {
		t.Fatal(err)
	}

	// The new shim must exist + contain the svc-shim bytes (hardlink or copy
	// of svc-shim, which newTestManager wrote as "fake-svc-shim").
	target := filepath.Join(shimsDir, name+ext)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("new shim %s not created: %v", target, err)
	}
	if string(got) != "fake-svc-shim" {
		t.Errorf("new shim content = %q; want %q (svc-shim bytes)", string(got), "fake-svc-shim")
	}

	// All OTHER pre-created variants must be gone.
	for _, v := range staleVariants {
		if v == name+ext {
			continue
		}
		if _, err := os.Stat(filepath.Join(shimsDir, v)); !os.IsNotExist(err) {
			t.Errorf("stale variant %s still exists after createShimFor; want removed", v)
		}
	}
}

// --- safeShimName (commands with dots, like python3.13) ---

// TestSafeShimName pins the shim-name guard. Real toolchains ship commands
// with dots, underscores and plus signs (python3.13, pip3.13, 2to3-3.13,
// x86_64-linux-gnu-g++); the old [A-Za-z0-9-] rule refused every one of them,
// so those commands never got a shim and a refresh left the pre-existing ones
// pointing at a superseded svc-shim inode. Names that could escape the shims
// directory or reach a shell must still be refused.
func TestSafeShimName(t *testing.T) {
	allowed := []string{
		"node", "python3", "python3.13", "python3.13-config", "pip3.13",
		"pydoc3.13", "idle3.13", "2to3-3.13", "x86_64-linux-gnu-g++",
		"i686_w64_mingw32-gcc", "cargo-clippy", "rust-analyzer", "a_b", "v1.2",
	}
	for _, name := range allowed {
		if !safeShimName(name) {
			t.Errorf("safeShimName(%q) = false; want true: a real toolchain command must get a shim", name)
		}
	}

	refused := []string{
		"", ".", "..", "...", "../svc", `..\svc`, "a/b", `a\b`, "/etc/passwd",
		"svc;rm -rf /", "a b", "a$b", "a`b", "a*b", "a?b", "a|b", "a&b", "a>b",
		`"a"`, "'a'", "-rf", ".hidden", "~", "#a", "a\tb", "a\nb", "node\x00",
	}
	for _, name := range refused {
		if safeShimName(name) {
			t.Errorf("safeShimName(%q) = true; want false: must not escape the shims dir or reach a shell", name)
		}
	}
}

// TestCreateShimFor_dottedCommand is the end-to-end half of the same
// regression: the versioned Python commands must actually land in the shims
// dir, hardlinked to the base shim like any other command.
func TestCreateShimFor_dottedCommand(t *testing.T) {
	m := newTestManager(t)
	shimsDir := m.cfg.ShimsDir()
	ext := shimExt()

	for _, name := range []string{"python3.13", "pip3.13", "python3.13-config"} {
		if err := m.createShimFor(name, ext); err != nil {
			t.Fatalf("createShimFor(%q): %v", name, err)
		}
		got, err := os.ReadFile(filepath.Join(shimsDir, name+ext))
		if err != nil {
			t.Fatalf("shim %q was not created: %v", name, err)
		}
		if string(got) != "fake-svc-shim" {
			t.Errorf("shim %q content = %q; want the svc-shim bytes", name, string(got))
		}
	}
}

// TestCreateShimFor_refusesTraversal keeps the M8 guarantee the relaxation
// must not weaken: a name that would climb out of the shims dir is refused
// and nothing is created outside it.
func TestCreateShimFor_refusesTraversal(t *testing.T) {
	m := newTestManager(t)

	if err := m.createShimFor("../evil", shimExt()); err == nil {
		t.Fatal("createShimFor accepted a traversal name; want an error")
	}
	outside := filepath.Join(m.cfg.ShimsDir(), "..", "evil")
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Errorf("%s exists; a refused shim name must not create anything", outside)
	}
}

// --- filesEqual (BUG G: content comparison helper) ---

// TestFilesEqual pins BUG G's helper: same bytes -> true; different bytes or
// different size -> false; missing file -> false. ensureShimBinary uses this
// to catch same-size-different-content binaries that the size+modtime
// heuristic would miss.
func TestFilesEqual(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shim.bin")
	content := []byte("hello shim bytes")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	if !filesEqual(path, content) {
		t.Error("filesEqual(path, content) = false; want true (identical bytes)")
	}
	if filesEqual(path, []byte("different content")) {
		t.Error("filesEqual(path, different) = true; want false (different content)")
	}
	// Different size: a prefix of the content is NOT equal (size check is
	// implicit in bytes.Equal, which returns false when lengths differ).
	if filesEqual(path, content[:len(content)-1]) {
		t.Error("filesEqual(path, content[:n-1]) = true; want false (different size)")
	}
	// Missing file -> false (read error is treated as not-equal).
	if filesEqual(filepath.Join(dir, "nope.bin"), content) {
		t.Error("filesEqual(missing, content) = true; want false (read error)")
	}
}

// --- mergeEnvVarKeys (item 5: union of caller + shims.json env keys) ---

func TestMergeEnvVarKeys(t *testing.T) {
	cases := []struct {
		name string
		a, b map[string]string
		want []string
	}{
		{"both nil", nil, nil, []string{}},
		{"a only", map[string]string{"JAVA_HOME": ""}, nil, []string{"JAVA_HOME"}},
		{"b only", nil, map[string]string{"GOROOT": ""}, []string{"GOROOT"}},
		{
			"union sorted",
			map[string]string{"JAVA_HOME": "", "PATH_X": ""},
			map[string]string{"GOROOT": "", "JAVA_HOME": "jre"},
			[]string{"GOROOT", "JAVA_HOME", "PATH_X"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeEnvVarKeys(tc.a, tc.b)
			if len(got) != len(tc.want) {
				t.Fatalf("mergeEnvVarKeys = %v; want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("mergeEnvVarKeys = %v; want %v", got, tc.want)
				}
			}
		})
	}
}

// TestRemoveSdk_removesStoredEnvVars pins item 5 end-to-end: even when the
// caller passes NO extra env vars, the keys recorded in shims.json for the
// SDK type must be collected for OS-level removal. On Unix
// removeEnvVarsFromSystem is a no-op, so this test verifies the observable
// behaviour available everywhere: RemoveSdk succeeds and drops the config
// entry; the key-union logic itself is pinned by TestMergeEnvVarKeys.
func TestRemoveSdk_removesStoredEnvVars(t *testing.T) {
	m := newTestManager(t)
	seed := shim.ShimConfig{
		SdkTypes: map[string]shim.SdkShimEntry{
			"jdk": {BinDirs: []string{"bin"}, EnvVars: map[string]string{"JAVA_HOME": ""}},
		},
		Commands: map[string]string{"java": "jdk"},
	}
	if err := m.saveShimConfig(m.cfg.ShimsConfigPath(), seed); err != nil {
		t.Fatal(err)
	}
	// Caller passes nil extraEnvVars — the stored JAVA_HOME must still be
	// part of the removal key set (union), and the call must succeed.
	if err := m.RemoveSdk("jdk", nil); err != nil {
		t.Fatalf("RemoveSdk with nil extraEnvVars failed: %v", err)
	}
	loaded, _ := m.loadShimConfig()
	if _, ok := loaded.SdkTypes["jdk"]; ok {
		t.Error(`SdkTypes["jdk"] still present after RemoveSdk`)
	}
}

// --- .cmd/.bat shim strategy: .exe hardlink instead of batch wrapper ---

// TestShimExtFor pins the extension mapping: .cmd/.bat targets get a .exe
// shim. A batch wrapper's %* expansion re-parses arguments (a `^&` that
// survived the caller's parse gets split on the wrapper's second parse);
// the .exe shim receives argv intact and the final .cmd invocation is
// escaped by internal/shim's cmd-safe logic.
func TestShimExtFor(t *testing.T) {
	tests := []struct{ in, want string }{
		{".cmd", ".exe"},
		{".bat", ".exe"},
		{".exe", ".exe"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := shimExtFor(tt.in); got != tt.want {
			t.Errorf("shimExtFor(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

// TestCreateShimFor_cmdTargetGetsExeShim verifies that a .cmd-target command
// is shimmed as <cmd>.exe (hardlink to svc-shim) and that a legacy .cmd
// wrapper from an older version is purged, never regenerated.
func TestCreateShimFor_cmdTargetGetsExeShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip(".cmd targets are Windows-only")
	}
	m := newTestManager(t)
	// Legacy wrapper left by an older app version.
	legacy := filepath.Join(m.cfg.ShimsDir(), "npm.cmd")
	if err := os.WriteFile(legacy, []byte("@echo off\r\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := m.createShimFor("npm", ".cmd"); err != nil {
		t.Fatal(err)
	}

	exeShim := filepath.Join(m.cfg.ShimsDir(), "npm.exe")
	if _, err := os.Stat(exeShim); err != nil {
		t.Errorf("expected npm.exe shim: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("legacy npm.cmd wrapper was not purged")
	}
	if _, err := os.Stat(filepath.Join(m.cfg.ShimsDir(), "npm.cmd")); !os.IsNotExist(err) {
		t.Error("no .cmd wrapper must be generated")
	}
}

func TestReplaceShimBinary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "svc-shim")

	writeBytes := func(content string) func(dst string) error {
		return func(dst string) error { return os.WriteFile(dst, []byte(content), 0755) }
	}

	// Fresh install.
	if err := replaceShimBinary(target, writeBytes("v1")); err != nil {
		t.Fatalf("fresh install: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "v1" {
		t.Fatalf("content = %q, want v1", b)
	}
	if _, err := os.Stat(target + ".new"); !os.IsNotExist(err) {
		t.Error(".new temp file left behind")
	}

	// Replace existing content.
	if err := replaceShimBinary(target, writeBytes("v2")); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "v2" {
		t.Fatalf("content = %q, want v2", b)
	}

	// Stale .new from an interrupted run is cleaned up.
	os.WriteFile(target+".new", []byte("stale"), 0755)
	if err := replaceShimBinary(target, writeBytes("v3")); err != nil {
		t.Fatalf("replace with stale tmp: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "v3" {
		t.Fatalf("content = %q, want v3", b)
	}

	// writeNew failure: error returned, temp removed, target untouched.
	failErr := fmt.Errorf("write exploded")
	err := replaceShimBinary(target, func(dst string) error { return failErr })
	if err == nil {
		t.Fatal("expected writeNew error to propagate")
	}
	if _, statErr := os.Stat(target + ".new"); !os.IsNotExist(statErr) {
		t.Error(".new temp file left behind after write failure")
	}
	if b, _ := os.ReadFile(target); string(b) != "v3" {
		t.Fatalf("target modified despite write failure: %q", b)
	}
}

// --- ShimBinaryInstalled ---

// TestShimBinaryInstalled guards the startup shim refresh: re-running
// ConfigureSdk without the base shim binary creates no command shims, and the
// config update would then record an empty command set — dropping the SDK from
// PATH instead of refreshing it. Callers need a reliable way to tell the two
// states apart.
func TestShimBinaryInstalled(t *testing.T) {
	if !newTestManager(t).ShimBinaryInstalled() {
		t.Error("ShimBinaryInstalled = false; newTestManager places the base shim binary")
	}

	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.SetHomeDir(dir)
	cfg.SetSvcDir(dir)
	m := New(cfg)
	if m.ShimBinaryInstalled() {
		t.Error("ShimBinaryInstalled = true; want false when there is no shims dir at all")
	}

	// An existing but empty shims dir (EnsureSetup failed halfway) is still
	// "missing": there is nothing for the command shims to link to.
	if err := os.MkdirAll(cfg.ShimsDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if m.ShimBinaryInstalled() {
		t.Error("ShimBinaryInstalled = true for an empty shims dir")
	}

	name := "svc-shim"
	if runtime.GOOS == "windows" {
		name = "svc-shim.exe"
	}
	if err := os.WriteFile(filepath.Join(cfg.ShimsDir(), name), []byte("fake"), 0755); err != nil {
		t.Fatal(err)
	}
	if !m.ShimBinaryInstalled() {
		t.Error("ShimBinaryInstalled = false; want true once the base shim binary exists")
	}
}
