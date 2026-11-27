package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"svc/internal/config"
)

// The writing half of `svc settings`: one key at a time, plus the GitHub token,
// which is deliberately not one of those keys.
//
// Each branch is a port of a handler in
// frontend/src/components/Settings/SettingsPage.tsx -- updateSettings for the
// ordinary keys, handleSaveToken and handleClearToken for the token,
// handleSaveInstallPath and handleResetInstallPath for the install path -- and
// each one calls the same service the GUI calls and reports the same sentence.
//
// Four differences from the GUI are intentional and documented where they are
// made: a confirmation the user declines is an exit code rather than a closed
// modal; the operation lock is taken around settings.json, which the GUI does
// not take at all yet; the token is read from stdin instead of from an
// argument; and a `language` change is reported in the language the run started
// in, because the catalog is resolved during option resolution, before any
// command body runs. The GUI switches its catalog first and toasts in the new
// language; the difference is one sentence and it disappears on the next run.

// SettingsWriteResult is what a settings-writing command reports through
// --json. The envelope is the CLI's own -- there is no service struct that
// means "one setting changed" -- but every field is a fact about the run.
type SettingsWriteResult struct {
	// Action is the operation that ran: one of the action constants below.
	Action string `json:"action"`
	// Key is the setting key, or the SDK identifier for an endpoint change.
	Key string `json:"key,omitempty"`
	// Value is what the key holds afterwards. For a token it is the MASKED
	// form read back from the service, never the plaintext: the raw token is
	// not an argument, is not echoed, and must not be in a redirected result
	// either, because a `--json` document is exactly the kind of thing that
	// gets pasted into an issue.
	Value string `json:"value,omitempty"`
}

// The Action values SettingsWriteResult reports.
const (
	actionSettingSet    = "set"
	actionTokenSet      = "token"
	actionTokenClear    = "clear-token"
	actionInstallPath   = "migrate"
	actionEndpointSet   = "endpoint-set"
	actionEndpointUnset = "endpoint-unset"
)

// The keys `svc settings set` writes itself, in the order the GUI's settings
// page presents them. installPath is here rather than in the ordinary path
// because it does not edit a field: it moves the whole tree.
const (
	settingTheme           = "theme"
	settingLanguage        = "language"
	settingDownloadThreads = "downloadThreads"
	settingGithubMirror    = "githubMirror"
	settingProxyEnabled    = "proxy.enabled"
	settingProxyMode       = "proxy.mode"
	settingProxyURL        = "proxy.url"
	settingProxyProtocol   = "proxy.protocol"
	settingInstallPath     = "installPath"
	// The two keys `set` refuses, each because another command owns them.
	settingToken     = "token"
	settingEndpoints = "endpoints"
)

// settingsKeys is the accepted key list, and the list `cli.availableKeys`
// prints when an unknown one arrives. The two refused keys are not in it: a
// hint that names a key the command then rejects is worse than one that does
// not mention it, and the refusal names the right command itself.
var settingsKeys = []string{
	settingTheme,
	settingLanguage,
	settingDownloadThreads,
	settingGithubMirror,
	settingProxyEnabled,
	settingProxyMode,
	settingProxyURL,
	settingProxyProtocol,
	settingInstallPath,
}

// settingsOwnCommand maps a key `set` refuses to the command that owns it.
//
// The token because a secret on argv is a secret in the shell's history file
// and in every `ps` listing for the lifetime of the process; the endpoints
// because one override per invocation, with an SDK identifier that
// `resolveSdk` validates, beats a `k=v,k=v` mini-language that would have to
// grow its own quoting rules.
var settingsOwnCommand = map[string]string{
	settingToken:     "svc settings token",
	settingEndpoints: "svc endpoints set <sdk> <url>",
}

// settingChoices lists the accepted values of every key the GUI renders as a
// radio group or a switch, so a value the window cannot express is a value the
// terminal refuses too. A key absent from this map takes free text.
//
// The lists are the GUI's options read off SettingsPage.tsx rather than a
// judgement about what would be reasonable: `downloadThreads` in particular is
// four values and not "any positive integer", because a thread count the
// downloader was never tested with is not a setting.
var settingChoices = map[string][]string{
	settingTheme:           {"system", "dark", "light"},
	settingLanguage:        {"zh", "en"},
	settingDownloadThreads: {"1", "2", "4", "8"},
	settingProxyEnabled:    {"true", "false"},
	settingProxyMode:       {"system", "custom"},
	settingProxyProtocol:   {"http", "socks5"},
}

