// Package i18n renders user-facing CLI text from the very same catalogs the
// GUI uses.
//
// "The CLI reads exactly like the UI" only holds if both shells translate from
// one source of truth, so this package ships no strings of its own. It parses
// frontend/src/i18n/{zh,en}.json and implements the small slice of i18next
// those files actually rely on: dotted key lookup, {{param}} interpolation
// without HTML escaping, and "a key that resolves nowhere is returned as-is so
// a typo stays visible instead of going blank".
//
// The catalogs are embedded by the root package and handed in as bytes: a Go
// package can only embed files inside its own directory, and only the root
// package can see frontend/.
//
// A Catalog is read-only once built and safe for concurrent use.
package i18n

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	// DefaultLang is what settings.json carries when the user never picked a
	// language: config.NewSettingsManager initialises Language to "zh", and
	// frontend/src/i18n/index.ts sets lng:"zh". The CLI must start in the same
	// language as the GUI, not in English.
	DefaultLang = "zh"
	// FallbackLang mirrors i18next's fallbackLng:"en" in the frontend config.
	FallbackLang = "en"
)

// knownLangs is the set of catalogs the GUI ships (frontend/src/i18n/*.json).
// Anything else resolves to FallbackLang rather than failing: the CLI must not
// refuse to run because someone exported SVC_LANG=fr.
var knownLangs = map[string]bool{DefaultLang: true, FallbackLang: true}

// apperrPattern is the marker expression shared with the frontend. The TS
// source is /^\[svc:([a-z0-9-]+)\](.*)$/s -- the `s` flag lets the JSON
// payload span newlines, which is Go's (?s). Keep the two identical; a drift
// test pins this constant against frontend/src/utils/error.ts.
const apperrPattern = `^\[svc:([a-z0-9-]+)\](.*)$`

var svcErrorRE = regexp.MustCompile("(?s)" + apperrPattern)

