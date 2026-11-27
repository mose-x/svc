package cli

import (
	"encoding/base64"
	"encoding/json"
	"slices"
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

		nodeRow := tableRow(lines[1:], "Node.js")
		goRow := tableRow(lines[1:], "Go")
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

// The `check-proxy` tests never reach the network, and cannot: CheckProxy's
// SSRF guard refuses loopback and private addresses, so an httptest server is
// exactly the target it is built to reject. That guard is the reason the
// command is testable at all -- every case below is a refusal svc decides on
// before it opens a socket -- and the reason the success path is asserted on
// the renderer instead of on a probe.

func TestCheckProxyRefusesTargetsTheSsrfGuardOwns(t *testing.T) {
	cases := []struct {
		name string
		url  string
		// want is the catalog key the refusal is rendered from, and the
		// parameters the service put in the marker.
		want   string
		params map[string]string
	}{
		{
			name:   "a loopback address",
			url:    "http://127.0.0.1:1/probe",
			want:   "errors.private-ip",
			params: map[string]string{"host": "127.0.0.1"},
		},
		{
			name:   "a private address",
			url:    "http://192.168.1.1/probe",
			want:   "errors.private-ip",
			params: map[string]string{"host": "192.168.1.1"},
		},
		{
			name:   "a scheme that is not http",
			url:    "ftp://example.com/probe",
			want:   "errors.scheme-not-allowed",
			params: map[string]string{"scheme": "ftp"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, s := pipedRun(t, nil, "--lang", "en", "settings", "check-proxy", testCase.url)
			// private-ip and scheme-not-allowed are both proxy policy, and
			// policy is the ExitNetwork class: retrying on a better connection
			// will not help, but neither is anything broken.
			if code != ExitNetwork {
				t.Fatalf("svc settings check-proxy %s = exit %d, want %d; stderr:\n%s",
					testCase.url, code, ExitNetwork, s.stderr)
			}
			reason := catalogWord(t, "en", testCase.want, testCase.params)
			// A URL typed on the command line is reported as itself: the two
			// button labels belong to the window's buttons, not to the URLs.
			want := catalogWord(t, "en", "settings.proxyCheckFail",
				map[string]string{"target": testCase.url, "error": reason})
			if lines := outLines(s); len(lines) != 1 || lines[0] != want {
				t.Errorf("stdout = %q, want the one line %q", lines, want)
			}
			// The refusal carries no raw marker into either stream.
			if strings.Contains(s.stdout.String()+s.stderr.String(), "[svc:") {
				t.Errorf("an apperr marker reached the user:\nstdout:%s\nstderr:%s", s.stdout, s.stderr)
			}
		})
	}
}

func TestCheckProxyReportsEveryTargetItWasGiven(t *testing.T) {
	t.Run("two failures are two result lines and one nonzero exit", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--lang", "en", "settings", "check-proxy",
			"http://127.0.0.1:1/a", "http://10.0.0.1/b")
		if code != ExitNetwork {
			t.Fatalf("svc settings check-proxy with two bad targets = exit %d, want %d", code, ExitNetwork)
		}
		if lines := outLines(s); len(lines) != 2 {
			t.Fatalf("stdout has %d lines, want one per target:\n%s", len(lines), s.stdout)
		}
		if !strings.Contains(outLines(s)[1], "http://10.0.0.1/b") {
			t.Errorf("the second line does not name the second target:\n%s", s.stdout)
		}
	})

	t.Run("--json keeps the per-target outcomes apart", func(t *testing.T) {
		code, s := pipedRun(t, nil, "--json", "settings", "check-proxy",
			"http://127.0.0.1:1/a", "ftp://example.com/b")
		if code != ExitNetwork {
			t.Fatalf("svc --json settings check-proxy = exit %d, want %d", code, ExitNetwork)
		}
		var result ProxyCheckResult
		decodeJSON(t, s, &result)
		if got, want := jsonKeysOf(t, result), []string{"checks", "failed"}; !slices.Equal(got, want) {
			t.Errorf("ProxyCheckResult marshals to %v, want %v", got, want)
		}
		if result.Failed != 2 || len(result.Checks) != 2 {
			t.Fatalf("result = %+v, want two failed checks", result)
		}
		for _, check := range result.Checks {
			if check.OK {
				t.Errorf("%+v reports success for a refused target", check)
			}
			if check.Name != "" {
				t.Errorf("%+v carries a button name for a URL typed on the command line", check)
			}
			if check.Error == "" {
				t.Errorf("%+v failed without saying why", check)
			}
			if strings.Contains(check.Error, "[svc:") {
				t.Errorf("%+v puts a raw apperr marker in a machine-readable field", check)
			}
		}
	})
}

