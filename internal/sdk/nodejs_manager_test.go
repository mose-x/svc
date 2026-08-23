package sdk

import (
	"os"
	"path/filepath"
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
