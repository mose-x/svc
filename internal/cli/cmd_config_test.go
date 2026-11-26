package cli

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"svc/internal/config"
	"svc/internal/sdk"
)

// rawToken is a plausible GitHub PAT, long enough that MaskGithubToken shows
// its first and last six characters rather than masking it whole.
const rawToken = "ghp_abcdefghijklmnopqrstuvwxyz012345"

// seededSettings is the settings.json the settings tests run against. Every
// field is set to something other than its default, so a command that silently
// fell back to a default would be visible.
func seededSettings(t *testing.T) func(home string) {
	t.Helper()
	return func(home string) {
		writeSettings(t, home, config.AppSettings{
			Theme:    "dark",
			Language: "en",
			Proxy: config.ProxySettings{
				Enabled: true,
				Mode:    "custom",
				URL:     "http://127.0.0.1:7890",
			},
			Endpoints:       map[string]string{"nodejs": "https://npmmirror.com/mirrors/node"},
			GithubMirror:    "https://ghfast.top",
			GitHubToken:     base64.StdEncoding.EncodeToString([]byte(rawToken)),
			DownloadThreads: 8,
		})
	}
}

func TestSettingsShowReportsTheEffectiveSettings(t *testing.T) {
	t.Run("text renders every field in the GUI's words", func(t *testing.T) {
		code, s := pipedRun(t, seededSettings(t), "--lang", "en", "settings", "show")
		if code != ExitOK {
			t.Fatalf("svc settings show = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}

		text := s.stdout.String()
		for _, key := range []string{"settings.theme", "settings.language", "settings.proxy",
			"settings.githubMirror", "settings.githubToken", "settings.downloadThreads", "settings.installPath"} {
			if want := catalogWord(t, "en", key, nil); !strings.Contains(text, want) {
				t.Errorf("stdout does not carry the label %q (%s):\n%s", want, key, text)
			}
		}
		for _, want := range []string{
			catalogWord(t, "en", "settings.themeDark", nil),
			"en",
			catalogWord(t, "en", "settings.proxyCustom", nil),
			"http://127.0.0.1:7890",
			"https://ghfast.top",
			"8",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("stdout does not carry %q:\n%s", want, text)
			}
		}
		if s.stderr.Len() != 0 {
			t.Errorf("stderr = %q, want nothing: settings show is a result", s.stderr)
		}
	})

	t.Run("--json reports the three GUI calls in one envelope", func(t *testing.T) {
		code, s := pipedRun(t, seededSettings(t), "--json", "settings", "show")
		if code != ExitOK {
			t.Fatalf("svc --json settings show = exit %d, want %d", code, ExitOK)
		}

		var result SettingsResult
		decodeJSON(t, s, &result)
		if result.Settings.Theme != "dark" || result.Settings.DownloadThreads != 8 {
			t.Errorf("settings = %+v, want the seeded values", result.Settings)
		}
		if result.InstallPath == "" || result.DefaultInstallPath == "" {
			t.Errorf("installPath = %q, defaultInstallPath = %q, want both reported", result.InstallPath, result.DefaultInstallPath)
		}

		var document map[string]json.RawMessage
		decodeJSON(t, s, &document)
		want := jsonKeysOf(t, SettingsResult{})
		got := jsonKeysOf(t, document)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("the document's keys are [%s], want [%s]", strings.Join(got, " "), strings.Join(want, " "))
		}
	})

	t.Run("config is an alias of settings", func(t *testing.T) {
		run := sharedHome(t, seededSettings(t))
		_, spelled := run("settings", "show")
		_, alias := run("config", "show")
		if alias.stdout.String() != spelled.stdout.String() {
			t.Errorf("svc config show printed:\n%s\nwant what svc settings show prints:\n%s", alias.stdout, spelled.stdout)
		}
	})

	t.Run("an extra argument is a usage error", func(t *testing.T) {
		if code, _ := pipedRun(t, nil, "settings", "show", "extra"); code != ExitUsage {
			t.Errorf("svc settings show extra = exit %d, want %d", code, ExitUsage)
		}
	})

	t.Run("no settings file falls back to the defaults", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--lang", "en", "settings", "show")
		if code != ExitOK {
			t.Fatalf("svc settings show = exit %d, want %d", code, ExitOK)
		}
		text := s.stdout.String()
		if !strings.Contains(text, catalogWord(t, "en", "settings.themeSystem", nil)) {
			t.Errorf("stdout = %q, want the system theme as the default", text)
		}
		if !strings.Contains(text, catalogWord(t, "en", "settings.githubTokenNotSet", nil)) {
			t.Errorf("stdout = %q, want the token reported as unset", text)
		}
	})
}

