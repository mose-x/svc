package sdk

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDetectNodeExternalManager(t *testing.T) {
	tests := []struct {
		name     string
		nodePath string
		want     string
	}{
		// nvm-rust (nvm-rs) layouts, all platforms.
		{
			name:     "nvm-rust shim",
			nodePath: "/Users/mose/.nvm.rust/shims/node",
			want:     "nvm-rust",
		},
		{
			name:     "nvm-rust active bin",
			nodePath: "/home/user/.nvm.rust/active/bin/node",
			want:     "nvm-rust",
		},
		{
			name:     "nvm-rust versioned dir",
			nodePath: "/Users/mose/.nvm.rust/v24.19.0/bin/node",
			want:     "nvm-rust",
		},
		// Classic nvm layouts.
		{
			name:     "nvm versions dir",
			nodePath: "/Users/mose/.nvm/versions/node/v20.11.1/bin/node",
			want:     "nvm",
		},
		{
			name:     "nvm-windows appdata",
			nodePath: `C:\Users\mose\AppData\Roaming\nvm\v20.11.1\node.exe`,
			want:     "nvm",
		},
		// Standalone copies must not match.
		{
			name:     "svc managed install",
			nodePath: "/Users/mose/.svc/nodejs/22.11.0/bin/node",
			want:     "",
		},
		{
			name:     "usr local bin",
			nodePath: "/usr/local/bin/node",
			want:     "",
		},
		{
			name:     "homebrew cellar",
			nodePath: "/opt/homebrew/Cellar/node/22.11.0/bin/node",
			want:     "",
		},
		{
			name:     "nvm substring inside unrelated name is not a segment",
			nodePath: "/Users/mose/projects/.nvm.rust-backup-clone/node",
			want:     "",
		},
		{
			name:     "empty",
			nodePath: "",
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectNodeExternalManager(tt.nodePath); got != tt.want {
				t.Errorf("DetectNodeExternalManager(%q) = %q; want %q", tt.nodePath, got, tt.want)
			}
		})
	}
}

func TestResolveNodeExternalManagerFollowsSymlinks(t *testing.T) {
	// Simulate a PATH entry outside the manager home that symlinks into it
	// (e.g. /usr/local/bin/node -> ~/.nvm.rust/active/bin/node).
	home := t.TempDir()
	managerDir := filepath.Join(home, ".nvm.rust", "v24.19.0", "bin")
	if err := os.MkdirAll(managerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realNode := filepath.Join(managerDir, "node")
	if err := os.WriteFile(realNode, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkDir, "node")
	if err := os.Symlink(realNode, link); err != nil {
		t.Skipf("symlink unsupported on this host: %v", err)
	}
	if got := resolveNodeExternalManager(link); got != "nvm-rust" {
		t.Errorf("resolveNodeExternalManager(%q) = %q; want %q", link, got, "nvm-rust")
	}
}

func TestResolveNodeExternalManagerStandalone(t *testing.T) {
	home := t.TempDir()
	node := filepath.Join(home, "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveNodeExternalManager(node); got != "" {
		t.Errorf("resolveNodeExternalManager(%q) = %q; want empty", node, got)
	}
	if got := resolveNodeExternalManager(""); got != "" {
		t.Errorf("resolveNodeExternalManager(\"\") = %q; want empty", got)
	}
}

// --- DetectNodeManager ------------------------------------------------------

// TestDetectNodeManager covers the machine-level answer, which is the one the
// refusal is built on. The directories are parameters rather than the host's own
// so every platform's layout is exercised here, on whatever machine runs it --
// and so the test does not start passing or failing because the CI runner
// happens to have nvm installed.
func TestDetectNodeManager(t *testing.T) {
	const (
		home   = "/Users/mose"
		config = "/Users/mose/Library/Application Support"
		winCfg = `C:\Users\mose\AppData\Roaming`
	)
	tests := []struct {
		name       string
		goos       string
		homeDir    string
		configDir  string
		pathBinary string
		// present lists the manager directories that exist, as path segments
		// relative to homeDir or configDir. Joining them with filepath.Join here
		// mirrors what DetectNodeManager does, so the expectation holds on a
		// Windows host as well.
		present []string
		want    string
	}{
		{
			name:    "an nvm-rust home alone is enough",
			goos:    "darwin",
			homeDir: home,
			present: []string{filepath.Join(home, ".nvm.rust")},
			want:    "nvm-rust",
		},
		{
			name:    "a classic nvm home alone is enough",
			goos:    "linux",
			homeDir: home,
			present: []string{filepath.Join(home, ".nvm")},
			want:    "nvm",
		},
		{
			name:    "nvm-rust wins when both homes are there",
			goos:    "darwin",
			homeDir: home,
			present: []string{filepath.Join(home, ".nvm.rust"), filepath.Join(home, ".nvm")},
			want:    "nvm-rust",
		},
		{
			name:      "the nvm-windows config dir counts on windows",
			goos:      "windows",
			homeDir:   `C:\Users\mose`,
			configDir: winCfg,
			present:   []string{filepath.Join(winCfg, "nvm")},
			want:      "nvm",
		},
		{
			name:      "the nvm-windows config dir is ignored on another platform",
			goos:      "darwin",
			homeDir:   home,
			configDir: config,
			present:   []string{filepath.Join(config, "nvm")},
			want:      "",
		},
		{
			name:      "an nvm-rust home counts on windows too",
			goos:      "windows",
			homeDir:   `C:\Users\mose`,
			configDir: winCfg,
			present:   []string{filepath.Join(`C:\Users\mose`, ".nvm.rust")},
			want:      "nvm-rust",
		},
		{
			name:      "a machine with no manager is free",
			goos:      "darwin",
			homeDir:   home,
			configDir: config,
			want:      "",
		},
		{
			name:       "the node PATH resolves to answers first",
			goos:       "darwin",
			homeDir:    home,
			pathBinary: "/Users/mose/.nvm/versions/node/v20.11.1/bin/node",
			want:       "nvm",
		},
		{
			name:       "the node on PATH beats a different manager's home",
			goos:       "darwin",
			homeDir:    home,
			pathBinary: "/Users/mose/.nvm/versions/node/v20.11.1/bin/node",
			present:    []string{filepath.Join(home, ".nvm.rust")},
			want:       "nvm",
		},
		// The stability case: once svc's own shim wins in PATH the node binary
		// says nothing about nvm any more, but the home directory is still there.
		// A refusal that only looked at PATH would fire once and then silently
		// allow every install after it.
		{
			name:       "svc's own shim does not hide the manager home",
			goos:       "darwin",
			homeDir:    home,
			pathBinary: "/Users/mose/.svc/shims/node",
			present:    []string{filepath.Join(home, ".nvm.rust")},
			want:       "nvm-rust",
		},
		{
			name:       "a standalone node is not a manager",
			goos:       "linux",
			homeDir:    home,
			pathBinary: "/usr/local/bin/node",
			want:       "",
		},
		{
			name:    "an empty home is not an error",
			goos:    "darwin",
			present: []string{filepath.Join(home, ".nvm.rust")},
			want:    "",
		},
		{
			name:    "an empty config dir is not an error on windows",
			goos:    "windows",
			homeDir: `C:\Users\mose`,
			present: []string{filepath.Join(winCfg, "nvm")},
			want:    "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			present := make(map[string]bool, len(tc.present))
			for _, dir := range tc.present {
				present[dir] = true
			}
			dirExists := func(path string) bool { return present[path] }
			if got := DetectNodeManager(tc.goos, tc.homeDir, tc.configDir, tc.pathBinary, dirExists); got != tc.want {
				t.Errorf("DetectNodeManager(%q, %q, %q, %q) = %q, want %q",
					tc.goos, tc.homeDir, tc.configDir, tc.pathBinary, got, tc.want)
			}
		})
	}
}

