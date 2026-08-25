package sdk

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// withMirrorsDir points the global mirror store at a temp dir for the duration
// of the test and restores the previous state afterwards (the store is a
// package-level singleton shared with other tests).
func withMirrorsDir(t *testing.T) string {
	t.Helper()
	prevPath, prevCached := globalMirrors.path, globalMirrors.cached
	t.Cleanup(func() {
		globalMirrors.mu.Lock()
		globalMirrors.path, globalMirrors.cached = prevPath, prevCached
		globalMirrors.mu.Unlock()
	})
	dir := t.TempDir()
	InitMirrorsFile(dir)
	return dir
}

func writeMirrors(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, mirrorsFileName), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestMirrorsSeedOnFirstLaunch(t *testing.T) {
	dir := withMirrorsDir(t)
	data, err := os.ReadFile(filepath.Join(dir, mirrorsFileName))
	if err != nil {
		t.Fatalf("seed file not written: %v", err)
	}
	if got := GetGithubMirrors(); !reflect.DeepEqual(got, defaultGithubMirrors) {
		t.Errorf("seeded list = %v; want defaults %v", got, defaultGithubMirrors)
	}
	_ = data
}

func TestMirrorsSnakeCaseKey(t *testing.T) {
	dir := withMirrorsDir(t)
	writeMirrors(t, dir, `{"github_mirrors": ["https://a.example", "https://b.example"]}`)
	if got := GetGithubMirrors(); !reflect.DeepEqual(got, []string{"https://a.example", "https://b.example"}) {
		t.Errorf("got %v; want the snake_case list", got)
	}
}

// Legacy users wrote the camelCase key ("githubMirrors"); their files were
// silently ignored before — the list must be honoured.
func TestMirrorsCamelCaseKey(t *testing.T) {
	dir := withMirrorsDir(t)
	writeMirrors(t, dir, `{"githubMirrors": ["https://camel.example"]}`)
	if got := GetGithubMirrors(); !reflect.DeepEqual(got, []string{"https://camel.example"}) {
		t.Errorf("got %v; want the camelCase list", got)
	}
}

func TestMirrorsBothKeysSnakeWins(t *testing.T) {
	dir := withMirrorsDir(t)
	writeMirrors(t, dir, `{"github_mirrors": ["https://snake.example"], "githubMirrors": ["https://camel.example"]}`)
	if got := GetGithubMirrors(); !reflect.DeepEqual(got, []string{"https://snake.example"}) {
		t.Errorf("got %v; want snake_case to win when both keys exist", got)
	}
}

func TestMirrorsEmptyListFallsBack(t *testing.T) {
	dir := withMirrorsDir(t)
	// Neither key present: valid JSON but no usable list -> defaults.
	writeMirrors(t, dir, `{}`)
	if got := GetGithubMirrors(); !reflect.DeepEqual(got, defaultGithubMirrors) {
		t.Errorf("got %v; want defaults for a key-less file", got)
	}
}

func TestMirrorsInvalidJSONFallsBackToCache(t *testing.T) {
	dir := withMirrorsDir(t)
	writeMirrors(t, dir, `{"github_mirrors": ["https://good.example"]}`)
	if got := GetGithubMirrors(); !reflect.DeepEqual(got, []string{"https://good.example"}) {
		t.Fatalf("setup: got %v", got)
	}
	// User is mid-edit: file temporarily invalid -> last good list survives.
	writeMirrors(t, dir, `{"github_mirrors": [`)
	if got := GetGithubMirrors(); !reflect.DeepEqual(got, []string{"https://good.example"}) {
		t.Errorf("got %v; want the cached good list while the file is invalid", got)
	}
}
