package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"svc/internal/config"
	"svc/internal/svclock"
)

// The settings tests are split the same way the mutating ones are: the pure
// helpers (value canonicalisation, snapshot folding, the secret reader's
// fallback) run everywhere and touch nothing, while the command-level tests
// drive a real settings.json inside a temporary home.
//
// Only the install-path migration reaches appcore.LevelWrite -- it is the one
// key that re-runs the shim setup -- so only those tests skip on Windows, where
// a write-level bootstrap merges the shims directory into the real registry.
// Every other test here runs on all three CI platforms.

// storedSettings reads settings.json back off disk. Tests read the file rather
// than the service so a value that was only changed in memory cannot pass: the
// whole point of these commands is what the next run -- and the GUI -- sees.
func storedSettings(t *testing.T, home string) config.AppSettings {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".svc", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var settings config.AppSettings
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("settings.json is not the documented shape: %v\n%s", err, raw)
	}
	return settings
}

// seededToken is the base64 form settings.json stores, and the plaintext it
// decodes to. The plaintext is what a test asserts never appears in any output.
const (
	seededTokenPlain  = "ghp_abcdef123456"
	seededTokenBase64 = "Z2hwX2FiY2RlZjEyMzQ1Ng=="
)

func TestCanonicalSettingValue(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		raw     string
		want    string
		wantErr bool
	}{
		{"a restricted key is returned in its canonical spelling", settingTheme, "Dark", "dark", false},
		{"a restricted key tolerates surrounding space", settingLanguage, "  en ", "en", false},
		{"a restricted key rejects a value the GUI cannot express", settingDownloadThreads, "3", "", true},
		{"a restricted key rejects a value of the wrong type", settingProxyEnabled, "maybe", "", true},
		{"downloadThreads accepts every GUI radio value", settingDownloadThreads, "8", "8", false},
		{"proxy.protocol accepts both GUI radio values", settingProxyProtocol, "SOCKS5", "socks5", false},
		{"a free-text key is trimmed and kept as typed", settingGithubMirror, " https://mirror.example/ ", "https://mirror.example/", false},
		{"a free-text key may be cleared", settingGithubMirror, "  ", "", false},
		{"proxy.url is free text", settingProxyURL, "http://127.0.0.1:7890", "http://127.0.0.1:7890", false},
		{"installPath is free text", settingInstallPath, "/tmp/elsewhere", "/tmp/elsewhere", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := canonicalSettingValue(c.key, c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("canonicalSettingValue(%q, %q) = %q, want a refusal", c.key, c.raw, got)
				}
				// The refusal names the key and lists what IS accepted: an
				// error that only says "invalid" sends the user to --help.
				if !strings.Contains(err.Error(), c.key) {
					t.Errorf("the refusal %q does not name the key %q", err, c.key)
				}
				for _, choice := range settingChoices[c.key] {
					if !strings.Contains(err.Error(), choice) {
						t.Errorf("the refusal %q does not list the accepted value %q", err, choice)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("canonicalSettingValue(%q, %q): %v", c.key, c.raw, err)
			}
			if got != c.want {
				t.Errorf("canonicalSettingValue(%q, %q) = %q, want %q", c.key, c.raw, got, c.want)
			}
		})
	}
}

