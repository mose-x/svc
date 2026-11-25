package i18n

import (
	"strings"
	"testing"

	"svc/internal/apperr"
)

// miniZh / miniEn are hand-written catalogs shaped like the real ones. The
// drift guards in catalog_drift_test.go run against the shipped files; these
// keep the behavioural tests independent of frontend edits.
const miniZh = `{
  "progress": {"downloading": "下载中... {{percent}}%", "done": "安装配置完成！"},
  "errors": {"unknown-sdk-type": "未知的 SDK 类型：{{sdk}}", "no-backup": "没有可用的备份", "exec-failed": "命令失败: {{cmd}}"}
}`

const miniEn = `{
  "progress": {"downloading": "Downloading... {{percent}}%", "done": "Installation complete!"},
  "errors": {"unknown-sdk-type": "Unknown SDK type: {{sdk}}", "no-backup": "No backup available", "exec-failed": "Command failed: {{cmd}}", "en-only-code": "Only in English"}
}`

func newMini(t *testing.T, lang string) *Catalog {
	t.Helper()
	c, err := New(map[string][]byte{"zh": []byte(miniZh), "en": []byte(miniEn)}, lang)
	if err != nil {
		t.Fatalf("New(%q): %v", lang, err)
	}
	return c
}

func TestNewRejectsBadInput(t *testing.T) {
	if _, err := New(nil, "zh"); err == nil {
		t.Error("New(nil) = nil error, want an error")
	}
	if _, err := New(map[string][]byte{"zh": []byte(miniZh)}, "zh"); err == nil {
		t.Error("New without the en fallback catalog = nil error, want an error")
	}
	if _, err := New(map[string][]byte{"en": []byte("{broken"), "zh": []byte(miniZh)}, "zh"); err == nil {
		t.Error("New with a corrupt catalog = nil error, want an error")
	}
	if _, err := New(map[string][]byte{"en": []byte(`["not","an","object"]`)}, "en"); err == nil {
		t.Error("New with a non-object catalog = nil error, want an error")
	}
}

func TestNewResolvesLang(t *testing.T) {
	cases := []struct {
		lang string
		want string
	}{
		{"zh", "zh"},
		{"en", "en"},
		{"zh-CN", "zh"},
		{" EN ", "en"},
		{"fr", "en"}, // unknown -> fallback, never an error
		{"", "zh"},   // no preference -> the GUI's default language
	}
	for _, c := range cases {
		catalogs := map[string][]byte{"zh": []byte(miniZh), "en": []byte(miniEn)}
		got, err := New(catalogs, c.lang)
		if err != nil {
			t.Fatalf("New(%q): %v", c.lang, err)
		}
		if got.Lang() != c.want {
			t.Errorf("New(%q).Lang() = %q, want %q", c.lang, got.Lang(), c.want)
		}
	}
}

func TestTDottedLookupAndInterpolation(t *testing.T) {
	c := newMini(t, "zh")

	got := c.T("progress.downloading", map[string]string{"percent": "42"})
	if want := "下载中... 42%"; got != want {
		t.Errorf("T = %q, want %q", got, want)
	}
	got = c.T("errors.unknown-sdk-type", map[string]string{"sdk": "nodejs"})
	if want := "未知的 SDK 类型：nodejs"; got != want {
		t.Errorf("T = %q, want %q", got, want)
	}
	if got := c.T("progress.done", nil); got != "安装配置完成！" {
		t.Errorf("T with no params = %q", got)
	}
}

func TestTFallsBackAndReportsMissing(t *testing.T) {
	c := newMini(t, "zh")

	// Present only in the en catalog: must resolve through the fallback
	// rather than returning the key, which is what i18next does.
	if got := c.T("errors.en-only-code", nil); got != "Only in English" {
		t.Errorf("fallback lookup = %q, want the English text", got)
	}
	if got := c.T("errors.nope", nil); got != "errors.nope" {
		t.Errorf("missing key = %q, want the key itself", got)
	}
	if got := c.T("", nil); got != "" {
		t.Errorf("empty key = %q, want empty", got)
	}
	// A group, not a message: counts as missing.
	if got := c.T("errors", nil); got != "errors" {
		t.Errorf("group key = %q, want the key itself", got)
	}
	// A path that walks through a leaf: counts as missing, must not panic.
	if got := c.T("progress.done.deeper", nil); got != "progress.done.deeper" {
		t.Errorf("path through a leaf = %q, want the key itself", got)
	}
}