// errInstallPathFlagActive refuses a migration while --install-path is in
// effect. Bootstrap pointed the config at the override, so the directory a
// migration would copy FROM is the one the flag named for this run only, and
// the value it would persist is one the user asked svc not to save.
var errInstallPathFlagActive = errors.New("--install-path is in effect; drop the flag to migrate the install path")

// installPathDefaultKeyword is the value that means "put it back where it
// started". The GUI has a separate reset button for this; a terminal spells it
// as a value, and the confirmation it triggers is the reset one.
const installPathDefaultKeyword = "default"

// canonicalSettingValue validates one key/value pair and returns the value to
// store.
//
// A key with a fixed choice list is matched case-insensitively and returned in
// its canonical spelling, so `svc settings set theme Dark` stores "dark" --
// the same string the GUI's radio writes -- instead of a spelling the theme
// switch in App.tsx would fall through. Free-text keys are trimmed and
// otherwise kept as typed.
func canonicalSettingValue(key, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	choices, restricted := settingChoices[key]
	if !restricted {
		return value, nil
	}
	for _, choice := range choices {
		if strings.EqualFold(value, choice) {
			return choice, nil
		}
	}
	return "", fmt.Errorf("invalid value %q for %s; accepted: %s", raw, key, strings.Join(choices, ", "))
}

// applySetting folds one canonical key/value pair into a settings snapshot and
// returns the snapshot to persist.
//
// The snapshot it is given is the one settings.Service.Get produced, whose
// GitHub token is already masked. That is safe rather than lossy because
// Service.Save preserves the stored token, endpoints and install path and
// ignores whatever the snapshot carries for them -- the same stale-snapshot
// guard the GUI relies on when its single-field handlers post the whole object.
func applySetting(current config.AppSettings, key, value string) (config.AppSettings, error) {
	switch key {
	case settingTheme:
		current.Theme = value
	case settingLanguage:
		current.Language = value
	case settingDownloadThreads:
		threads, err := strconv.Atoi(value)
		if err != nil {
			return current, err
		}
		current.DownloadThreads = threads
	case settingGithubMirror:
		current.GithubMirror = value
	case settingProxyEnabled:
		current.Proxy.Enabled = value == "true"
	case settingProxyMode:
		current.Proxy.Mode = value
	case settingProxyURL:
		current.Proxy.URL = value
	case settingProxyProtocol:
		current.Proxy.Protocol = value
	default:
		return current, fmt.Errorf("unknown settings key %q", key)
	}
	return current, nil
}

func (r *runner) newSettingsSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Change one setting",
		Long: "Change one setting in <home>/.svc/settings.json, the same file the GUI edits.\n" +
			"\n" +
			"Keys and their accepted values:\n" +
			"\n" +
			"  theme             system | dark | light\n" +
			"  language          zh | en\n" +
			"  downloadThreads   1 | 2 | 4 | 8\n" +
			"  githubMirror      any URL, empty to clear\n" +
			"  proxy.enabled     true | false\n" +
			"  proxy.mode        system | custom\n" +
			"  proxy.url         any URL\n" +
			"  proxy.protocol    http | socks5\n" +
			"  installPath       an absolute path, or 'default' to move it back\n" +
			"\n" +
			"Two keys are refused here because another command owns them: the GitHub token\n" +
			"belongs to 'svc settings token', which reads it from stdin rather than from an\n" +
			"argument so it never reaches the shell's history, and the download endpoints\n" +
			"belong to 'svc endpoints set'.\n" +
			"\n" +
			"'installPath' does not edit a field: it copies the whole SDK tree to the new\n" +
			"directory, backs the old one up to the desktop, re-runs the shim setup there\n" +
			"and deletes the original. It asks before doing any of that, and it is refused\n" +
			"while --install-path is in effect.",
		Args:              r.exactArgs(2),
		ValidArgsFunction: r.noFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSettingsSet(cmd, args[0], args[1])
		},
	}
}

