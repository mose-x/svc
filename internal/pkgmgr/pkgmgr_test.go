package pkgmgr

import "testing"

func TestParsePipVersion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"standard", "pip 24.0 from /opt/python/lib/python3.12/site-packages/pip (python 3.12)\n", "24.0"},
		{"patch version", "pip 23.2.1 from /usr/lib/python3.11/site-packages/pip (python 3.11)", "23.2.1"},
		{"trailing newline only", "pip 22.0.2\n", "22.0.2"},
		{"no leading pip", "Package Installer Program v9", "Package Installer Program v9"},
		{"empty output", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePipVersion(tt.input)
			if got != tt.want {
				t.Errorf("parsePipVersion(%q) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTruncatedTail(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short unchanged", "abc", 10, "abc"},
		{"trims whitespace", "  abc \n", 10, "abc"},
		{"exact length unchanged", "abcde", 5, "abcde"},
		{"truncated with prefix", "abcdefgh", 3, "...fgh"},
		{"empty", "", 10, ""},
		{"whitespace only", "   ", 10, ""},
		{"multibyte cut on rune boundary", "aaaa中", 3, "...中"},
		{"multibyte cut inside rune snaps to end", "aaa中", 2, "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncatedTail(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncatedTail(%q, %d) = %q; want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestParseCnpmVersion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"verbose report",
			"cnpm@9.4.0 (C:\\node_modules\\cnpm\\lib\\parse_argv.js)\r\nnpm@9.9.2 (C:\\npm\\index.js)\r\nnode@23.0.0 (C:\\node.exe)",
			"9.4.0",
		},
		{"plain version", "9.4.0\n", "9.4.0"},
		{"name without parens", "cnpm@10.0.1", "10.0.1"},
		{"empty output", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCnpmVersion(tt.input)
			if got != tt.want {
				t.Errorf("parseCnpmVersion(%q) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNodeSupportsCorepack(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"16.9.0", true},
		{"16.9.1", true},
		{"16.10.0", true},
		{"16.8.0", false},
		{"16.0.0", false},
		{"18.0.0", true},
		{"v18.0.0", true},
		{"20.10.0", true},
		{"14.17.0", false},
		{"", false},
		{"invalid", false},
		{"16", false},
		// M5: single-part major versions > 16 must return true even without a
		// minor (previously fell through to false because len(parts) < 2).
		{"18", true},
		{"v20", true},
		{"17", true},
		{"15", false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got := nodeSupportsCorepack(tt.version)
			if got != tt.want {
				t.Errorf("nodeSupportsCorepack(%q) = %v; want %v", tt.version, got, tt.want)
			}
		})
	}
}
