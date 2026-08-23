package helpers

import (
	"testing"
)

func TestExtractVersionFromString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"rustc version", "rustc 1.75.0 (cf89e91d4 2023-12-18)\n", "1.75.0"},
		{"go version multiline", "go version go1.21.5 darwin/arm64\n", "1.21.5"},
		{"node version", "v20.10.0\n", "20.10.0"},
		{"python version", "Python 3.13.1\n", "3.13.1"},
		{"empty output", "", ""},
		{"no version pattern", "/usr/local/bin", ""},
		{"sysroot path no version", "/usr\n", ""},
		{"two-digit minor", "rustc 1.80.1 (35 compilercentricities)\n", "1.80.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractVersionFromString(tt.input)
			if got != tt.want {
				t.Errorf("ExtractVersionFromString(%q) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestReplacePathEnv pins the PATH-override semantics: existing PATH
// entries (any case on the key) are dropped and the new value is appended.
func TestReplacePathEnv(t *testing.T) {
	env := []string{"A=1", "PATH=/old", "path=/other", "B=2"}
	out := ReplacePathEnv(env, "/new")
	want := []string{"A=1", "B=2", "PATH=/new"}
	if len(out) != len(want) {
		t.Fatalf("ReplacePathEnv = %v; want %v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("ReplacePathEnv[%d] = %q; want %q", i, out[i], want[i])
		}
	}
}

// TestValidatePathSegment covers the guard used for every user-supplied path
// segment (happy path, traversal, separators, illegal chars, reserved names).
func TestValidatePathSegment(t *testing.T) {
	valid := []string{"go", "1.21.5", "my-sdk", "1..2", "a b"}
	for _, s := range valid {
		if err := ValidatePathSegment(s); err != nil {
			t.Errorf("ValidatePathSegment(%q) unexpected error: %v", s, err)
		}
	}
	invalid := []string{"", ".", "..", "../x", `a\b`, "a/b", "a\x00b", "a<b", "CON", "nul", "LPT1"}
	for _, s := range invalid {
		if err := ValidatePathSegment(s); err == nil {
			t.Errorf("ValidatePathSegment(%q) = nil; want error", s)
		}
	}
}
