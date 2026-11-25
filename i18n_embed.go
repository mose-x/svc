package main

import (
	_ "embed"

	"svc/internal/cli/i18n"
)

// The GUI's i18n catalogs are the single source of truth for user-facing
// wording, so the CLI embeds those very files rather than keeping a second
// copy that could drift. A Go package can only embed files inside its own
// directory and only the root package can see frontend/, which is why the
// embed lives here and i18n.New takes the bytes.
//
// These are source files, not build output: unlike frontend/dist they are
// tracked in git and unaffected by `npm run build`, so `go build` needs no
// frontend toolchain to resolve them.

//go:embed frontend/src/i18n/zh.json
var i18nZhJSON []byte

//go:embed frontend/src/i18n/en.json
var i18nEnJSON []byte

// cliCatalogs returns the embedded catalogs keyed by language tag, ready to
// hand to i18n.New.
func cliCatalogs() map[string][]byte {
	return map[string][]byte{
		i18n.DefaultLang:  i18nZhJSON,
		i18n.FallbackLang: i18nEnJSON,
	}
}
