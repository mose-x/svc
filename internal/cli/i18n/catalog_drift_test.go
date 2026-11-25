package i18n

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"svc/internal/apperr"
)

// The guards in this file read the shipped frontend tree instead of a copy of
// it. That is the point: A3 of the CLI contract says the CLI words things
// exactly like the UI, which only holds while there is ONE catalog. A test
// asserting against a snapshot would stay green while the real thing drifted.
// Paths are relative to this package, which is the working directory `go test`
// uses.

// repoRootRel walks from internal/cli/i18n up to the repository root.
const repoRootRel = "../../../"

func repoFile(t *testing.T, rel string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRootRel, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return raw
}

func catalogRelPath(lang string) string {
	return filepath.Join("frontend/src/i18n", lang+".json")
}

// loadShippedCatalogs builds a Catalog out of the real zh/en files.
func loadShippedCatalogs(t *testing.T, lang string) *Catalog {
	t.Helper()
	catalogs := map[string][]byte{}
	for _, l := range []string{DefaultLang, FallbackLang} {
		catalogs[l] = repoFile(t, catalogRelPath(l))
	}
	c, err := New(catalogs, lang)
	if err != nil {
		t.Fatalf("New from the shipped catalogs (lang=%q): %v", lang, err)
	}
	return c
}

// apperrCodeRE matches one Code constant in internal/apperr's const block.
// Parsing the source rather than asking apperr for a list is deliberate: a
// code added to the block is caught even if nobody registered it anywhere.
var apperrCodeRE = regexp.MustCompile(`(?m)^\s*[A-Za-z0-9_]+\s+Code\s+=\s+"([a-z0-9-]+)"`)

func apperrCodes(t *testing.T) []string {
	t.Helper()
	matches := apperrCodeRE.FindAllStringSubmatch(string(repoFile(t, "internal/apperr/apperr.go")), -1)
	if len(matches) == 0 {
		t.Fatal("no apperr Code constants matched; apperrCodeRE has rotted")
	}
	codes := make([]string, len(matches))
	seen := make(map[string]bool, len(matches))
	for i, m := range matches {
		codes[i] = m[1]
		if seen[m[1]] {
			t.Errorf("apperr declares the code %q twice", m[1])
		}
		seen[m[1]] = true
	}
	sort.Strings(codes)
	return codes
}

// catalogGroupKeys returns the dotted keys of one top-level catalog group.
func catalogGroupKeys(t *testing.T, lang, group string) []string {
	t.Helper()
	var tree map[string]json.RawMessage
	if err := json.Unmarshal(repoFile(t, catalogRelPath(lang)), &tree); err != nil {
		t.Fatalf("parse the %s catalog: %v", lang, err)
	}
	raw, ok := tree[group]
	if !ok {
		t.Fatalf("the %s catalog has no %q group", lang, group)
	}
	var leaves map[string]string
	if err := json.Unmarshal(raw, &leaves); err != nil {
		t.Fatalf("the %q group of the %s catalog is not a flat string map: %v", group, lang, err)
	}
	keys := make([]string, 0, len(leaves))
	for key := range leaves {
		keys = append(keys, group+"."+key)
	}
	sort.Strings(keys)
	return keys
}

// flattenCatalog collects every leaf string under its dotted key.
func flattenCatalog(t *testing.T, lang string) map[string]string {
	t.Helper()
	var tree map[string]any
	if err := json.Unmarshal(repoFile(t, catalogRelPath(lang)), &tree); err != nil {
		t.Fatalf("parse the %s catalog: %v", lang, err)
	}
	out := map[string]string{}
	flattenInto(t, "", tree, out)
	if len(out) == 0 {
		t.Fatalf("the %s catalog flattened to nothing", lang)
	}
	return out
}

func flattenInto(t *testing.T, prefix string, node any, out map[string]string) {
	t.Helper()
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			next := key
			if prefix != "" {
				next = prefix + "." + key
			}
			flattenInto(t, next, child, out)
		}
	case string:
		out[prefix] = value
	default:
		t.Errorf("catalog key %q holds a %T, not a string; i18n cannot render it", prefix, node)
	}
}

