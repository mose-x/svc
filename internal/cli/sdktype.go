package cli

import (
	"strings"

	"svc/internal/apperr"
	"svc/internal/sdk"
)

// ResolveSdkType maps what the user typed onto an SdkType the registry knows.
//
// The GUI never needs this: it hands the backend the exact string it was given
// by the backend. A command line is typed by a human, and the human reads
// "Node.js" on the screen and types `node`, or reads the directory name and
// types `NodeJS`. Rejecting everything but the internal identifier would make
// every command a guessing game whose only feedback is an error.
//
// The accepted spellings are, in order: the identifier verbatim, the identifier
// in any case, and the display name in any case. Nothing else -- in particular
// no prefix matching, because `d` is both `dart` and `dotnet` and a command that
// silently picks one is worse than a command that asks.
//
// Resolution goes through the registry rather than a hardcoded list so a new
// SDK fetcher is accepted by every command the moment it is registered, with
// no second table to update.
func ResolveSdkType(registry *sdk.Registry, input string) (sdk.SdkType, error) {
	if registry == nil {
		return "", apperr.New(apperr.AppNotInitialized, nil)
	}
	unknown := func() (sdk.SdkType, error) {
		return "", apperr.New(apperr.UnknownSdkType, map[string]string{"sdk": input})
	}

	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return unknown()
	}
	if fetcher := registry.Get(sdk.SdkType(trimmed)); fetcher != nil {
		return fetcher.Type(), nil
	}

	lowered := strings.ToLower(trimmed)
	for _, fetcher := range registry.All() {
		kind := fetcher.Type()
		if strings.ToLower(string(kind)) == lowered {
			return kind, nil
		}
		if strings.ToLower(sdk.SdkDisplayName(kind)) == lowered {
			return kind, nil
		}
	}
	return unknown()
}

// SdkTypeNames lists every registered identifier in registry order. It backs
// the "did you mean" hint and, from P5, shell completion -- both of which have
// to enumerate the same set ResolveSdkType accepts, so neither can drift.
func SdkTypeNames(registry *sdk.Registry) []string {
	if registry == nil {
		return nil
	}
	fetchers := registry.All()
	names := make([]string, 0, len(fetchers))
	for _, fetcher := range fetchers {
		names = append(names, string(fetcher.Type()))
	}
	return names
}

// SdkTypeHint is the line appended to an unknown-SDK failure in an interactive
// terminal, listing what would have been accepted. It goes to stderr through
// Printer.Note, never into the result stream and never in JSON mode: a machine
// consumer asked for a list of versions, not a tutorial.
func SdkTypeHint(registry *sdk.Registry) string {
	names := SdkTypeNames(registry)
	if len(names) == 0 {
		return ""
	}
	return strings.Join(names, ", ")
}
