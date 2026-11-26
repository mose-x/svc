package cli

import (
	"os"

	"svc/internal/cli/i18n"
)

// Environment variables the CLI reads. They are declared here rather than
// inline so a grep for one name finds every place it is honoured.
const (
	// EnvLang overrides the language saved in settings.json.
	EnvLang = "SVC_LANG"
	// EnvNoColor disables colour by mere presence, per no-color.org.
	EnvNoColor = "NO_COLOR"
	// EnvForceColor forces colour on when set to anything but "0".
	EnvForceColor = "CLICOLOR_FORCE"
)

// Environment is the half of a run's inputs that comes from the process
// rather than from the command line. Splitting it out keeps Resolve pure: the
// command layer collects an Environment once, hands it to every subcommand,
// and nothing else ever calls os.Getenv or asks the kernel about a descriptor.
type Environment struct {
	// EnvLang is the raw $SVC_LANG value.
	EnvLang string
	// EnvNoColor is the raw $NO_COLOR value.
	EnvNoColor string
	// EnvForceColor is the raw $CLICOLOR_FORCE value.
	EnvForceColor string
	// SettingsLang is the language stored in settings.json, i.e. what the GUI
	// is currently showing. The CLI starts in the same language as the window.
	SettingsLang string
	// StdinTTY is whether a human can answer a prompt.
	StdinTTY bool
	// StdoutTTY is whether results go to a terminal.
	StdoutTTY bool
}

// Inputs is the complete raw input to Resolve: the global flags plus the
// environment they are read against.
type Inputs struct {
	// JSON is --json: emit machine-readable results.
	JSON bool
	// Quiet is --quiet: suppress everything but errors and results.
	Quiet bool
	// NoColor is --no-color.
	NoColor bool
	// Yes is --yes: skip confirmation prompts.
	Yes bool
	// NoWait is --no-wait: fail immediately instead of waiting for another
	// svc operation to release the lock.
	NoWait bool
	// Lang is --lang.
	Lang string
	// InstallPath is --install-path, a one-run override of the SVC home. It is
	// never written back to settings.json.
	InstallPath string

	Environment
}

// Options is a resolved run configuration. Every field is final: nothing
// downstream re-reads a flag, an environment variable or a terminal, so the
// three output modes cannot drift apart within one command.
type Options struct {
	// Mode is the output contract in force.
	Mode Mode
	// Color reports whether ANSI sequences may be emitted.
	Color bool
	// Quiet reports whether progress and informational output is suppressed.
	Quiet bool
	// Lang is the resolved catalog language, always a language the GUI ships.
	Lang string
	// Yes reports whether confirmation prompts are pre-answered.
	Yes bool
	// NoWait reports whether a contended operation lock fails the run instead
	// of blocking it.
	NoWait bool
	// InstallPath overrides the SVC home for this run only. Empty means "use
	// the configured home".
	InstallPath string
	// StdinTTY reports whether a prompt could be answered.
	StdinTTY bool
	// StdoutTTY reports whether stdout is a terminal.
	StdoutTTY bool
}

// Resolve turns raw inputs into a run configuration. It performs no I/O, so the
// precedence rules it encodes can be asserted directly instead of being probed
// through a subprocess with a pty attached.
func Resolve(in Inputs) Options {
	return Options{
		Mode: ResolveMode(in.JSON, in.StdoutTTY),
		Color: ResolveColor(ColorInputs{
			NoColorFlag: in.NoColor,
			JSONMode:    in.JSON,
			StdoutTTY:   in.StdoutTTY,
			NoColorEnv:  in.EnvNoColor,
			ForceEnv:    in.EnvForceColor,
		}),
		Quiet:       in.Quiet,
		Lang:        i18n.ResolveLang(in.Lang, in.EnvLang, in.SettingsLang),
		Yes:         in.Yes,
		NoWait:      in.NoWait,
		InstallPath: in.InstallPath,
		StdinTTY:    in.StdinTTY,
		StdoutTTY:   in.StdoutTTY,
	}
}

// CollectEnvironment reads the process environment and the two terminal
// descriptors. It is the only function here that reads an environment variable,
// and it reaches the terminal only through IsStdinTTY/IsStdoutTTY, so the rest
// of the package stays testable without a pty.
//
// settingsLang is passed in rather than loaded here: reading settings.json
// means opening the SVC home, which the command layer already does and which
// --install-path may be redirecting.
func CollectEnvironment(settingsLang string) Environment {
	return Environment{
		EnvLang:       os.Getenv(EnvLang),
		EnvNoColor:    os.Getenv(EnvNoColor),
		EnvForceColor: os.Getenv(EnvForceColor),
		SettingsLang:  settingsLang,
		StdinTTY:      IsStdinTTY(),
		StdoutTTY:     IsStdoutTTY(),
	}
}

// Catalog builds the message catalog for this run's language. The bytes come
// from the caller because only the root package can see frontend/: a Go package
// may embed files inside its own directory and nothing below it.
func (o Options) Catalog(catalogs map[string][]byte) (*i18n.Catalog, error) {
	return i18n.New(catalogs, o.Lang)
}