// placeholderNames lists the distinct {{...}} names a message uses, sorted.
// It reuses the package's own placeholderRE so the guard checks exactly the
// syntax the interpolator accepts.
//
// The result is a set, not a multiset: a translation is free to repeat a
// placeholder for readability ("{{name}} requires {{sdk}}. Please install
// {{sdk}} first." is the shipped English for errors.need-sdk while the Chinese
// says it once). What must not differ is WHICH names a message consumes -- a
// dropped one prints literal braces, an invented one never resolves.
func placeholderNames(text string) []string {
	matches := placeholderRE.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool, len(matches))
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	sort.Strings(names)
	return names
}

// TestShippedCatalogsLoad is the smoke test for the embed path: the two JSON
// files the GUI ships must parse into a working Catalog for both languages.
func TestShippedCatalogsLoad(t *testing.T) {
	for _, lang := range []string{DefaultLang, FallbackLang} {
		c := loadShippedCatalogs(t, lang)
		if c.Lang() != lang {
			t.Errorf("New(lang=%q).Lang() = %q, want %q", lang, c.Lang(), lang)
		}
		if !c.Exists("app.title") && !c.Exists("sidebar.title") {
			t.Errorf("the %s catalog has no app/sidebar group; the layout changed", lang)
		}
	}
	// An empty preference must land on the GUI's own default, not on English.
	if got := loadShippedCatalogs(t, "").Lang(); got != DefaultLang {
		t.Errorf("New(lang=\"\").Lang() = %q, want %q", got, DefaultLang)
	}
}

// TestEveryApperrCodeIsTranslatableInBothLanguages is the drift guard the CLI
// design requires: adding a code to internal/apperr without adding it to both
// catalogs must fail here, not surface later as a raw "[svc:xyz]" marker in
// front of a user. It also fails on the reverse -- a catalog entry with no
// code behind it is dead weight that will rot.
func TestEveryApperrCodeIsTranslatableInBothLanguages(t *testing.T) {
	codes := apperrCodes(t)
	if len(codes) < 30 {
		t.Errorf("parsed only %d apperr codes; the CLI design records 33, so apperrCodeRE has probably rotted", len(codes))
	}

	for _, lang := range []string{DefaultLang, FallbackLang} {
		c := loadShippedCatalogs(t, lang)
		codeSet := make(map[string]bool, len(codes))

		// Membership is checked against THIS language's own file, not through
		// c.Exists: Exists falls back to English, so a zh-only deletion would
		// still "resolve" and the guard would miss exactly the case it exists
		// for -- one language silently degrading to the other.
		present := make(map[string]bool)
		keys := catalogGroupKeys(t, lang, "errors")
		for _, key := range keys {
			present[key] = true
		}

		for _, code := range codes {
			codeSet[code] = true
			key := "errors." + code
			if !present[key] {
				t.Errorf("the %s catalog has no %q; add it to frontend/src/i18n/%s.json, otherwise that language degrades to the fallback", lang, key, lang)
				continue
			}
			if !c.Exists(key) {
				t.Errorf("%q is in the %s file but the catalog cannot resolve it", key, lang)
				continue
			}
			raw := apperr.New(apperr.Code(code), nil).Error()
			got := c.RenderErrorText(raw)
			if got == raw {
				t.Errorf("%s: rendering %q returned the marker unchanged", lang, raw)
			}
			if strings.TrimSpace(got) == "" {
				t.Errorf("%s: rendering %q produced empty text", lang, raw)
			}
		}

		for _, key := range keys {
			if !codeSet[strings.TrimPrefix(key, "errors.")] {
				t.Errorf("the %s catalog carries %q but internal/apperr declares no such code", lang, key)
			}
		}
	}
}

// TestApperrPatternMatchesTheFrontendSource pins the marker grammar to the
// expression the GUI uses. If either side changes on its own, a code that
// translates in the GUI prints raw in the CLI, or vice versa.
func TestApperrPatternMatchesTheFrontendSource(t *testing.T) {
	src := string(repoFile(t, "frontend/src/utils/error.ts"))

	// The TS literal is the Go pattern wrapped in slashes with the `s` flag,
	// which is Go's (?s).
	if want := "/" + apperrPattern + "/s"; !strings.Contains(src, want) {
		t.Errorf("frontend/src/utils/error.ts no longer contains %s; apperrPattern in i18n.go must be updated to match", want)
	}
	// error.ts translates only when the key resolves, and returns the raw text
	// otherwise. RenderErrorText follows the same two rules; if the frontend
	// stops doing so, the shells diverge.
	for _, want := range []string{`errors.${code}`, "i18n.exists(key)", "return raw"} {
		if !strings.Contains(src, want) {
			t.Errorf("frontend/src/utils/error.ts no longer contains %q; keep RenderErrorText aligned with it", want)
		}
	}
}