func (r *runner) runSettingsSet(cmd *cobra.Command, key, rawValue string) error {
	if owned, ok := settingsOwnCommand[key]; ok {
		return r.usageError(cmd, fmt.Errorf("%s: %s", key,
			r.printer.T("cli.useCommand", map[string]string{"command": owned})))
	}
	if !slices.Contains(settingsKeys, key) {
		// The hint goes where resolveSdk puts the SDK list: to a human at a
		// terminal, never into a pipe or a JSON document.
		r.printer.Note("%s", r.printer.T("cli.availableKeys",
			map[string]string{"list": strings.Join(settingsKeys, ", ")}))
		return r.usageError(cmd, fmt.Errorf("unknown settings key %q", key))
	}
	if key == settingInstallPath {
		return r.runInstallPathMigration(cmd, rawValue)
	}

	value, err := canonicalSettingValue(key, rawValue)
	if err != nil {
		return r.usageError(cmd, err)
	}
	// Read-level, not write-level: settings.json is a whole-object write under
	// the operation lock and nothing here edits the shell rc or the registry
	// PATH. A write-level startup would also empty the temp directory and
	// delete stale rollback backups -- side effects a user changing their theme
	// did not ask for, and the same reason `svc clean tmp` boots read-only.
	core, err := r.readCore()
	if err != nil {
		return err
	}
	// The snapshot is read INSIDE the lock. settings.json is a whole-object
	// write, so a snapshot taken before the wait would resurrect every field
	// another svc process changed while this one was queued.
	err = r.withOpLock(core, func() error {
		updated, applyErr := applySetting(core.Home.SettingsSvc.Get(), key, value)
		if applyErr != nil {
			return applyErr
		}
		return core.Home.SettingsSvc.Save(updated)
	})
	if err != nil {
		return err
	}
	return r.reportSetting(SettingsWriteResult{
		Action: actionSettingSet,
		Key:    key,
		Value:  value,
	}, r.printer.T("settings.settingsSaved", nil), value)
}

// runInstallPathMigration is `settings set installPath`, the one key that moves
// files instead of writing a field. It is a port of SettingsPage's
// handleSaveInstallPath and handleResetInstallPath, which differ only in the
// confirmation they show and in where the target path comes from.
//
// This is also the one settings key that reaches writeCore: the migration
// re-runs EnsureSetup at the new location, which rewrites .svc.rc on Unix and
// the registry PATH on Windows. Every other key here touches settings.json and
// nothing else, and stays read-only for the reason `svc clean tmp` gives.
func (r *runner) runInstallPathMigration(cmd *cobra.Command, rawValue string) error {
	if r.opts.InstallPath != "" {
		return r.usageError(cmd, errInstallPathFlagActive)
	}
	target := strings.TrimSpace(rawValue)
	if target == "" {
		return r.usageError(cmd, fmt.Errorf("give an absolute path, or %q to restore the default", installPathDefaultKeyword))
	}
	reset := strings.EqualFold(target, installPathDefaultKeyword)
	core, err := r.writeCore()
	if err != nil {
		return err
	}
	if reset {
		target = core.Storage.GetDefaultInstallPath()
	}

	question, explanation := r.installPathWording(target, reset)
	if err := r.confirmMutation(question, explanation); err != nil {
		return err
	}
	// The lock is taken on the directory svc is using now, which is the one
	// about to be copied and deleted. MigrateInstallPath repoints the config at
	// the new directory while the lock is held and does not re-take it: a lock
	// is not re-entrant, and the old directory is the one a concurrent svc
	// would be working in.
	if err := r.withOpLock(core, func() error {
		return core.Storage.MigrateInstallPath(target)
	}); err != nil {
		return err
	}
	return r.reportSetting(SettingsWriteResult{
		Action: actionInstallPath,
		Key:    settingInstallPath,
		Value:  target,
	}, r.printer.T("settings.installPathSuccess", nil), target)
}

// installPathWording picks the confirmation the GUI shows for this migration:
// the reset button's pair when the target is the default, the save button's
// otherwise.
func (r *runner) installPathWording(target string, reset bool) (question, explanation string) {
	if reset {
		return r.printer.T("settings.installPathResetConfirm", nil),
			r.printer.T("settings.installPathResetConfirmDesc", map[string]string{"path": target})
	}
	return r.printer.T("settings.installPathConfirm", nil),
		r.printer.T("settings.installPathConfirmDesc", map[string]string{"path": target})
}