// TestDirExistsIsNotFooledByAFile pins the one way the real probe can lie: a
// regular file called ~/.nvm.rust (a stale download, a lockfile, a mistake) is
// not an installed manager, and refusing every Node install because of it would
// be a refusal the user cannot explain or undo.
func TestDirExistsIsNotFooledByAFile(t *testing.T) {
	home := t.TempDir()
	asFile := filepath.Join(home, ".nvm.rust")
	if err := os.WriteFile(asFile, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirExists(asFile) {
		t.Errorf("dirExists(%q) = true for a regular file, want false", asFile)
	}
	if dirExists(filepath.Join(home, ".nvm")) {
		t.Error("dirExists reported a directory that was never created")
	}
	asDir := filepath.Join(home, ".nvm")
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !dirExists(asDir) {
		t.Errorf("dirExists(%q) = false for a real directory, want true", asDir)
	}
	if got := DetectNodeManager(runtime.GOOS, home, home, "", dirExists); got != "nvm" {
		t.Errorf("DetectNodeManager = %q, want %q: the file must not count, the directory must", got, "nvm")
	}
}

// TestNodeManagerOnMachineReportsAKnownAnswer is a smoke test for the real
// probe. The answer depends on the host running it, so all it can assert is that
// the probe reads the machine without failing and never invents a manager name:
// anything else is a typo waiting to reach a user-visible sentence.
func TestNodeManagerOnMachineReportsAKnownAnswer(t *testing.T) {
	switch got := NodeManagerOnMachine(); got {
	case "", "nvm", "nvm-rust":
	default:
		t.Errorf("NodeManagerOnMachine() = %q, want one of \"\", nvm, nvm-rust", got)
	}
}

// TestExternalManagerLabel covers the table a service refusal is rendered with:
// both machine names the detectors can produce, and the pass-through that keeps
// a manager added later readable instead of blank.
func TestExternalManagerLabel(t *testing.T) {
	cases := []struct{ manager, want string }{
		{"nvm-rust", "nvm(nvm-rust)"},
		{"nvm", "nvm"},
		{"fnm", "fnm"},
		{"", ""},
	}
	for _, c := range cases {
		if got := ExternalManagerLabel(c.manager); got != c.want {
			t.Errorf("ExternalManagerLabel(%q) = %q, want %q", c.manager, got, c.want)
		}
	}
	// Every machine name a detector can return must have a label, or a refusal
	// about it would show the user an implementation string.
	labels := ExternalManagerLabels()
	for _, manager := range []string{"nvm-rust", "nvm"} {
		if _, ok := labels[manager]; !ok {
			t.Errorf("the label table has no entry for %q, which DetectNodeManager returns", manager)
		}
	}
	// A copy, so a caller cannot edit the table the next refusal is rendered from.
	labels["nvm"] = "edited"
	if got := ExternalManagerLabel("nvm"); got != "nvm" {
		t.Errorf("ExternalManagerLabels handed out the table itself: nvm now renders as %q", got)
	}
}