// stageDocRE reads the documented stage list off the InstallProgress.Stage
// field in internal/sdk/types.go. That comment is the contract both the
// frontend and the CLI progress renderer switch on.
var stageDocRE = regexp.MustCompile(`(?m)^\s*Stage\s+string\s+` + "`" + `json:"stage"` + "`" + `\s*//\s*(.+)$`)

// TestProgressGroupCoversEveryInstallStage guards the other half of A3: a
// progress stage the installer emits but the catalog cannot name would make the
// CLI print "progress.xyz" mid-install.
func TestProgressGroupCoversEveryInstallStage(t *testing.T) {
	doc := stageDocRE.FindStringSubmatch(string(repoFile(t, "internal/sdk/types.go")))
	if doc == nil {
		t.Fatal("cannot find the InstallProgress.Stage contract comment in internal/sdk/types.go; stageDocRE has rotted")
	}

	var stages []string
	for _, part := range strings.Split(doc[1], "|") {
		if stage := strings.Trim(strings.TrimSpace(part), `"`); stage != "" {
			stages = append(stages, stage)
		}
	}
	if len(stages) < 6 {
		t.Fatalf("parsed %d stages from the contract comment, want the 6 the installer emits: %v", len(stages), stages)
	}

	for _, lang := range []string{DefaultLang, FallbackLang} {
		c := loadShippedCatalogs(t, lang)
		for _, stage := range stages {
			key := "progress." + stage
			if !c.Exists(key) {
				t.Errorf("the %s catalog has no %q for install stage %q", lang, key, stage)
				continue
			}
			if strings.TrimSpace(c.T(key, nil)) == "" {
				t.Errorf("%s: %q is empty", lang, key)
			}
		}
	}
}

// TestShippedCatalogsAreInSync keeps the two languages structurally identical:
// same key set, no empty values, and the same {{placeholders}} per message. A
// translator dropping a placeholder is the failure mode that silently prints
// literal braces to a user.
func TestShippedCatalogsAreInSync(t *testing.T) {
	zh := flattenCatalog(t, DefaultLang)
	en := flattenCatalog(t, FallbackLang)

	for key, zhText := range zh {
		enText, ok := en[key]
		if !ok {
			t.Errorf("%q is in the %s catalog but missing from %s", key, DefaultLang, FallbackLang)
			continue
		}
		if strings.TrimSpace(zhText) == "" {
			t.Errorf("%q is empty in the %s catalog", key, DefaultLang)
		}
		if strings.TrimSpace(enText) == "" {
			t.Errorf("%q is empty in the %s catalog", key, FallbackLang)
		}
		zhNames, enNames := placeholderNames(zhText), placeholderNames(enText)
		if !slices.Equal(zhNames, enNames) {
			t.Errorf("%q consumes placeholders %v in %s but %v in %s; the two must agree, since a dropped one prints literal braces and an invented one never resolves",
				key, zhNames, DefaultLang, enNames, FallbackLang)
		}
	}
	for key := range en {
		if _, ok := zh[key]; !ok {
			t.Errorf("%q is in the %s catalog but missing from %s", key, FallbackLang, DefaultLang)
		}
	}
}

// TestRenderErrorWithTheShippedCatalogs exercises the real files end to end for
// a marker with parameters, in both languages, so a regression in lookup or
// interpolation cannot hide behind the mini catalogs.
func TestRenderErrorWithTheShippedCatalogs(t *testing.T) {
	raw := apperr.New(apperr.UnknownSdkType, map[string]string{"sdk": "nodejs"}).Error()

	zhText := loadShippedCatalogs(t, DefaultLang).RenderErrorText(raw)
	enText := loadShippedCatalogs(t, FallbackLang).RenderErrorText(raw)

	for lang, got := range map[string]string{DefaultLang: zhText, FallbackLang: enText} {
		if got == raw {
			t.Errorf("%s: a shipped code failed to translate: %q", lang, got)
		}
		if strings.Contains(got, "{{") {
			t.Errorf("%s: an unfilled placeholder survived rendering: %q", lang, got)
		}
		if !strings.Contains(got, "nodejs") {
			t.Errorf("%s: the sdk param did not reach the message: %q", lang, got)
		}
	}
	if zhText == enText {
		t.Errorf("zh and en rendered identically (%q); the language selection is not taking effect", zhText)
	}
}