func TestCheckProxyGuardsAnEmptyCustomProxyAddress(t *testing.T) {
	cases := []struct {
		name     string
		proxy    config.ProxySettings
		refused  bool
		wantNote string
	}{
		{
			name:    "custom mode with no address is refused",
			proxy:   config.ProxySettings{Enabled: true, Mode: "custom", URL: "   "},
			refused: true,
		},
		{
			name:  "custom mode with an address is probed",
			proxy: config.ProxySettings{Enabled: true, Mode: "custom", URL: "http://127.0.0.1:7890"},
		},
		{
			name:  "system mode with no address is probed",
			proxy: config.ProxySettings{Enabled: true, Mode: "system"},
		},
		{
			name:  "a disabled proxy with a stale custom mode is probed",
			proxy: config.ProxySettings{Enabled: false, Mode: "custom"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			seed := func(home string) {
				writeSettings(t, home, config.AppSettings{Proxy: testCase.proxy})
			}
			// The loopback target is refused either way, so the only thing the
			// guard can change is WHICH refusal arrives: exit 2 with the
			// window's warning before anything is sent, or exit 6 from the
			// probe itself.
			code, s := pipedRun(t, seed, "--lang", "en", "settings", "check-proxy", "http://127.0.0.1:1/probe")
			if testCase.refused {
				if code != ExitUsage {
					t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitUsage, s.stderr)
				}
				if want := catalogWord(t, "en", "settings.proxyUrlRequired", nil); !strings.Contains(s.stderr.String(), want) {
					t.Errorf("stderr does not carry %q:\n%s", want, s.stderr)
				}
				if s.stdout.Len() != 0 {
					t.Errorf("stdout = %q, want nothing: the probe never ran", s.stdout)
				}
				return
			}
			if code != ExitNetwork {
				t.Fatalf("exit = %d, want %d -- the guard fired when it should not have; stderr:\n%s",
					code, ExitNetwork, s.stderr)
			}
			if want := catalogWord(t, "en", "settings.proxyUrlRequired", nil); strings.Contains(s.stderr.String(), want) {
				t.Errorf("stderr carries the empty-address warning for %+v:\n%s", testCase.proxy, s.stderr)
			}
		})
	}
}

// TestCheckProxyDefaultsAreTheGUITwoButtons pins the no-argument target list to
// the window's own probe buttons, by reading them out of SettingsPage.tsx. The
// parity is the whole reason the command has a default: a terminal that probed
// a different pair of sites would answer a different question than the one the
// "test proxy" buttons answer.
func TestCheckProxyDefaultsAreTheGUITwoButtons(t *testing.T) {
	targets := (&runner{}).proxyCheckTargets(nil)
	if len(targets) != 2 {
		t.Fatalf("check-proxy with no argument probes %d targets, want the GUI's two: %+v", len(targets), targets)
	}
	if targets[0].name != proxyTargetBaidu || targets[1].name != proxyTargetGoogle {
		t.Errorf("the default tokens are %q then %q, want %q then %q in the order the buttons are laid out",
			targets[0].name, targets[1].name, proxyTargetBaidu, proxyTargetGoogle)
	}
	if proxyTargetBaidu != "baidu" || proxyTargetGoogle != "google" {
		t.Errorf("the tokens are %q/%q: they are the machine-readable names in --json, so a rename is a decision",
			proxyTargetBaidu, proxyTargetGoogle)
	}

	page := string(repoFile(t, "frontend/src/components/Settings/SettingsPage.tsx"))
	for name, url := range proxyCheckURLs {
		if !strings.Contains(page, url) {
			t.Errorf("SettingsPage.tsx no longer probes %s at %s; the CLI default is stale", name, url)
		}
		if got := (&runner{}).proxyCheckTargets([]string{url}); got[0].name != "" {
			t.Errorf("an explicit %s was resolved to the button name %q; a typed URL is reported as itself", url, got[0].name)
		}
	}
	// A token typed as an argument is a URL, not a lookup key.
	if got := (&runner{}).proxyCheckTargets([]string{proxyTargetBaidu}); got[0].url != proxyTargetBaidu {
		t.Errorf("check-proxy %s probes %q, want the literal argument", proxyTargetBaidu, got[0].url)
	}
}