func (r *runner) newSettingsTokenCommand() *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Store or clear the GitHub token",
		Long: "Read a GitHub personal access token from stdin and store it, or clear the stored\n" +
			"one with --clear.\n" +
			"\n" +
			"The token is never an argument. An argument lands in the shell's history file,\n" +
			"in the process listing for as long as the command runs, and in any script that\n" +
			"logged its own command line. At a terminal the read is also unechoed, which is\n" +
			"the terminal's version of the GUI's password field; piped input is read as one\n" +
			"line, so `svc settings token < token.txt` works.\n" +
			"\n" +
			"A token raises the GitHub API rate limit from 60 to 5000 requests an hour,\n" +
			"which is what makes 'svc versions' usable on a shared network. It is stored\n" +
			"base64-encoded -- obfuscated, not encrypted -- so use one with minimal scopes.\n" +
			"\n" +
			"Neither the raw token nor the stored form is ever printed. The result reports\n" +
			"the masked value 'svc settings show' shows.",
		Args:              r.noArgs(),
		ValidArgsFunction: r.noFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runSettingsToken(cmd, clear)
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the stored token instead of setting one")
	return cmd
}

func (r *runner) runSettingsToken(cmd *cobra.Command, clear bool) error {
	core, err := r.readCore()
	if err != nil {
		return err
	}
	if clear {
		if err := r.confirmMutation(
			r.printer.T("settings.githubTokenClear", nil),
			r.printer.T("settings.githubTokenClearDesc", nil),
		); err != nil {
			return err
		}
		if err := r.withOpLock(core, func() error {
			return core.Home.SettingsSvc.SaveGithubToken("")
		}); err != nil {
			return err
		}
		return r.reportSetting(SettingsWriteResult{Action: actionTokenClear, Key: settingToken},
			r.printer.T("settings.githubTokenCleared", nil), "")
	}

	// Read BEFORE asking for the lock: a token is not something to hold in
	// memory while waiting on another process, and a run that cannot get one
	// should fail without having touched anything.
	token, err := r.readSecret(r.printer.T("cli.tokenPrompt", nil))
	if err != nil {
		return err
	}
	if token == "" {
		// The GUI's handleSaveToken refuses an empty draft with this exact
		// sentence rather than clearing the token by accident, and so does
		// this: clearing is --clear, which asks first.
		return r.usageError(cmd, errors.New(r.printer.T("settings.githubTokenEmpty", nil)))
	}
	if err := r.withOpLock(core, func() error {
		return core.Home.SettingsSvc.SaveGithubToken(token)
	}); err != nil {
		return err
	}
	masked := core.Home.SettingsSvc.Get().GitHubToken
	return r.reportSetting(SettingsWriteResult{
		Action: actionTokenSet,
		Key:    settingToken,
		Value:  masked,
	}, r.printer.T("settings.githubTokenSaved", nil), masked)
}

// readSecret reads one secret from stdin, unechoed when a human is typing it.
//
// term.ReadPassword needs a file descriptor, so it is only reachable when stdin
// really is a terminal; an injected reader -- a pipe, a test's buffer -- falls
// back to reading one line. The fallback is not a hole in the no-echo rule: a
// reader that is not a terminal is not echoing anything in the first place.
func (r *runner) readSecret(prompt string) (string, error) {
	atTerminal := r.opts.StdinTTY
	if atTerminal {
		fmt.Fprintf(r.in.Stderr, "%s ", prompt)
	}
	return readSecretLine(r.in.Stdin, atTerminal, r.in.Stderr)
}

// readSecretLine is readSecret's I/O, split out so the two input shapes can be
// tested without a pty. out receives the newline a password read consumes; it
// may be nil.
func readSecretLine(in io.Reader, stdinTTY bool, out io.Writer) (string, error) {
	if file, ok := in.(*os.File); ok && stdinTTY && term.IsTerminal(int(file.Fd())) {
		raw, err := term.ReadPassword(int(file.Fd()))
		if out != nil {
			_, _ = io.WriteString(out, "\n")
		}
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	return readOneLine(in)
}

// readOneLine reads up to the first newline. A stream that ends without one is
// still a value -- `printf ghp_x | svc settings token` has no trailing newline
// and must work -- and an empty stream is an empty value rather than an error,
// because "nothing was typed" is the case the caller reports with the GUI's own
// "token cannot be empty" sentence.
func readOneLine(in io.Reader) (string, error) {
	if in == nil {
		return "", nil
	}
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// reportSetting writes one settings change. Exactly one of the two shapes
// reaches stdout in any given mode, for the reason reportMutation gives.
//
// changed is the value rendered next to the key in the text modes, and is
// omitted when the change has no value worth repeating -- a cleared token, an
// endpoint reset to its default.
func (r *runner) reportSetting(result SettingsWriteResult, summary, changed string) error {
	if err := r.printer.JSON(result); err != nil {
		return err
	}
	r.printLine("%s", summary)
	if changed != "" {
		r.printer.KeyValue([]Pair{{Key: result.Key, Value: changed}})
	}
	return nil
}
