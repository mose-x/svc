package cli

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"svc/internal/config"
	"svc/internal/sdk"
)

// SettingsResult is what `svc settings show` reports.
//
// The GUI's settings page reads three bindings (GetSettings, GetInstallPath,
// GetDefaultInstallPath); the command answers once, so the three are gathered
// here. Settings is config.AppSettings verbatim and its GitHub token is already
// masked by settings.Service.Get -- the same masking rule the GUI shows, and
// the reason the raw token cannot leak into a shell log or a pasted --json
// output.
type SettingsResult struct {
	Settings           config.AppSettings `json:"settings"`
	InstallPath        string             `json:"installPath"`
	DefaultInstallPath string             `json:"defaultInstallPath"`
}

// newSettingsCommand builds `svc settings`. `show` reads the file; `set` and
// `token` write it and live in cmd_settings.go, which is also where the
// read-from-stdin treatment the token needs is explained.
func (r *runner) newSettingsCommand() *cobra.Command {
	settings := &cobra.Command{
		Use:     "settings",
		Short:   "Inspect and change svc's settings",
		Aliases: []string{"config"},
	}
	settings.AddCommand(r.newSettingsSetCommand(), r.newSettingsTokenCommand())
	settings.AddCommand(r.newSettingsCheckProxyCommand())
	settings.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print the effective settings",
		Long: "Print the settings svc is running with: theme, language, proxy, GitHub mirror and\n" +
			"token (masked), download threads, and the install path with its default for\n" +
			"comparison.\n" +
			"\n" +
			"These are the values in <home>/.svc/settings.json, which is the fixed location\n" +
			"the shim runtime reads regardless of any custom install path. --install-path\n" +
			"redirects the SDK tree for one run, not this file.",
		Args: r.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			result := SettingsResult{
				Settings:           core.Home.SettingsSvc.Get(),
				InstallPath:        core.Storage.GetInstallPath(),
				DefaultInstallPath: core.Storage.GetDefaultInstallPath(),
			}
			if err := r.printer.JSON(result); err != nil {
				return err
			}
			r.printer.KeyValue([]Pair{
				{Key: r.printer.T("settings.theme", nil), Value: r.themeLabel(result.Settings.Theme)},
				{Key: r.printer.T("settings.language", nil), Value: orNone(result.Settings.Language)},
				{Key: r.printer.T("settings.proxy", nil), Value: r.proxyLabel(result.Settings.Proxy)},
				{Key: r.printer.T("settings.githubMirror", nil), Value: orNone(result.Settings.GithubMirror)},
				{Key: r.printer.T("settings.githubToken", nil), Value: r.tokenLabel(result.Settings.GitHubToken)},
				{Key: r.printer.T("settings.downloadThreads", nil), Value: strconv.Itoa(result.Settings.DownloadThreads)},
				{Key: r.printer.T("settings.installPath", nil), Value: r.installPathLabel(result)},
			})
			return nil
		},
	})
	return settings
}

// themeLabel translates the stored theme identifier. The three spellings are
// the ones config.AppSettings documents and the ones the GUI's select offers;
// anything else is a hand-edited file and is shown as it stands rather than
// being silently coerced to "system".
func (r *runner) themeLabel(theme string) string {
	switch theme {
	case "system":
		return r.printer.T("settings.themeSystem", nil)
	case "dark":
		return r.printer.T("settings.themeDark", nil)
	case "light":
		return r.printer.T("settings.themeLight", nil)
	default:
		return orNone(theme)
	}
}

// proxyLabel renders the proxy setting the way the GUI's radio group does:
// disabled, system, or custom with the URL next to it.
func (r *runner) proxyLabel(proxy config.ProxySettings) string {
	if !proxy.Enabled {
		return r.printer.T("cli.stateDisabled", nil)
	}
	if proxy.Mode == "system" {
		return r.printer.T("settings.proxySystem", nil)
	}
	label := r.printer.T("settings.proxyCustom", nil)
	if proxy.URL != "" {
		label += " " + proxy.URL
	}
	return label
}

// tokenLabel never prints a raw token. settings.Service.Get has already
// replaced it with the first6***last6 mask, so the value here is either that
// mask or empty; empty gets the GUI's own "not set" wording.
func (r *runner) tokenLabel(masked string) string {
	if masked == "" {
		return r.printer.T("settings.githubTokenNotSet", nil)
	}
	return masked
}

// installPathLabel marks an install path that is still the default, so a user
// can tell "I never moved it" from "I moved it here on purpose".
func (r *runner) installPathLabel(result SettingsResult) string {
	path := orNone(result.InstallPath)
	if result.InstallPath != "" && result.InstallPath == result.DefaultInstallPath {
		path += " " + r.printer.T("settings.installPathDefault", nil)
	}
	return path
}

