package apperr

import (
	"strings"
	"testing"
)

func TestNewNoParams(t *testing.T) {
	err := New(AppNotInitialized, nil)
	if err.Error() != "[svc:app-not-initialized]" {
		t.Errorf("got %q; want bare marker", err.Error())
	}
}

func TestNewWithParams(t *testing.T) {
	err := New(ProtectedImport, map[string]string{
		"cmd":  "java",
		"path": "/usr/bin/java",
		"sdk":  "jdk",
	})
	got := err.Error()
	want := `[svc:protected-import]{"cmd":"java","path":"/usr/bin/java","sdk":"jdk"}`
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestNewParamsSortedDeterministic(t *testing.T) {
	// json.Marshal sorts map keys; identical inputs must produce identical
	// error text so tests and log diffs stay stable.
	a := New(ChecksumMismatch, map[string]string{"expected": "aa", "got": "bb"})
	b := New(ChecksumMismatch, map[string]string{"got": "bb", "expected": "aa"})
	if a.Error() != b.Error() {
		t.Errorf("non-deterministic: %q vs %q", a.Error(), b.Error())
	}
}

func TestNewSpecialCharsPreserved(t *testing.T) {
	// Windows paths with backslashes and colons must survive JSON encoding.
	err := New(PathNotExist, map[string]string{"path": `C:\Program Files\jdk`})
	if !strings.Contains(err.Error(), `C:\\Program Files\\jdk`) {
		t.Errorf("path not preserved in %q", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "[svc:path-not-exist]") {
		t.Errorf("marker missing in %q", err.Error())
	}
}