func TestExists(t *testing.T) {
	c := newMini(t, "zh")
	for _, key := range []string{"errors.no-backup", "errors.en-only-code", "progress.done"} {
		if !c.Exists(key) {
			t.Errorf("Exists(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"errors.nope", "progress", "", "errors.no-backup.deeper"} {
		if c.Exists(key) {
			t.Errorf("Exists(%q) = true, want false", key)
		}
	}
}

func TestInterpolate(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		params map[string]string
		want   string
	}{
		{"single", "hi {{name}}", map[string]string{"name": "mose"}, "hi mose"},
		{"repeated", "{{a}} and {{a}}", map[string]string{"a": "x"}, "x and x"},
		{"multiple", "{{a}}-{{b}}", map[string]string{"a": "1", "b": "2"}, "1-2"},
		{"unknown left alone", "{{a}} {{b}}", map[string]string{"a": "1"}, "1 {{b}}"},
		{"whitespace tolerated", "{{ a }}", map[string]string{"a": "1"}, "1"},
		{"no escaping", "{{v}}", map[string]string{"v": `<b>&"x"</b>`}, `<b>&"x"</b>`},
		{"empty value", "{{a}}!", map[string]string{"a": ""}, "!"},
		{"nil params", "{{a}}", nil, "{{a}}"},
		{"no placeholder", "plain", map[string]string{"a": "1"}, "plain"},
		{"dotted name", "{{a.b}}", map[string]string{"a.b": "1"}, "1"},
		{"unterminated brace", "{{a", map[string]string{"a": "1"}, "{{a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Interpolate(c.text, c.params); got != c.want {
				t.Errorf("Interpolate(%q) = %q, want %q", c.text, got, c.want)
			}
		})
	}
}