// ProxyCheckResult is what `svc settings check-proxy` reports through --json.
//
// The envelope is the CLI's own: CheckProxy answers with an error or nothing,
// and a command that probes several targets has to gather those answers into
// something one document can carry.
type ProxyCheckResult struct {
	// Checks holds one entry per target, in the order they were probed.
	Checks []ProxyCheck `json:"checks"`
	// Failed counts the entries that could not be reached. Any nonzero value
	// makes the run exit non-zero.
	Failed int `json:"failed"`
}

// ProxyCheck is one target's outcome.
type ProxyCheck struct {
	// URL is what was actually requested.
	URL string `json:"url"`
	// Name is the stable token of one of the two targets the GUI offers as
	// buttons, and is empty for a URL typed on the command line. The label a
	// human reads is rendered from it at print time; it is kept out of this
	// field because a machine-readable document must not change shape with
	// --lang.
	Name string `json:"name,omitempty"`
	// OK is whether the probe succeeded.
	OK bool `json:"ok"`
	// Error is the rendered reason an entry failed. Rendered rather than raw
	// for the reason DoctorCheck.Detail is: a --json consumer that has to
	// parse an apperr marker to explain a failure has been handed the CLI's
	// internal vocabulary instead of an answer.
	Error string `json:"error,omitempty"`
}

// The ProxyCheck.Name values, and the two URLs behind them. They are read off
// SettingsPage's two probe buttons rather than chosen: the point of the command
// is that a terminal can ask the same question the window's "test proxy"
// buttons ask, and a different pair of targets would be a different question.
const (
	proxyTargetBaidu  = "baidu"
	proxyTargetGoogle = "google"
)

// proxyCheckURLs maps a default target token to the URL the GUI probes.
var proxyCheckURLs = map[string]string{
	proxyTargetBaidu:  "https://www.baidu.com",
	proxyTargetGoogle: "https://www.google.com",
}

// proxyCheckTarget is one thing to probe. name is empty for a URL typed on the
// command line, which is reported as itself.
type proxyCheckTarget struct {
	name string
	url  string
}

// proxyCheckDefaults is the target list `check-proxy` uses when given no
// argument, in the order the GUI lays its buttons out.
var proxyCheckDefaults = []proxyCheckTarget{
	{name: proxyTargetBaidu, url: proxyCheckURLs[proxyTargetBaidu]},
	{name: proxyTargetGoogle, url: proxyCheckURLs[proxyTargetGoogle]},
}

// newSettingsCheckProxyCommand builds the proxy probe. It lives with `show`
// rather than in cmd_settings.go because it reads: it changes no setting, and
// the write half of the group is the half that has to take the operation lock.
func (r *runner) newSettingsCheckProxyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "check-proxy [url...]",
		Short: "Test whether the configured proxy can reach a site",
		Long: "Probe one or more URLs through the proxy settings.json currently configures, and\n" +
			"report each one.\n" +
			"\n" +
			"With no argument the two sites the window's proxy buttons probe are checked:\n" +
			"https://www.baidu.com and https://www.google.com. Checking both is what tells a\n" +
			"user in China whether the proxy is actually doing its job -- one is reachable\n" +
			"either way and the other is not reachable without it.\n" +
			"\n" +
			"A custom proxy mode with an empty address is refused before anything is sent,\n" +
			"with the window's own warning: the probe would silently fall back to a direct\n" +
			"connection and then report a misleading failure. Exit code is 2.\n" +
			"\n" +
			"CheckProxy carries an SSRF guard, so a loopback or private address is refused\n" +
			"rather than fetched -- exit 6 -- which is also why this command cannot be\n" +
			"pointed at a local test server.\n" +
			"\n" +
			"Each target is reported on stdout whether it passed or failed. When one\n" +
			"failed the run also exits non-zero, with the first failure deciding the code:\n" +
			"6 for a proxy-policy or HTTP-status refusal, 1 for a transport failure the\n" +
			"service reports without a code of its own. 'svc doctor' has the same shape --\n" +
			"the whole report on stdout, and an error on stderr because the report says\n" +
			"something the caller has to act on.",
		Args:              r.anyArgs(),
		ValidArgsFunction: r.noFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSettingsCheckProxy(cmd, args)
		},
	}
}

