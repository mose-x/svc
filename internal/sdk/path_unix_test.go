//go:build !windows

package sdk

import "testing"

// TestSanitizeShellPath pins the filtering of rc-file banner garbage that a
// login shell prints to stdout before $PATH (observed on macOS zsh:
// "Restored session: Sun Aug 23 ..." leaking into the captured PATH).
func TestSanitizeShellPath(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			"clean path unchanged",
			"/usr/local/bin:/usr/bin:/bin",
			"/usr/local/bin:/usr/bin:/bin",
		},
		{
			"banner garbage dropped",
			"Restored session: Sun Aug 23 19:56:49 CST 2026:/usr/bin:/bin",
			"/usr/bin:/bin",
		},
		{
			"relative and empty segments dropped",
			":relative/dir:/usr/bin::",
			"/usr/bin",
		},
		{
			"whitespace segments dropped",
			"/usr/bin:56:49 CST 2026:/opt/sdks",
			"/usr/bin:/opt/sdks",
		},
		{"empty input", "", ""},
		{"all garbage", "Restored session: Sun Aug 23", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeShellPath(tt.raw); got != tt.want {
				t.Errorf("sanitizeShellPath(%q) = %q; want %q", tt.raw, got, tt.want)
			}
		})
	}
}
