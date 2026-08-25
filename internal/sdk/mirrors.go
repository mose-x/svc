package sdk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// mirrorsFileName is the on-disk "easter egg" config file under the SVC data
// dir. It is NOT surfaced anywhere in the UI -- it exists so that users who
// discover it can customise the built-in GitHub mirror fallback list without
// touching code or the settings UI. The app seeds it with a sensible default
// list on first launch; from then on it is entirely user-owned.
const mirrorsFileName = "mirrors.json"

// defaultGithubMirrors is the seed list written into mirrors.json on first
// launch. These are public GitHub reverse-proxy mirrors that forward requests
// to api.github.com / github.com. Tsinghua/Aliyun are NOT listed because they
// mirror PyPI/NPM sources, not the GitHub releases API. A mirror here receives
// requests WITHOUT the user's GitHub token (the proxy talks to GitHub under its
// own identity), so listing a third-party mirror never leaks the PAT.
var defaultGithubMirrors = []string{
	"https://ghfast.top",
	"https://ghproxy.com",
	"https://mirror.ghproxy.com",
}

// mirrorsFile is the JSON shape of mirrors.json. The list key is read under
// BOTH spellings: current builds seed/read "github_mirrors" (snake_case), but
// early releases documented/wrote the camelCase "githubMirrors" form, and user
// files from that era must keep working instead of being silently ignored.
// When both keys are present, snake_case wins.
type mirrorsFile struct {
	GithubMirrors      []string `json:"github_mirrors"`
	GithubMirrorsCamel []string `json:"githubMirrors"`
}

// list returns the effective mirror list from either accepted key spelling.
func (m mirrorsFile) list() []string {
	if m.GithubMirrors != nil {
		return m.GithubMirrors
	}
	return m.GithubMirrorsCamel
}

// mirrorStore holds the in-memory copy of the mirror list and the path to the
// on-disk file. The file is read on first access and re-read on every
// GetGithubMirrors call so edits to mirrors.json take effect without a restart
// (the file is tiny, so re-reading is cheap).
type mirrorStore struct {
	mu   sync.Mutex
	path string
	// cached is the last successfully read list; used as a fallback if a
	// re-read fails (e.g. the user is mid-edit and the file is momentarily
	// invalid). nil means "not loaded yet".
	cached []string
}

var globalMirrors = &mirrorStore{}

// InitMirrorsFile sets the on-disk location of mirrors.json and seeds it with
// defaults if it does not yet exist. Called once at startup after the SVC data
// dir is known. Safe to call multiple times; the latest path wins.
func InitMirrorsFile(svcDir string) {
	globalMirrors.mu.Lock()
	defer globalMirrors.mu.Unlock()
	globalMirrors.path = filepath.Join(svcDir, mirrorsFileName)
	globalMirrors.cached = nil // force a re-read against the new path
	// Seed the file on first launch so users have a template to edit. A missing
	// file is the normal first-run state; any other error (e.g. permission) is
	// ignored -- GetGithubMirrors will fall back to the in-memory defaults.
	if _, err := os.Stat(globalMirrors.path); os.IsNotExist(err) {
		seed := mirrorsFile{GithubMirrors: defaultGithubMirrors}
		if data, err := json.MarshalIndent(seed, "", "  "); err == nil {
			_ = os.WriteFile(globalMirrors.path, data, 0644)
		}
	}
}

// GetGithubMirrors returns the list of GitHub mirror URLs to try as a fallback
// when a direct api.github.com request fails. It re-reads mirrors.json on each
// call so user edits take effect live; on any read error it falls back to the
// last good list (or the built-in defaults if none has ever loaded).
//
// Returned URLs are bare mirror roots (e.g. "https://ghfast.top"); callers
// prepend them to the full GitHub URL to form the proxied URL.
func GetGithubMirrors() []string {
	globalMirrors.mu.Lock()
	defer globalMirrors.mu.Unlock()

	if globalMirrors.path == "" {
		// Not initialised (e.g. unit test) -- return defaults so behaviour is
		// still sensible.
		return append([]string(nil), defaultGithubMirrors...)
	}

	data, err := os.ReadFile(globalMirrors.path)
	if err == nil {
		var mf mirrorsFile
		if jsonErr := json.Unmarshal(data, &mf); jsonErr == nil {
			if list := mf.list(); list != nil {
				globalMirrors.cached = list
				return append([]string(nil), list...)
			}
		}
	}
	// Read or parse failed: use the last good list, or defaults if none.
	if globalMirrors.cached != nil {
		return append([]string(nil), globalMirrors.cached...)
	}
	return append([]string(nil), defaultGithubMirrors...)
}