func (r *runner) runSettingsCheckProxy(cmd *cobra.Command, args []string) error {
	// No service graph, and deliberately: proxy.New is built by NewHome out of
	// the settings manager alone, and settings.json lives at the fixed home
	// location regardless of any --install-path, so there is nothing Bootstrap
	// would resolve that this command reads. Bootstrapping would heal fourteen
	// SDK layouts to answer a question about a socket.
	if r.home == nil {
		if err := r.resolveOptions(); err != nil {
			return err
		}
	}
	saved := r.home.Settings.Get().Proxy
	// The GUI's guard, ported. BuildClient skips proxy setup on an empty URL,
	// so without this the probe would go direct and report a failure that says
	// nothing about the proxy the user was asking about.
	if saved.Enabled && saved.Mode == "custom" && strings.TrimSpace(saved.URL) == "" {
		return r.usageError(cmd, errors.New(r.printer.T("settings.proxyUrlRequired", nil)))
	}

	targets := r.proxyCheckTargets(args)
	result := ProxyCheckResult{Checks: make([]ProxyCheck, 0, len(targets))}
	var firstFailure error
	for _, target := range targets {
		check := ProxyCheck{URL: target.url, Name: target.name}
		if err := r.home.Proxy.CheckProxy(target.url); err != nil {
			check.Error = translateWrapped(r.catalog, err)
			result.Failed++
			if firstFailure == nil {
				firstFailure = err
			}
		} else {
			check.OK = true
		}
		result.Checks = append(result.Checks, check)
	}

	if err := r.printer.JSON(result); err != nil {
		return err
	}
	r.printProxyChecks(result)
	return firstFailure
}

// proxyCheckTargets turns the positional arguments into the probe list. No
// argument means the GUI's own two buttons; any argument is a URL, and is
// reported as itself rather than being looked up in the token map -- letting
// `check-proxy baidu` probe a host called "baidu" would be a surprise nobody
// asked for.
func (r *runner) proxyCheckTargets(args []string) []proxyCheckTarget {
	if len(args) == 0 {
		return proxyCheckDefaults
	}
	targets := make([]proxyCheckTarget, 0, len(args))
	for _, arg := range args {
		targets = append(targets, proxyCheckTarget{url: arg})
	}
	return targets
}

func (r *runner) printProxyChecks(result ProxyCheckResult) {
	for _, check := range result.Checks {
		label := r.proxyCheckLabel(check)
		if check.OK {
			r.printLine("%s", r.printer.T("settings.proxyCheckSuccess",
				map[string]string{"target": label}))
			continue
		}
		r.printLine("%s", r.printer.T("settings.proxyCheckFail",
			map[string]string{"target": label, "error": check.Error}))
	}
}

// proxyCheckLabel renders a target as the word the GUI's toast uses. The switch
// is spelled out rather than built from the token because the catalog-key guard
// in output_guard_test.go can only see a literal, for the reason
// doctorCheckLabel gives.
func (r *runner) proxyCheckLabel(check ProxyCheck) string {
	switch check.Name {
	case proxyTargetBaidu:
		return r.printer.T("settings.proxyCheckBaidu", nil)
	case proxyTargetGoogle:
		return r.printer.T("settings.proxyCheckGoogle", nil)
	default:
		return check.URL
	}
}

// EndpointsResult is what `svc endpoints list` reports: the built-in presets
// and the user's overrides, side by side. The effective endpoint for an SDK is
// its override when one is set and the preset otherwise, which is what the
// fetchers resolve at download time.
type EndpointsResult struct {
	// Defaults is GetDefaultEndpoints: every SDK's built-in endpoint.
	Defaults []sdk.EndpointInfo `json:"defaults"`
	// Custom is GetEndpoints, which the service documents as never nil, so a
	// consumer always sees an object here and never null.
	Custom map[string]string `json:"custom"`
}

func (r *runner) newEndpointsCommand() *cobra.Command {
	endpoints := &cobra.Command{
		Use:   "endpoints",
		Short: "Inspect and override the SDK download endpoints",
	}
	endpoints.AddCommand(r.newEndpointsSetCommand(), r.newEndpointsUnsetCommand())
	endpoints.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List every SDK's default and effective endpoint",
		Long: "List each SDK's built-in download endpoint next to the one actually in effect.\n" +
			"\n" +
			"The two differ only where an override was saved; 'svc endpoints set' writes one\n" +
			"and 'svc endpoints unset' removes it again.",
		Args: r.noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			core, err := r.readCore()
			if err != nil {
				return err
			}
			result := EndpointsResult{
				Defaults: core.Home.SettingsSvc.GetDefaultEndpoints(),
				Custom:   core.Home.SettingsSvc.GetEndpoints(),
			}
			if err := r.printer.JSON(result); err != nil {
				return err
			}
			r.printEndpoints(result)
			return nil
		},
	})
	return endpoints
}

func (r *runner) printEndpoints(result EndpointsResult) {
	table := Table{
		Columns: []Column{
			{Header: r.printer.T("path.sdkColumn", nil)},
			{Header: r.printer.T("cli.columnDefaultEndpoint", nil)},
			{Header: r.printer.T("cli.columnEndpoint", nil)},
		},
	}
	for _, info := range result.Defaults {
		effective := info.DefaultEndpoint
		if custom := result.Custom[info.SdkType]; custom != "" {
			effective = custom
		}
		table.Rows = append(table.Rows, []string{info.DisplayName, info.DefaultEndpoint, effective})
	}
	if len(table.Rows) == 0 {
		return
	}
	r.printer.Table(table)
}