// TestPrintProxyChecksRendersTheGUISentences covers the half no probe can
// reach: the success line, and the two labels. The SSRF guard means a passing
// check cannot be produced by a test, so the renderer is driven directly with
// the result a passing probe would have left behind.
func TestPrintProxyChecksRendersTheGUISentences(t *testing.T) {
	s := newStreams()
	r := newRunner(t, s, globalFlags{lang: "en"}, homeAt(t, t.TempDir()))
	if err := r.resolveOptions(); err != nil {
		t.Fatalf("resolveOptions: %v", err)
	}
	r.printProxyChecks(ProxyCheckResult{Checks: []ProxyCheck{
		{URL: proxyCheckURLs[proxyTargetBaidu], Name: proxyTargetBaidu, OK: true},
		{URL: proxyCheckURLs[proxyTargetGoogle], Name: proxyTargetGoogle, Error: "timed out"},
		{URL: "https://example.com/probe", Error: "refused"},
	}})

	want := []string{
		catalogWord(t, "en", "settings.proxyCheckSuccess",
			map[string]string{"target": catalogWord(t, "en", "settings.proxyCheckBaidu", nil)}),
		catalogWord(t, "en", "settings.proxyCheckFail",
			map[string]string{"target": catalogWord(t, "en", "settings.proxyCheckGoogle", nil), "error": "timed out"}),
		catalogWord(t, "en", "settings.proxyCheckFail",
			map[string]string{"target": "https://example.com/probe", "error": "refused"}),
	}
	lines := outLines(s)
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), s.stdout)
	}
	for i, line := range want {
		if lines[i] != line {
			t.Errorf("line %d = %q, want %q", i, lines[i], line)
		}
	}
}

// TestCheckProxyReportsATransportFailure covers the one outcome that carries no
// apperr code: a host the transport could not reach. It exits 1 rather than 6
// because ExitCodeForError has no marker to read, and that is the service's
// classification, not the command's -- inventing a code here would put two
// answers to "was this a network problem?" in the tree.
//
// The failure comes from a closed loopback PROXY port, which is the only way to
// get one hermetically: the SSRF guard refuses loopback targets, and a real
// unreachable hostname costs either a 10s client timeout or a DNS dependency.
func TestCheckProxyReportsATransportFailure(t *testing.T) {
	seed := func(home string) {
		writeSettings(t, home, config.AppSettings{
			Proxy: config.ProxySettings{
				Enabled:  true,
				Mode:     "custom",
				Protocol: "http",
				URL:      "http://127.0.0.1:1",
			},
		})
	}
	target := "http://cli-check-proxy-target.example/probe"

	t.Run("text", func(t *testing.T) {
		code, s := pipedRun(t, seed, "--lang", "en", "settings", "check-proxy", target)
		if code != ExitError {
			t.Fatalf("exit = %d, want %d; stderr:\n%s", code, ExitError, s.stderr)
		}
		lines := outLines(s)
		if len(lines) != 1 {
			t.Fatalf("stdout has %d lines, want one:\n%s", len(lines), s.stdout)
		}
		// The GUI's sentence, once. A "connection failed: connection failed:"
		// stutter here means the service wrapped the reason in the phrase both
		// shells already put in front of it.
		if count := strings.Count(lines[0], "connection failed"); count != 1 {
			t.Errorf("the failure line says %q %dx, want it exactly once:\n%s", "connection failed", count, lines[0])
		}
		if !strings.HasPrefix(lines[0], target+" ") {
			t.Errorf("the failure line does not start with the target: %q", lines[0])
		}
		if !strings.Contains(lines[0], target) || !strings.Contains(lines[0], "refused") {
			t.Errorf("the failure line lost the transport's own reason:\n%s", lines[0])
		}
	})

	t.Run("--json keeps the rendered reason and the stable fields", func(t *testing.T) {
		code, s := pipedRun(t, seed, "--json", "settings", "check-proxy", target)
		if code != ExitError {
			t.Fatalf("exit = %d, want %d", code, ExitError)
		}
		var result ProxyCheckResult
		decodeJSON(t, s, &result)
		if result.Failed != 1 || len(result.Checks) != 1 || result.Checks[0].OK {
			t.Fatalf("result = %+v, want one failed check", result)
		}
		if result.Checks[0].URL != target {
			t.Errorf("url = %q, want %q", result.Checks[0].URL, target)
		}
		if strings.Contains(result.Checks[0].Error, "[svc:") {
			t.Errorf("a raw apperr marker reached a machine-readable field: %q", result.Checks[0].Error)
		}
		// The document carries the reason, not the sentence: `ok`, `url` and
		// `name` are already separate fields, so a consumer has no use for the
		// localized "{{target}} connection failed: ..." wrapper the text mode
		// renders around it -- and no business parsing one either.
		if strings.Contains(result.Checks[0].Error, "connection failed") {
			t.Errorf("error = %q, want the bare reason with no sentence wrapped around it", result.Checks[0].Error)
		}
		if !strings.Contains(result.Checks[0].Error, "refused") {
			t.Errorf("error = %q, want the transport's own cause", result.Checks[0].Error)
		}
	})
}