// TestSettingsShowNeverLeaksTheToken is the security assertion of the command.
// The token is stored base64-encoded and the service masks it before it leaves
// the process, so neither the plaintext nor the stored form may reach a shell
// -- where it would land in a scrollback, a CI log or a pasted --json output.
func TestSettingsShowNeverLeaksTheToken(t *testing.T) {
	stored := base64.StdEncoding.EncodeToString([]byte(rawToken))
	mask := rawToken[:6] + "***" + rawToken[len(rawToken)-6:]

	for _, args := range [][]string{
		{"settings", "show"},
		{"--json", "settings", "show"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, s := terminalRun(t, seededSettings(t), args...)
			if code != ExitOK {
				t.Fatalf("svc %s = exit %d, want %d", strings.Join(args, " "), code, ExitOK)
			}
			both := s.stdout.String() + s.stderr.String()
			if strings.Contains(both, rawToken) {
				t.Errorf("the plaintext token reached a stream:\n%s", both)
			}
			if strings.Contains(both, stored) {
				t.Errorf("the stored base64 token reached a stream:\n%s", both)
			}
			if !strings.Contains(both, mask) {
				t.Errorf("neither stream carries the mask %q:\n%s", mask, both)
			}
		})
	}
}

// TestSettingsLabelsCoverEveryBranch drives the four renderers behind
// `settings show` directly, since reaching the unusual branches through the
// command would mean writing a settings.json for each one.
func TestSettingsLabelsCoverEveryBranch(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}

	t.Run("the three documented themes translate", func(t *testing.T) {
		for theme, key := range map[string]string{
			"system": "settings.themeSystem",
			"dark":   "settings.themeDark",
			"light":  "settings.themeLight",
		} {
			if got, want := r.themeLabel(theme), catalogWord(t, "zh", key, nil); got != want {
				t.Errorf("themeLabel(%q) = %q, want %q", theme, got, want)
			}
		}
	})

	t.Run("a hand-edited theme is shown as it stands", func(t *testing.T) {
		// Coercing it to "system" would tell the user something untrue about
		// their own file.
		if got := r.themeLabel("solarized"); got != "solarized" {
			t.Errorf("themeLabel(solarized) = %q, want it unchanged", got)
		}
		if got := r.themeLabel(""); got != placeholderNone {
			t.Errorf("themeLabel(\"\") = %q, want the placeholder", got)
		}
	})

	t.Run("the proxy label follows the radio group", func(t *testing.T) {
		cases := []struct {
			name  string
			proxy config.ProxySettings
			want  string
		}{
			{"disabled even with a mode and a URL", config.ProxySettings{Mode: "custom", URL: "http://x"}, catalogWord(t, "zh", "cli.stateDisabled", nil)},
			{"system", config.ProxySettings{Enabled: true, Mode: "system"}, catalogWord(t, "zh", "settings.proxySystem", nil)},
			{"custom with a URL", config.ProxySettings{Enabled: true, Mode: "custom", URL: "http://127.0.0.1:7890"},
				catalogWord(t, "zh", "settings.proxyCustom", nil) + " http://127.0.0.1:7890"},
			{"custom with no URL yet", config.ProxySettings{Enabled: true, Mode: "custom"}, catalogWord(t, "zh", "settings.proxyCustom", nil)},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				if got := r.proxyLabel(c.proxy); got != c.want {
					t.Errorf("proxyLabel(%+v) = %q, want %q", c.proxy, got, c.want)
				}
			})
		}
	})

	t.Run("the token label never invents a value", func(t *testing.T) {
		if got, want := r.tokenLabel(""), catalogWord(t, "zh", "settings.githubTokenNotSet", nil); got != want {
			t.Errorf("tokenLabel(\"\") = %q, want %q", got, want)
		}
		masked := rawToken[:6] + "***" + rawToken[len(rawToken)-6:]
		if got := r.tokenLabel(masked); got != masked {
			t.Errorf("tokenLabel(%q) = %q, want the mask passed through", masked, got)
		}
		// A short token is masked whole by the service; the label must not
		// "improve" it into something that looks like part of a real token.
		if got := r.tokenLabel("***"); got != "***" {
			t.Errorf("tokenLabel(***) = %q, want it unchanged", got)
		}
	})

	t.Run("an install path still at its default is marked", func(t *testing.T) {
		marked := r.installPathLabel(SettingsResult{InstallPath: "/home/u/.svc", DefaultInstallPath: "/home/u/.svc"})
		if want := "/home/u/.svc " + catalogWord(t, "zh", "settings.installPathDefault", nil); marked != want {
			t.Errorf("installPathLabel = %q, want %q", marked, want)
		}
		moved := r.installPathLabel(SettingsResult{InstallPath: "/data/svc", DefaultInstallPath: "/home/u/.svc"})
		if moved != "/data/svc" {
			t.Errorf("installPathLabel for a moved path = %q, want it unmarked", moved)
		}
		if got := r.installPathLabel(SettingsResult{}); got != placeholderNone {
			t.Errorf("installPathLabel with no path = %q, want the placeholder", got)
		}
	})
}