// placeholderRE matches i18next's {{name}}. Surrounding whitespace is
// tolerated (i18next trims it); the shipped catalogs do not use it, but a
// hand-edited one must not degrade into literal braces.
var placeholderRE = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.\-]+?)\s*\}\}`)

// Catalog is a resolved view over the frontend i18n catalogs.
type Catalog struct {
	lang  string
	trees map[string]map[string]any
}

// New builds a Catalog from raw catalog JSON keyed by language tag. An empty
// lang yields DefaultLang and a lang naming no supplied catalog yields
// FallbackLang, so a caller can pass the raw result of ResolveLang straight
// through. The FallbackLang catalog is mandatory: without it a missing key
// would have nowhere to go.
func New(catalogs map[string][]byte, lang string) (*Catalog, error) {
	if len(catalogs) == 0 {
		return nil, fmt.Errorf("i18n: no catalogs supplied")
	}
	trees := make(map[string]map[string]any, len(catalogs))
	for name, raw := range catalogs {
		var tree map[string]any
		if err := json.Unmarshal(raw, &tree); err != nil {
			return nil, fmt.Errorf("i18n: parse the %s catalog: %w", name, err)
		}
		trees[name] = tree
	}
	if _, ok := trees[FallbackLang]; !ok {
		return nil, fmt.Errorf("i18n: the %q catalog is required as the fallback", FallbackLang)
	}

	resolved := NormalizeLang(lang)
	if resolved == "" {
		// No preference expressed anywhere: speak the language a fresh GUI
		// install speaks, not the fallback.
		resolved = DefaultLang
	}
	if _, ok := trees[resolved]; !ok {
		resolved = FallbackLang
	}
	return &Catalog{lang: resolved, trees: trees}, nil
}

// Lang reports the language this catalog renders in.
func (c *Catalog) Lang() string { return c.lang }

// T returns the message for a dotted key with its {{param}} placeholders
// filled in. A key absent from the active language is looked up in the
// fallback language; a key absent from both yields the key itself.
func (c *Catalog) T(key string, params map[string]string) string {
	text, ok := c.lookup(key)
	if !ok {
		return key
	}
	return Interpolate(text, params)
}

// Exists reports whether key resolves in the active or the fallback catalog.
// frontend/src/utils/error.ts gates on exactly this before translating an
// apperr marker, and RenderErrorText must agree with it.
func (c *Catalog) Exists(key string) bool {
	_, ok := c.lookup(key)
	return ok
}

// lookup resolves a dotted key against the active language, then the fallback.
// A non-string leaf counts as missing: only leaf strings are messages.
func (c *Catalog) lookup(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	if text, ok := lookupKey(c.trees[c.lang], key); ok {
		return text, true
	}
	if c.lang != FallbackLang {
		return lookupKey(c.trees[FallbackLang], key)
	}
	return "", false
}

// lookupKey walks one catalog tree by dotted path.
func lookupKey(tree map[string]any, key string) (string, bool) {
	var node any = tree
	for _, part := range strings.Split(key, ".") {
		group, ok := node.(map[string]any)
		if !ok {
			return "", false
		}
		if node, ok = group[part]; !ok {
			return "", false
		}
	}
	text, ok := node.(string)
	return text, ok
}

// Interpolate fills {{name}} placeholders from params.
//
// Values go in verbatim. The frontend initialises i18next with
// escapeValue:false, so escaping here would make the CLI and the GUI print
// different text for the same error -- the one thing this package exists to
// prevent. Placeholders with no matching param are left untouched, as
// i18next does.
func Interpolate(text string, params map[string]string) string {
	if len(params) == 0 || !strings.Contains(text, "{{") {
		return text
	}
	return placeholderRE.ReplaceAllStringFunc(text, func(match string) string {
		name := placeholderRE.FindStringSubmatch(match)[1]
		if value, ok := params[name]; ok {
			return value
		}
		return match
	})
}

// ErrorCode extracts the apperr code from a marker, reporting whether raw is a
// marker at all. Shells need the code itself, not only the rendered text: it is
// what selects the process exit code. It applies the same grammar as
// RenderErrorText, so the two can never disagree about what counts as a marker.
func ErrorCode(raw string) (string, bool) {
	match := svcErrorRE.FindStringSubmatch(raw)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// RenderError translates a backend error into user-facing text.
//
// The backend has no localization layer: internal/apperr emits a
// "[svc:<code>]{json}" marker and each shell translates it. Anything that is
// not a well-formed marker -- a wrapped low-level cause, an already rendered
// string, a nil error -- comes back unchanged (empty for nil), so no
// diagnostic detail is ever swallowed.
func (c *Catalog) RenderError(err error) string {
	if err == nil {
		return ""
	}
	return c.RenderErrorText(err.Error())
}

// RenderErrorText is RenderError for an error string that has already been
// extracted, e.g. one carried inside a JSON payload or a wrapped cause chain.
// It is the Go counterpart of errMsg() in frontend/src/utils/error.ts and
// follows the same four exits: no marker, unknown code, unparseable params,
// translated.
func (c *Catalog) RenderErrorText(raw string) string {
	match := svcErrorRE.FindStringSubmatch(raw)
	if match == nil {
		return raw
	}
	key := "errors." + match[1]
	if !c.Exists(key) {
		return raw
	}
	var params map[string]string
	if payload := match[2]; payload != "" {
		if err := json.Unmarshal([]byte(payload), &params); err != nil {
			return raw
		}
	}
	return c.T(key, params)
}

// NormalizeLang lower-cases a tag and drops any region or script subtag, so
// "zh-CN", " ZH_cn " and "zh" all resolve to "zh".
func NormalizeLang(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		tag = tag[:i]
	}
	return tag
}

// ResolveLang applies the documented precedence: the --lang flag beats the
// SVC_LANG environment variable beats the language stored in settings.json.
// An empty chain yields DefaultLang, so a fresh install speaks the same
// language as the GUI. The first non-empty value wins even when it names no
// shipped catalog -- it then yields FallbackLang, matching i18next's
// fallbackLng rather than silently reaching past an explicit user choice.
func ResolveLang(flag, env, settings string) string {
	for _, candidate := range []string{flag, env, settings} {
		lang := NormalizeLang(candidate)
		if lang == "" {
			continue
		}
		if knownLangs[lang] {
			return lang
		}
		return FallbackLang
	}
	return DefaultLang
}
