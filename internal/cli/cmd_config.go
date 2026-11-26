package cli

import (
	"strconv"

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

// newSettingsCommand builds `svc settings`. Only `show` exists in this phase:
// `set` and `token` change state and belong to P4, where `token` also gets the
// read-from-stdin treatment the design requires (no shell history, no echo).
func (r *runner) newSettingsCommand() *cobra.Command {
	settings := &cobra.Command{
		Use:     "settings",
		Short:   "Inspect svc's settings",
		Aliases: []string{"config"},
	}
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
		Short: "Inspect the SDK download endpoints",
	}
	endpoints.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List every SDK's default and effective endpoint",
		Long: "List each SDK's built-in download endpoint next to the one actually in effect.\n" +
			"\n" +
			"The two differ only where an override was saved. 'svc endpoints set' and\n" +
			"'unset' change them and belong to a later phase, since they write settings.json.",
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