func TestEndpointsListReportsDefaultsAndOverrides(t *testing.T) {
	t.Run("--json reports both sides", func(t *testing.T) {
		code, s := pipedRun(t, seededSettings(t), "--json", "endpoints", "list")
		if code != ExitOK {
			t.Fatalf("svc --json endpoints list = exit %d, want %d; stderr:\n%s", code, ExitOK, s.stderr)
		}

		var result EndpointsResult
		decodeJSON(t, s, &result)
		if len(result.Defaults) != len(sdk.AllSdkTypes()) {
			t.Errorf("defaults holds %d entries, want one per SDK", len(result.Defaults))
		}
		if result.Custom == nil {
			t.Error("custom decoded to nil, want an object a consumer can index")
		}
		if got := result.Custom["nodejs"]; got != "https://npmmirror.com/mirrors/node" {
			t.Errorf("custom[nodejs] = %q, want the seeded override", got)
		}

		var document map[string]json.RawMessage
		decodeJSON(t, s, &document)
		if got := strings.TrimSpace(string(document["custom"])); strings.HasPrefix(got, "null") {
			t.Errorf("custom = %s, want an object rather than null", got)
		}
		want := jsonKeysOf(t, EndpointsResult{})
		got := jsonKeysOf(t, document)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("the document's keys are [%s], want [%s]", strings.Join(got, " "), strings.Join(want, " "))
		}
	})

	t.Run("text shows the override as the effective endpoint", func(t *testing.T) {
		code, s := pipedRun(t, seededSettings(t), "--lang", "en", "endpoints", "list")
		if code != ExitOK {
			t.Fatalf("svc endpoints list = exit %d, want %d", code, ExitOK)
		}

		lines := outLines(s)
		if len(lines) != len(sdk.AllSdkTypes())+1 {
			t.Fatalf("stdout has %d lines, want a header and one row per SDK:\n%s", len(lines), s.stdout)
		}
		for _, key := range []string{"path.sdkColumn", "cli.columnDefaultEndpoint", "cli.columnEndpoint"} {
			if want := catalogWord(t, "en", key, nil); !strings.Contains(lines[0], want) {
				t.Errorf("the header %q does not carry %q (%s)", lines[0], want, key)
			}
		}

		var nodeRow, goRow string
		for _, line := range lines[1:] {
			switch {
			case strings.HasPrefix(line, "Node.js"):
				nodeRow = line
			case strings.HasPrefix(line, "Go"):
				goRow = line
			}
		}
		if nodeRow == "" || goRow == "" {
			t.Fatalf("stdout has no Node.js or Go row:\n%s", s.stdout)
		}
		// The overridden SDK shows both the built-in endpoint and the one in
		// effect; that difference is the only reason the command exists.
		if !strings.Contains(nodeRow, "https://npmmirror.com/mirrors/node") {
			t.Errorf("the Node.js row %q does not carry the override", nodeRow)
		}
		if strings.Contains(goRow, "npmmirror") {
			t.Errorf("the Go row %q picked up an override that was not set for it", goRow)
		}
	})

	t.Run("an extra argument is a usage error", func(t *testing.T) {
		if code, _ := pipedRun(t, nil, "endpoints", "list", "nodejs"); code != ExitUsage {
			t.Errorf("svc endpoints list nodejs = exit %d, want %d", code, ExitUsage)
		}
	})
}

// TestEndpointsRenderingWithoutDefaults covers the empty branch, which the real
// registry cannot produce: a table with no rows is suppressed rather than
// printed as a bare header.
func TestEndpointsRenderingWithoutDefaults(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}

	r.printEndpoints(EndpointsResult{Custom: map[string]string{"nodejs": "https://mirror"}})
	if s.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing rather than a header with no rows", s.stdout)
	}
}

// TestEndpointsEffectiveColumnPrefersTheOverride pins the precedence rule the
// fetchers apply at download time, on a table built by hand so both branches
// are reached without a settings file per case.
func TestEndpointsEffectiveColumnPrefersTheOverride(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{}, tempHome(t, nil))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}

	r.printEndpoints(EndpointsResult{
		Defaults: []sdk.EndpointInfo{
			{SdkType: "nodejs", DisplayName: "Node.js", DefaultEndpoint: "https://nodejs.org"},
			{SdkType: "go", DisplayName: "Go", DefaultEndpoint: "https://go.dev"},
		},
		// An override saved as an empty string must not replace the default:
		// that is how the GUI clears one.
		Custom: map[string]string{"nodejs": "https://mirror", "go": ""},
	})

	lines := outLines(s)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want a header and two rows:\n%s", len(lines), s.stdout)
	}
	if !strings.Contains(lines[1], "https://mirror") {
		t.Errorf("the Node.js row %q does not show the override as effective", lines[1])
	}
	if !strings.Contains(lines[1], "https://nodejs.org") {
		t.Errorf("the Node.js row %q lost its default endpoint", lines[1])
	}
	if count := strings.Count(lines[2], "https://go.dev"); count != 2 {
		t.Errorf("the Go row %q shows the default %d times, want it in both columns", lines[2], count)
	}
}