func TestRenderErrorText(t *testing.T) {
	c := newMini(t, "zh")

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"marker with params", `[svc:unknown-sdk-type]{"sdk":"rust"}`, "未知的 SDK 类型：rust"},
		{"marker without params", `[svc:no-backup]`, "没有可用的备份"},
		{"empty json object", `[svc:no-backup]{}`, "没有可用的备份"},
		{"unknown code returns raw", `[svc:not-a-real-code]{"a":"b"}`, `[svc:not-a-real-code]{"a":"b"}`},
		{"malformed payload returns raw", `[svc:unknown-sdk-type]{oops`, `[svc:unknown-sdk-type]{oops`},
		{"payload of wrong type returns raw", `[svc:unknown-sdk-type]{"sdk":7}`, `[svc:unknown-sdk-type]{"sdk":7}`},
		{"plain text returns raw", "dial tcp: connection refused", "dial tcp: connection refused"},
		{"empty string", "", ""},
		{"marker must anchor at the start", `prefix [svc:no-backup]`, `prefix [svc:no-backup]`},
		{"uppercase code is not a marker", `[svc:No-Backup]`, `[svc:No-Backup]`},
		{"newline inside the payload", "[svc:exec-failed]{\"cmd\":\"cargo\",\n\"x\":\"y\"}", "命令失败: cargo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.RenderErrorText(tc.raw); got != tc.want {
				t.Errorf("RenderErrorText(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestRenderErrorMatchesApperrNew is the end-to-end half: markers produced by
// the real internal/apperr must translate, with no hand-written string in
// between.
func TestRenderErrorMatchesApperrNew(t *testing.T) {
	c := newMini(t, "zh")

	err := apperr.New(apperr.UnknownSdkType, map[string]string{"sdk": "perl"})
	if got, want := c.RenderError(err), "未知的 SDK 类型：perl"; got != want {
		t.Errorf("RenderError = %q, want %q", got, want)
	}
	err = apperr.New(apperr.NoBackup, nil)
	if got, want := c.RenderError(err), "没有可用的备份"; got != want {
		t.Errorf("RenderError = %q, want %q", got, want)
	}
	if got := c.RenderError(nil); got != "" {
		t.Errorf("RenderError(nil) = %q, want empty", got)
	}
	if got := c.RenderError(apperr.New(apperr.PathNotExist, map[string]string{"path": "/x"})); !strings.HasPrefix(got, "[svc:") {
		t.Errorf("a code missing from the catalog must come back raw, got %q", got)
	}
}

// TestRenderErrorInBothLanguages proves the same marker yields the language
// the user asked for, which is the whole point of A3.
func TestRenderErrorInBothLanguages(t *testing.T) {
	raw := `[svc:unknown-sdk-type]{"sdk":"go"}`
	if got := newMini(t, "zh").RenderErrorText(raw); got != "未知的 SDK 类型：go" {
		t.Errorf("zh = %q", got)
	}
	if got := newMini(t, "en").RenderErrorText(raw); got != "Unknown SDK type: go" {
		t.Errorf("en = %q", got)
	}
}

func TestNormalizeLang(t *testing.T) {
	cases := map[string]string{
		"zh": "zh", "ZH": "zh", "  en  ": "en", "zh-CN": "zh",
		"zh_Hans_CN": "zh", "en-US": "en", "": "", "   ": "", "-lead": "",
	}
	for in, want := range cases {
		if got := NormalizeLang(in); got != want {
			t.Errorf("NormalizeLang(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveLang(t *testing.T) {
	cases := []struct {
		name                      string
		flag, env, settings, want string
	}{
		{"all empty defaults to the GUI language", "", "", "", DefaultLang},
		{"settings wins over nothing", "", "", "en", "en"},
		{"env beats settings", "", "zh", "en", "zh"},
		{"flag beats env and settings", "en", "zh", "zh", "en"},
		{"region subtag is accepted", "zh-CN", "", "", "zh"},
		{"case and spaces are accepted", "", " EN ", "", "en"},
		{"unknown flag falls back rather than reaching past it", "fr", "", "zh", FallbackLang},
		{"unknown env falls back rather than reaching past it", "", "klingon", "zh", FallbackLang},
		{"unknown settings falls back", "", "", "fr", FallbackLang},
		{"blank flag is skipped, not treated as a choice", "", "", "en", "en"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ResolveLang(c.flag, c.env, c.settings); got != c.want {
				t.Errorf("ResolveLang(%q,%q,%q) = %q, want %q", c.flag, c.env, c.settings, got, c.want)
			}
		})
	}
}

// TestApperrPatternMatchesTheFrontend pins the marker grammar to the one the
// GUI uses. If either side changes alone, a code that translates in the GUI
// would print raw in the CLI (or vice versa).
func TestApperrPatternMatchesTheFrontend(t *testing.T) {
	if want := `^\[svc:([a-z0-9-]+)\](.*)$`; apperrPattern != want {
		t.Errorf("apperrPattern = %q, want %q", apperrPattern, want)
	}
	if got := svcErrorRE.String(); got != "(?s)"+apperrPattern {
		t.Errorf("svcErrorRE = %q, want the (?s) form of apperrPattern", got)
	}
	// The TS source is checked in catalog_drift_test.go, which can read the
	// frontend tree; this assertion keeps the two halves of the pair honest.
	if !strings.Contains(svcErrorRE.String(), "[a-z0-9-]") {
		t.Error("the code character class must stay [a-z0-9-]")
	}
}

func TestErrorCode(t *testing.T) {
	cases := []struct {
		raw      string
		wantCode string
		wantOK   bool
	}{
		{`[svc:no-backup]`, "no-backup", true},
		{`[svc:unknown-sdk-type]{"sdk":"go"}`, "unknown-sdk-type", true},
		{"[svc:exec-failed]{\"cmd\":\"x\",\n\"y\":\"z\"}", "exec-failed", true},
		{`[svc:not-a-real-code]`, "not-a-real-code", true},
		{`prefix [svc:no-backup]`, "", false},
		{`[svc:No-Backup]`, "", false},
		{`[svc:]`, "", false},
		{"plain text", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		code, ok := ErrorCode(c.raw)
		if ok != c.wantOK || code != c.wantCode {
			t.Errorf("ErrorCode(%q) = (%q, %v), want (%q, %v)", c.raw, code, ok, c.wantCode, c.wantOK)
		}
	}
}

// TestErrorCodeAgreesWithRenderError pins the two marker readers together: a
// string RenderErrorText translates must be one ErrorCode recognises, and vice
// versa. A shell picks its exit code from ErrorCode and its message from
// RenderErrorText, so a disagreement would mean a translated message exiting
// with the generic failure code.
func TestErrorCodeAgreesWithRenderError(t *testing.T) {
	c := newMini(t, "zh")
	translatable := []string{
		`[svc:unknown-sdk-type]{"sdk":"go"}`,
		`[svc:no-backup]`,
		`[svc:exec-failed]{"cmd":"cargo"}`,
	}
	for _, raw := range translatable {
		if _, ok := ErrorCode(raw); !ok {
			t.Errorf("ErrorCode did not recognise %q", raw)
		}
		if got := c.RenderErrorText(raw); got == raw {
			t.Errorf("RenderErrorText left %q untranslated", raw)
		}
	}
	// Not markers: neither half may treat them as one.
	for _, raw := range []string{`prefix [svc:no-backup]`, "dial tcp: refused", ""} {
		if _, ok := ErrorCode(raw); ok {
			t.Errorf("ErrorCode treated %q as a marker", raw)
		}
		if got := c.RenderErrorText(raw); got != raw {
			t.Errorf("RenderErrorText altered the non-marker %q into %q", raw, got)
		}
	}
}