func TestApplySettingFoldsEveryKey(t *testing.T) {
	cases := []struct {
		key   string
		value string
		check func(t *testing.T, got config.AppSettings)
	}{
		{settingTheme, "dark", func(t *testing.T, got config.AppSettings) {
			if got.Theme != "dark" {
				t.Errorf("Theme = %q, want dark", got.Theme)
			}
		}},
		{settingLanguage, "en", func(t *testing.T, got config.AppSettings) {
			if got.Language != "en" {
				t.Errorf("Language = %q, want en", got.Language)
			}
		}},
		{settingDownloadThreads, "8", func(t *testing.T, got config.AppSettings) {
			if got.DownloadThreads != 8 {
				t.Errorf("DownloadThreads = %d, want 8", got.DownloadThreads)
			}
		}},
		{settingGithubMirror, "https://mirror.example/", func(t *testing.T, got config.AppSettings) {
			if got.GithubMirror != "https://mirror.example/" {
				t.Errorf("GithubMirror = %q", got.GithubMirror)
			}
		}},
		{settingProxyEnabled, "true", func(t *testing.T, got config.AppSettings) {
			if !got.Proxy.Enabled {
				t.Error("Proxy.Enabled = false, want true")
			}
		}},
		{settingProxyEnabled, "false", func(t *testing.T, got config.AppSettings) {
			if got.Proxy.Enabled {
				t.Error("Proxy.Enabled = true, want false")
			}
		}},
		{settingProxyMode, "custom", func(t *testing.T, got config.AppSettings) {
			if got.Proxy.Mode != "custom" {
				t.Errorf("Proxy.Mode = %q, want custom", got.Proxy.Mode)
			}
		}},
		{settingProxyURL, "http://127.0.0.1:7890", func(t *testing.T, got config.AppSettings) {
			if got.Proxy.URL != "http://127.0.0.1:7890" {
				t.Errorf("Proxy.URL = %q", got.Proxy.URL)
			}
		}},
		{settingProxyProtocol, "socks5", func(t *testing.T, got config.AppSettings) {
			if got.Proxy.Protocol != "socks5" {
				t.Errorf("Proxy.Protocol = %q, want socks5", got.Proxy.Protocol)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.key+"="+c.value, func(t *testing.T) {
			before := config.AppSettings{
				Theme:           "system",
				Language:        "zh",
				DownloadThreads: 4,
				Proxy:           config.ProxySettings{Enabled: false, Mode: "system", Protocol: "http"},
				GithubMirror:    "untouched",
				Endpoints:       map[string]string{"go": "https://goproxy.cn"},
				InstallPath:     "/untouched",
				GitHubToken:     seededTokenBase64,
			}
			got, err := applySetting(before, c.key, c.value)
			if err != nil {
				t.Fatalf("applySetting(%q, %q): %v", c.key, c.value, err)
			}
			c.check(t, got)
			// Folding one key must leave every other field alone, including the
			// three Service.Save preserves anyway: a snapshot that lost them
			// would be a stale-snapshot bug reintroduced from the CLI side.
			if got.Endpoints["go"] != "https://goproxy.cn" {
				t.Errorf("Endpoints = %v, want the seeded override kept", got.Endpoints)
			}
			if got.InstallPath != "/untouched" || got.GitHubToken != seededTokenBase64 {
				t.Errorf("installPath = %q, token = %q; both must survive", got.InstallPath, got.GitHubToken)
			}
		})
	}
}

func TestApplySettingRefusesAnUnknownKey(t *testing.T) {
	if _, err := applySetting(config.AppSettings{}, settingToken, "x"); err == nil {
		t.Error("applySetting accepted the token key, which has its own command")
	}
	if _, err := applySetting(config.AppSettings{}, "theme ", "dark"); err == nil {
		t.Error("applySetting accepted an untrimmed key")
	}
}

// TestSettingsSetWritesTheSetting is the end-to-end counterpart of the two pure
// tests above: what canonicalSettingValue and applySetting compute has to be
// what lands in the file.
func TestSettingsSetWritesTheSetting(t *testing.T) {
	cases := []struct {
		key   string
		value string
		check func(t *testing.T, got config.AppSettings)
	}{
		{settingTheme, "dark", func(t *testing.T, got config.AppSettings) {
			if got.Theme != "dark" {
				t.Errorf("stored Theme = %q, want dark", got.Theme)
			}
		}},
		{settingDownloadThreads, "2", func(t *testing.T, got config.AppSettings) {
			if got.DownloadThreads != 2 {
				t.Errorf("stored DownloadThreads = %d, want 2", got.DownloadThreads)
			}
		}},
		{settingProxyEnabled, "true", func(t *testing.T, got config.AppSettings) {
			if !got.Proxy.Enabled {
				t.Error("stored Proxy.Enabled = false, want true")
			}
		}},
		{settingGithubMirror, "", func(t *testing.T, got config.AppSettings) {
			if got.GithubMirror != "" {
				t.Errorf("stored GithubMirror = %q, want it cleared", got.GithubMirror)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			var home string
			code, s := pipedRun(t, func(h string) {
				home = h
				writeSettings(t, h, config.AppSettings{
					Theme: "system", Language: "zh", DownloadThreads: 4, GithubMirror: "https://old.example/",
				})
			}, "settings", "set", c.key, c.value)
			if code != ExitOK {
				t.Fatalf("svc settings set %s %q = exit %d, want %d; stderr:\n%s", c.key, c.value, code, ExitOK, s.stderr)
			}
			c.check(t, storedSettings(t, home))
			// The success sentence is the GUI's own toast, not a CLI synonym.
			if want := catalogWord(t, "zh", "settings.settingsSaved", nil); !strings.Contains(s.stdout.String(), want) {
				t.Errorf("stdout = %q, want %q", s.stdout, want)
			}
		})
	}
}

// TestSettingsSetKeepsTheFieldsAnotherPathOwns pins the CLI side of the
// stale-snapshot guard: `set` posts a whole settings object, exactly as the
// GUI's single-field handlers do, so a token and endpoints saved by their own
// commands must survive a theme change.
func TestSettingsSetKeepsTheFieldsAnotherPathOwns(t *testing.T) {
	var home, moved string
	code, s := pipedRun(t, func(h string) {
		home = h
		// A real directory, because Bootstrap points the whole run at whatever
		// installPath says and the operation lock is taken inside it.
		moved = filepath.Join(h, "moved")
		if err := os.MkdirAll(moved, 0755); err != nil {
			t.Fatalf("mkdir the migrated install dir: %v", err)
		}
		writeSettings(t, h, config.AppSettings{
			Theme:           "system",
			Language:        "zh",
			DownloadThreads: 4,
			GitHubToken:     seededTokenBase64,
			Endpoints:       map[string]string{"go": "https://goproxy.cn"},
			InstallPath:     moved,
		})
	}, "settings", "set", "theme", "light")
	if code != ExitOK {
		t.Fatalf("svc settings set theme light = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	got := storedSettings(t, home)
	if got.GitHubToken != seededTokenBase64 {
		t.Errorf("stored token = %q, want %q unchanged", got.GitHubToken, seededTokenBase64)
	}
	if got.Endpoints["go"] != "https://goproxy.cn" {
		t.Errorf("stored endpoints = %v, want the go override kept", got.Endpoints)
	}
	if got.InstallPath != moved {
		t.Errorf("stored installPath = %q, want it kept", got.InstallPath)
	}
	if got.Theme != "light" {
		t.Errorf("stored Theme = %q, want light", got.Theme)
	}
}

func TestSettingsSetRefusesAnUnknownKeyAndListsTheRest(t *testing.T) {
	code, s := terminalRun(t, nil, "settings", "set", "colour", "dark")
	if code != ExitUsage {
		t.Fatalf("an unknown key = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	// The hint is a Note: a terminal sees it, and the list of accepted keys is
	// the answer to the question the user just asked wrong.
	list := catalogWord(t, "zh", "cli.availableKeys", map[string]string{"list": strings.Join(settingsKeys, ", ")})
	if !strings.Contains(s.stderr.String(), list) {
		t.Errorf("stderr = %q, want the available-keys hint %q", s.stderr, list)
	}
	for _, key := range settingsKeys {
		if !strings.Contains(s.stderr.String(), key) {
			t.Errorf("the hint %q does not list %q", list, key)
		}
	}
	// A pipe gets the error and none of the advice, so a script's stderr stays
	// parseable.
	_, piped := pipedRun(t, nil, "settings", "set", "colour", "dark")
	if strings.Contains(piped.stderr.String(), strings.Join(settingsKeys, ", ")) {
		t.Errorf("a pipe saw the hint list: %q", piped.stderr)
	}
}

func TestSettingsSetRefusesAnInvalidValue(t *testing.T) {
	var home string
	code, s := pipedRun(t, func(h string) {
		home = h
		writeSettings(t, h, config.AppSettings{Theme: "system", Language: "zh", DownloadThreads: 4})
	}, "settings", "set", "downloadThreads", "3")
	if code != ExitUsage {
		t.Fatalf("downloadThreads 3 = exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	// 3 is a perfectly reasonable thread count and is still refused: the GUI's
	// radio offers four values, and a fifth would be a setting the downloader
	// was never exercised with.
	if !strings.Contains(s.stderr.String(), "1, 2, 4, 8") {
		t.Errorf("stderr = %q, want the accepted values listed", s.stderr)
	}
	if got := storedSettings(t, home); got.DownloadThreads != 4 {
		t.Errorf("a refused set changed downloadThreads to %d", got.DownloadThreads)
	}
}

// TestSettingsSetRefusesTheKeysAnotherCommandOwns pins design rule 6: a secret
// is never an argument, and an endpoint override is never a k=v pair.
func TestSettingsSetRefusesTheKeysAnotherCommandOwns(t *testing.T) {
	for key, command := range settingsOwnCommand {
		t.Run(key, func(t *testing.T) {
			code, s := pipedRun(t, nil, "settings", "set", key, "whatever")
			if code != ExitUsage {
				t.Fatalf("settings set %s = exit %d, want %d; stderr:\n%s", key, code, ExitUsage, s.stderr)
			}
			want := catalogWord(t, "zh", "cli.useCommand", map[string]string{"command": command})
			if !strings.Contains(s.stderr.String(), want) {
				t.Errorf("stderr = %q, want %q", s.stderr, want)
			}
			// The value the user typed must not be echoed back as though it had
			// been considered: for the token key that value is a secret.
			if strings.Contains(s.stderr.String(), "whatever") {
				t.Errorf("stderr repeats the refused value: %q", s.stderr)
			}
		})
	}
}

func TestSettingsSetJSONReportsTheChange(t *testing.T) {
	code, s := pipedRun(t, nil, "--json", "settings", "set", "proxy.mode", "CUSTOM")
	if code != ExitOK {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result SettingsWriteResult
	decodeJSON(t, s, &result)
	want := SettingsWriteResult{Action: actionSettingSet, Key: settingProxyMode, Value: "custom"}
	if result != want {
		t.Errorf("the document is %+v, want %+v", result, want)
	}
	if got := jsonKeysOf(t, result); len(got) != 3 {
		t.Errorf("the document carries %v, want exactly action, key and value", got)
	}
}

// TestSettingsSetStaysReadOnly is the reason these commands boot at LevelRead:
// changing a theme must not empty the download cache, create shims.json or
// delete a rollback backup, which is what a write-level startup does on the
// side.
func TestSettingsSetStaysReadOnly(t *testing.T) {
	home := t.TempDir()
	tmp := filepath.Join(home, ".svc", "tmp")
	if err := os.MkdirAll(tmp, 0755); err != nil {
		t.Fatalf("mkdir the temp dir: %v", err)
	}
	leftover := filepath.Join(tmp, "go1.22.0.tar.gz")
	if err := os.WriteFile(leftover, []byte("archive"), 0644); err != nil {
		t.Fatalf("seed the leftover download: %v", err)
	}

	code, s := runHome(t, home, "settings", "set", "theme", "dark")
	if code != ExitOK {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Errorf("a settings change emptied the temp dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".svc", "shims.json")); !os.IsNotExist(err) {
		t.Errorf("a settings change ran the shim setup (stat err = %v)", err)
	}
}

func TestSettingsSetFailsFastOnAContendedLockWithNoWait(t *testing.T) {
	home := t.TempDir()
	held, err := svclock.Acquire(filepath.Join(home, ".svc"), nil)
	if err != nil {
		t.Fatalf("take the operation lock: %v", err)
	}
	defer held.Release()

	code, s := runHome(t, home, "--no-wait", "settings", "set", "theme", "dark")
	if code != ExitBusy {
		t.Fatalf("a contended --no-wait set = exit %d, want %d; stderr:\n%s", code, ExitBusy, s.stderr)
	}
	if want := catalogWord(t, "zh", "errors.operation-in-progress", nil); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
}

// TestSettingsTokenNeverLeaksThePlaintext is the security assertion the whole
// read-from-stdin design exists for. The token is not an argument, so it is not
// in the process listing or the shell's history; this test pins the other half,
// that it is not in any output either -- including the --json document, which
// is the one a user is most likely to paste somewhere.
func TestSettingsTokenNeverLeaksThePlaintext(t *testing.T) {
	const plain = "ghp_secretVALUE999xyz"
	for _, c := range []struct {
		name string
		args []string
	}{
		{"a pipe", []string{"settings", "token"}},
		{"a JSON document", []string{"--json", "settings", "token"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			code, s := runAt(t, home, &Environment{}, plain+"\n", c.args...)
			if code != ExitOK {
				t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
			}
			if strings.Contains(s.stdout.String(), plain) || strings.Contains(s.stderr.String(), plain) {
				t.Fatalf("the plaintext token appears in the output:\nstdout:\n%s\nstderr:\n%s", s.stdout, s.stderr)
			}
			// It was stored, base64-encoded, exactly as SaveGithubToken does.
			got := storedSettings(t, home)
			if got.GitHubToken == plain {
				t.Error("settings.json holds the token in plaintext")
			}
			decoded, err := base64.StdEncoding.DecodeString(got.GitHubToken)
			if err != nil || string(decoded) != plain {
				t.Errorf("the stored token decodes to %q (err %v), want %q", decoded, err, plain)
			}
			// And what the run reports is the mask the GUI shows.
			masked := plain[:6] + "***" + plain[len(plain)-6:]
			if !strings.Contains(s.stdout.String(), masked) {
				t.Errorf("stdout = %q, want the masked form %q", s.stdout, masked)
			}
			if strings.Contains(s.stdout.String(), plain) {
				t.Errorf("stdout carries the plaintext next to the mask: %q", s.stdout)
			}
		})
	}
}

func TestSettingsTokenJSONReportsTheMaskOnly(t *testing.T) {
	const plain = "ghp_abcdef123456789"
	home := t.TempDir()
	code, s := runAt(t, home, &Environment{}, plain+"\n", "--json", "settings", "token")
	if code != ExitOK {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	var result SettingsWriteResult
	decodeJSON(t, s, &result)
	if result.Action != actionTokenSet || result.Key != settingToken {
		t.Errorf("the document is %+v, want a token action on the token key", result)
	}
	if result.Value == plain || strings.Contains(result.Value, plain) {
		t.Errorf("the document carries the plaintext token: %q", result.Value)
	}
	if result.Value != plain[:6]+"***"+plain[len(plain)-6:] {
		t.Errorf("the document reports %q, want the masked form", result.Value)
	}
}

func TestSettingsTokenReadsInputWithoutATrailingNewline(t *testing.T) {
	home := t.TempDir()
	// `svc settings token < token.txt` and `printf tok | svc settings token`
	// both end without a newline, and a token whose last characters were
	// dropped is a token that does not work.
	code, s := runAt(t, home, &Environment{}, "ghp_abcdef123456", "settings", "token")
	if code != ExitOK {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	decoded, err := base64.StdEncoding.DecodeString(storedSettings(t, home).GitHubToken)
	if err != nil || string(decoded) != "ghp_abcdef123456" {
		t.Errorf("the stored token decodes to %q (err %v)", decoded, err)
	}
}

// TestSettingsTokenPromptsOnlyWhereSomebodyCanAnswer pins the two halves of the
// prompt rule: a terminal is told what it is being asked for, and a pipe is not
// -- a prompt on a redirected stderr is text with nobody attached to it, and in
// JSON mode it would land in the same stream as the progress lines.
func TestSettingsTokenPromptsOnlyWhereSomebodyCanAnswer(t *testing.T) {
	const plain = "ghp_abcdef123456"
	want := catalogWord(t, "zh", "cli.tokenPrompt", nil)

	home := t.TempDir()
	code, s := runAt(t, home, &Environment{StdinTTY: true, StdoutTTY: true, EnvNoColor: "1"}, plain+"\n", "settings", "token")
	if code != ExitOK {
		t.Fatalf("a terminal run = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want the prompt %q", s.stderr, want)
	}
	if strings.Contains(s.stdout.String(), want) {
		t.Errorf("the prompt reached the result stream: %q", s.stdout)
	}

	home = t.TempDir()
	code, s = runAt(t, home, &Environment{}, plain+"\n", "settings", "token")
	if code != ExitOK {
		t.Fatalf("a piped run = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if strings.Contains(s.stderr.String(), want) {
		t.Errorf("a pipe saw the prompt: %q", s.stderr)
	}
}

func TestSettingsTokenRefusesEmptyInput(t *testing.T) {
	for _, input := range []string{"", "\n", "   \n"} {
		t.Run(strings.TrimSpace(input)+"/empty", func(t *testing.T) {
			home := t.TempDir()
			writeSettings(t, home, config.AppSettings{Theme: "system", GitHubToken: seededTokenBase64})
			code, s := runAt(t, home, &Environment{}, input, "settings", "token")
			if code != ExitUsage {
				t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
			}
			// The GUI's own sentence, and the GUI's own rule: an empty draft is
			// a mistake, not a request to clear. Clearing is --clear, which asks.
			if want := catalogWord(t, "zh", "settings.githubTokenEmpty", nil); !strings.Contains(s.stderr.String(), want) {
				t.Errorf("stderr = %q, want %q", s.stderr, want)
			}
			if got := storedSettings(t, home); got.GitHubToken != seededTokenBase64 {
				t.Errorf("an empty input changed the stored token to %q", got.GitHubToken)
			}
		})
	}
}

func TestSettingsTokenClearAsksFirst(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, home, config.AppSettings{Theme: "system", GitHubToken: seededTokenBase64})

	t.Run("a pipe without --yes is refused", func(t *testing.T) {
		code, s := runHome(t, home, "settings", "token", "--clear")
		if code != ExitUsage {
			t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
		}
		if got := storedSettings(t, home); got.GitHubToken != seededTokenBase64 {
			t.Errorf("an unconfirmed clear removed the token: %q", got.GitHubToken)
		}
	})

	t.Run("a decline changes nothing", func(t *testing.T) {
		code, s := interactiveRunHome(t, home, "n\n", "settings", "token", "--clear")
		if code != ExitCanceled {
			t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
		}
		// The question and its explanation are the GUI modal's own two strings.
		for _, key := range []string{"settings.githubTokenClear", "settings.githubTokenClearDesc"} {
			if want := catalogWord(t, "zh", key, nil); !strings.Contains(s.stderr.String(), want) {
				t.Errorf("stderr = %q, want %q (%s)", s.stderr, want, key)
			}
		}
		if got := storedSettings(t, home); got.GitHubToken != seededTokenBase64 {
			t.Errorf("a declined clear removed the token: %q", got.GitHubToken)
		}
	})

	t.Run("--yes clears it", func(t *testing.T) {
		code, s := runHome(t, home, "--yes", "settings", "token", "--clear")
		if code != ExitOK {
			t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		if got := storedSettings(t, home); got.GitHubToken != "" {
			t.Errorf("the stored token is %q, want it cleared", got.GitHubToken)
		}
		if want := catalogWord(t, "zh", "settings.githubTokenCleared", nil); !strings.Contains(s.stdout.String(), want) {
			t.Errorf("stdout = %q, want %q", s.stdout, want)
		}
		var result SettingsWriteResult
		code, s = runHome(t, home, "--yes", "--json", "settings", "token", "--clear")
		if code != ExitOK {
			t.Fatalf("the json clear = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}
		decodeJSON(t, s, &result)
		if result.Action != actionTokenClear || result.Value != "" {
			t.Errorf("the document is %+v, want a clear with no value", result)
		}
	})
}

func TestReadSecretLineFallsBackToOneLineOffATerminal(t *testing.T) {
	// An injected reader is never a terminal, so both shapes take the line
	// fallback -- including the one that claims a terminal is present, which is
	// what a test driving stdin from a buffer looks like. The no-echo rule is
	// about a real descriptor, and a reader that is not a descriptor echoes
	// nothing anyway.
	for _, stdinTTY := range []bool{false, true} {
		out := &bytes.Buffer{}
		got, err := readSecretLine(strings.NewReader("  ghp_abcdef123456\nignored\n"), stdinTTY, out)
		if err != nil {
			t.Fatalf("stdinTTY=%v: %v", stdinTTY, err)
		}
		if got != "ghp_abcdef123456" {
			t.Errorf("stdinTTY=%v: read %q, want the trimmed first line", stdinTTY, got)
		}
		if out.Len() != 0 {
			t.Errorf("stdinTTY=%v: the fallback wrote %q to the prompt stream", stdinTTY, out)
		}
	}
}

func TestReadOneLineTreatsAnEmptyStreamAsAnEmptyValue(t *testing.T) {
	for _, c := range []struct {
		name string
		in   io.Reader
		want string
	}{
		{"a closed stdin", strings.NewReader(""), ""},
		{"a nil reader", nil, ""},
		{"whitespace only", strings.NewReader("   \n"), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := readOneLine(c.in)
			if err != nil {
				t.Fatalf("readOneLine: %v", err)
			}
			if got != c.want {
				t.Errorf("readOneLine = %q, want %q", got, c.want)
			}
		})
	}
}

// TestSettingsSetInstallPathAsksBeforeMigrating covers the two ways the
// migration can stop before it copies anything: nobody to ask, and somebody who
// said no. A migration that copied a whole SDK tree before asking would be
// indistinguishable from one that ignored the answer.
func TestSettingsSetInstallPathAsksBeforeMigrating(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	home := t.TempDir()
	target := filepath.Join(home, "elsewhere")

	t.Run("a pipe without --yes is refused", func(t *testing.T) {
		code, _ := runHome(t, home, "settings", "set", "installPath", target)
		if code != ExitUsage {
			t.Fatalf("exit %d, want %d", code, ExitUsage)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Errorf("an unconfirmed migration created %s (stat err = %v)", target, err)
		}
	})

	t.Run("a decline changes nothing", func(t *testing.T) {
		code, s := interactiveRunHome(t, home, "n\n", "settings", "set", "installPath", target)
		if code != ExitCanceled {
			t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitCanceled, s.stderr)
		}
		for _, key := range []string{"settings.installPathConfirm", "settings.installPathConfirmDesc"} {
			params := map[string]string{"path": target}
			if want := catalogWord(t, "zh", key, params); !strings.Contains(s.stderr.String(), want) {
				t.Errorf("stderr = %q, want %q (%s)", s.stderr, want, key)
			}
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Errorf("a declined migration created %s (stat err = %v)", target, err)
		}
	})
}

// TestSettingsSetInstallPathReachesTheMigration proves the command calls
// storage.MigrateInstallPath with the path the user gave, without performing a
// successful migration: a relative path is refused by the service's own first
// guard, before anything is copied and before the logger is repointed at the
// new directory. The migration itself is covered end to end in
// internal/storage/migration_test.go.
func TestSettingsSetInstallPathReachesTheMigration(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	home := t.TempDir()
	code, s := runHome(t, home, "--yes", "settings", "set", "installPath", "relative/path")
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	if want := catalogWord(t, "zh", "errors.path-not-absolute", map[string]string{"path": "relative/path"}); !strings.Contains(s.stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", s.stderr, want)
	}
}

// TestSettingsSetInstallPathDefaultIsTheResetWording pins the keyword that
// stands in for the GUI's separate reset button, and the fact that it produces
// the reset pair of sentences rather than the save pair.
func TestSettingsSetInstallPathDefaultIsTheResetWording(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	// The wording needs a resolved catalog and nothing else, so the runner is
	// built against a temporary home rather than driven through Execute.
	r := newRunner(t, newStreams(), globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolve options: %v", err)
	}
	const target = "/tmp/somewhere"

	question, explanation := r.installPathWording(target, false)
	if want := catalogWord(t, "zh", "settings.installPathConfirm", nil); question != want {
		t.Errorf("the save question is %q, want %q", question, want)
	}
	if want := catalogWord(t, "zh", "settings.installPathConfirmDesc", map[string]string{"path": target}); explanation != want {
		t.Errorf("the save explanation is %q, want %q", explanation, want)
	}

	question, explanation = r.installPathWording(target, true)
	if want := catalogWord(t, "zh", "settings.installPathResetConfirm", nil); question != want {
		t.Errorf("the reset question is %q, want %q", question, want)
	}
	if want := catalogWord(t, "zh", "settings.installPathResetConfirmDesc", map[string]string{"path": target}); explanation != want {
		t.Errorf("the reset explanation is %q, want %q", explanation, want)
	}
}

// TestSettingsSetInstallPathDefaultRestoresTheDefaultPath runs the reset for
// real against a home that is already at the default, where the migration
// recognises source == target and returns without copying anything. It is the
// cheap way to prove the keyword resolves to GetDefaultInstallPath.
func TestSettingsSetInstallPathDefaultRestoresTheDefaultPath(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	home := t.TempDir()
	code, s := runHome(t, home, "--yes", "settings", "set", "installPath", "default")
	if code != ExitOK {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	if want := catalogWord(t, "zh", "settings.installPathSuccess", nil); !strings.Contains(s.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", s.stdout, want)
	}
	var result SettingsWriteResult
	code, s = runHome(t, home, "--yes", "--json", "settings", "set", "installPath", "DEFAULT")
	if code != ExitOK {
		t.Fatalf("the json reset = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
	}
	decodeJSON(t, s, &result)
	if result.Action != actionInstallPath || result.Key != settingInstallPath {
		t.Errorf("the document is %+v, want a migration of the install path", result)
	}
	if want := filepath.Join(home, ".svc"); result.Value != want {
		t.Errorf("the document reports %q, want the default %q", result.Value, want)
	}
}

func TestSettingsSetInstallPathRefusesTheFlagOverride(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	home := t.TempDir()
	other := filepath.Join(home, "override")
	target := filepath.Join(home, "elsewhere")
	code, s := runHome(t, home, "--yes", "--install-path", other, "settings", "set", "installPath", target)
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	// The refusal is about the flag, and says so in English prose like every
	// other usage error: the sentence is for a log, the exit code is for a
	// script.
	if !strings.Contains(s.stderr.String(), "--install-path") {
		t.Errorf("stderr = %q, want it to name the flag in the way", s.stderr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("a refused migration created %s", target)
	}
}

func TestSettingsSetInstallPathRefusesAnEmptyValue(t *testing.T) {
	skipOnWindowsRegistryWrites(t)
	home := t.TempDir()
	code, s := runHome(t, home, "--yes", "settings", "set", "installPath", "   ")
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
	}
	// The GUI's save button is disabled for an empty draft; the terminal has no
	// disabled state, so the refusal names the keyword that means "put it back".
	if !strings.Contains(s.stderr.String(), installPathDefaultKeyword) {
		t.Errorf("stderr = %q, want it to name the %q keyword", s.stderr, installPathDefaultKeyword)
	}
}
