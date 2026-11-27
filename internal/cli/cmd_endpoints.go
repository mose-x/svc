package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"svc/internal/sdk"
)

// The writing half of `svc endpoints`: one override per invocation.
//
// Both commands are ports of handlers in
// frontend/src/components/Settings/SettingsPage.tsx. The GUI edits a whole
// draft map and saves it in one go (handleSaveEndpoints) or deletes one entry
// and saves the rest (handleResetOneEndpoint); a terminal names one SDK and
// one URL, so the map is read, changed and written back inside the operation
// lock instead of being drafted on screen first. What lands in settings.json is
// the same either way, and so is the sentence the user reads: both GUI paths
// finish with the plain "settings saved" toast, and both are confirmed first.
//
// settings.Service.SaveEndpoints REPLACES the whole map, which is why the
// read-modify-write has to be atomic with respect to other svc processes. That
// is the operation lock's job, and it is also why the map returned by
// GetEndpoints is copied before being edited: it is the live map inside the
// settings manager, and mutating it in place would change what a later Get
// reports even if the save that follows fails.

func (r *runner) newEndpointsSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set <sdk> <url>",
		Short: "Override one SDK's download endpoint",
		Long: "Point one SDK at a different download endpoint -- a company mirror, a national\n" +
			"mirror, a local cache -- and save it in <home>/.svc/settings.json.\n" +
			"\n" +
			"The override is what the fetcher resolves at download time, so it affects both\n" +
			"the version list and the files themselves. 'svc endpoints list' shows every\n" +
			"SDK's default next to the one in effect.\n" +
			"\n" +
			"Only the SDK named here changes; every other override already saved is kept.",
		Args:              r.exactArgs(2),
		ValidArgsFunction: r.sdkThenNothing,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runEndpointsSet(cmd, args[0], args[1])
		},
	}
}

func (r *runner) runEndpointsSet(cmd *cobra.Command, sdkInput, rawURL string) error {
	endpoint := strings.TrimSpace(rawURL)
	if endpoint == "" {
		// An empty value is what the GUI's draft map means by "reset this one",
		// and it has a command of its own here. Refusing beats saving an
		// override that no fetcher would ever resolve.
		return r.usageError(cmd, fmt.Errorf("an empty endpoint resets the SDK to its default; %s",
			r.printer.T("cli.useCommand", map[string]string{"command": "svc endpoints unset <sdk>"})))
	}
	core, err := r.readCore()
	if err != nil {
		return err
	}
	kind, err := r.resolveSdk(core, sdkInput)
	if err != nil {
		return err
	}
	if err := r.confirmMutation(
		r.printer.T("endpoint.confirmSave", nil),
		r.printer.T("endpoint.confirmSaveDesc", nil),
	); err != nil {
		return err
	}
	if err := r.withOpLock(core, func() error {
		endpoints := copyEndpoints(core.Home.SettingsSvc.GetEndpoints())
		endpoints[string(kind)] = endpoint
		return core.Home.SettingsSvc.SaveEndpoints(endpoints)
	}); err != nil {
		return err
	}
	return r.reportSetting(SettingsWriteResult{
		Action: actionEndpointSet,
		Key:    string(kind),
		Value:  endpoint,
	}, r.printer.T("settings.settingsSaved", nil), endpoint)
}

func (r *runner) newEndpointsUnsetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <sdk>",
		Short: "Restore one SDK's default download endpoint",
		Long: "Remove one SDK's endpoint override and leave it downloading from the built-in\n" +
			"default again. Every other override already saved is kept.\n" +
			"\n" +
			"Removing an override that is not there is not an error: the state the command\n" +
			"was asked for is the state it leaves behind.",
		Args:              r.exactArgs(1),
		ValidArgsFunction: r.sdkCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runEndpointsUnset(cmd, args[0])
		},
	}
}

func (r *runner) runEndpointsUnset(cmd *cobra.Command, sdkInput string) error {
	core, err := r.readCore()
	if err != nil {
		return err
	}
	kind, err := r.resolveSdk(core, sdkInput)
	if err != nil {
		return err
	}
	// The GUI's reset button names the SDK by its display name in both
	// sentences, not by the identifier the map is keyed on.
	displayName := sdk.SdkDisplayName(kind)
	if err := r.confirmMutation(
		r.printer.T("endpoint.confirmResetOne", map[string]string{"sdk": displayName}),
		r.printer.T("endpoint.confirmResetOneDesc", map[string]string{"sdk": displayName}),
	); err != nil {
		return err
	}
	if err := r.withOpLock(core, func() error {
		endpoints := copyEndpoints(core.Home.SettingsSvc.GetEndpoints())
		delete(endpoints, string(kind))
		return core.Home.SettingsSvc.SaveEndpoints(endpoints)
	}); err != nil {
		return err
	}
	return r.reportSetting(SettingsWriteResult{
		Action: actionEndpointUnset,
		Key:    string(kind),
	}, r.printer.T("settings.settingsSaved", nil), "")
}

// copyEndpoints copies the live override map out of the settings manager.
// GetEndpoints returns the map the manager holds -- the service documents it as
// never nil, but it is not a copy -- so editing it directly would change the
// in-memory settings before SaveEndpoints had agreed to anything.
func copyEndpoints(source map[string]string) map[string]string {
	copied := make(map[string]string, len(source))
	for key, value := range source {
		copied[key] = value
	}
	return copied
}
