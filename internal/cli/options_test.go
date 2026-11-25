package cli

import (
	"testing"

	"svc/internal/cli/i18n"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		name string
		in   Inputs
		want Options
	}{
		{
			name: "a fresh interactive run",
			in:   Inputs{Environment: Environment{StdinTTY: true, StdoutTTY: true}},
			want: Options{
				Mode: ModeTTY, Color: true, Lang: i18n.DefaultLang,
				StdinTTY: true, StdoutTTY: true,
			},
		},
		{
			name: "a piped run",
			in:   Inputs{},
			want: Options{Mode: ModePlain, Color: false, Lang: i18n.DefaultLang},
		},
		{
			name: "--json overrides a terminal and colour",
			in: Inputs{
				JSON:        true,
				Environment: Environment{StdinTTY: true, StdoutTTY: true},
			},
			want: Options{
				Mode: ModeJSON, Color: false, Lang: i18n.DefaultLang,
				StdinTTY: true, StdoutTTY: true,
			},
		},
		{
			name: "quiet and yes pass straight through",
			in: Inputs{
				Quiet: true, Yes: true, InstallPath: "/tmp/svc-home",
				Environment: Environment{StdoutTTY: true},
			},
			want: Options{
				Mode: ModeTTY, Color: true, Quiet: true, Yes: true,
				InstallPath: "/tmp/svc-home", Lang: i18n.DefaultLang, StdoutTTY: true,
			},
		},
		{
			name: "--lang beats SVC_LANG beats settings.json",
			in: Inputs{
				Lang: "en",
				Environment: Environment{
					EnvLang: "zh", SettingsLang: "zh", StdoutTTY: true,
				},
			},
			want: Options{Mode: ModeTTY, Color: true, Lang: "en", StdoutTTY: true},
		},
		{
			name: "SVC_LANG beats settings.json",
			in: Inputs{
				Environment: Environment{EnvLang: "en", SettingsLang: "zh", StdoutTTY: true},
			},
			want: Options{Mode: ModeTTY, Color: true, Lang: "en", StdoutTTY: true},
		},
		{
			name: "settings.json alone decides the language",
			in: Inputs{
				Environment: Environment{SettingsLang: "en", StdoutTTY: true},
			},
			want: Options{Mode: ModeTTY, Color: true, Lang: "en", StdoutTTY: true},
		},
		{
			name: "NO_COLOR survives a terminal",
			in: Inputs{
				Environment: Environment{EnvNoColor: "1", StdinTTY: true, StdoutTTY: true},
			},
			want: Options{Mode: ModeTTY, Color: false, Lang: i18n.DefaultLang, StdinTTY: true, StdoutTTY: true},
		},
		{
			name: "--no-color survives CLICOLOR_FORCE",
			in: Inputs{
				NoColor: true,
				Environment: Environment{
					EnvForceColor: "1", StdinTTY: true, StdoutTTY: true,
				},
			},
			want: Options{Mode: ModeTTY, Color: false, Lang: i18n.DefaultLang, StdinTTY: true, StdoutTTY: true},
		},
		{
			name: "an unknown language falls back rather than failing",
			in: Inputs{
				Lang: "fr",
			},
			want: Options{Mode: ModePlain, Color: false, Lang: i18n.FallbackLang},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Resolve(c.in); got != c.want {
				t.Errorf("Resolve(%+v)\n  got  %+v\n  want %+v", c.in, got, c.want)
			}
		})
	}
}

// TestResolveIsPure pins the property the whole package leans on: the same
// inputs always produce the same options, and resolving twice changes nothing.
// If Resolve ever starts reading the environment itself, --json output would
// depend on how the command was launched and the table above would start
// passing or failing by accident.
func TestResolveIsPure(t *testing.T) {
	in := Inputs{
		JSON: true, Quiet: true, Lang: "en", InstallPath: "/opt/svc",
		Environment: Environment{EnvNoColor: "1", SettingsLang: "zh"},
	}
	first := Resolve(in)
	second := Resolve(in)
	if first != second {
		t.Errorf("Resolve is not deterministic:\n  %+v\n  %+v", first, second)
	}
	if first.Lang != "en" || first.Mode != ModeJSON || first.Color {
		t.Errorf("Resolve(%+v) = %+v", in, first)
	}
}

// TestResolveKeepsInstallPathVerbatim: the value reaches the config layer
// untouched, so a path that legitimately contains spaces or a trailing
// separator is not silently rewritten into something else.
func TestResolveKeepsInstallPathVerbatim(t *testing.T) {
	for _, path := range []string{"", "/tmp/my svc home", `C:\Program Files\svc\`, " /padded "} {
		got := Resolve(Inputs{InstallPath: path})
		if got.InstallPath != path {
			t.Errorf("InstallPath = %q, want %q verbatim", got.InstallPath, path)
		}
	}
}

func TestCollectEnvironment(t *testing.T) {
	t.Setenv(EnvLang, "en")
	t.Setenv(EnvNoColor, "1")
	t.Setenv(EnvForceColor, "0")

	got := CollectEnvironment("zh")
	if got.EnvLang != "en" {
		t.Errorf("EnvLang = %q, want %q", got.EnvLang, "en")
	}
	if got.EnvNoColor != "1" {
		t.Errorf("EnvNoColor = %q, want %q", got.EnvNoColor, "1")
	}
	if got.EnvForceColor != "0" {
		t.Errorf("EnvForceColor = %q, want %q", got.EnvForceColor, "0")
	}
	if got.SettingsLang != "zh" {
		t.Errorf("SettingsLang = %q, want the value passed in", got.SettingsLang)
	}
	// The terminal flags depend on how the test binary was started (a pipe
	// under `go test`, possibly a console under `go test -v`), so assert they
	// agree with the probes rather than asserting a value.
	if got.StdoutTTY != IsStdoutTTY() {
		t.Errorf("StdoutTTY = %v, IsStdoutTTY() = %v", got.StdoutTTY, IsStdoutTTY())
	}
	if got.StdinTTY != IsStdinTTY() {
		t.Errorf("StdinTTY = %v, IsStdinTTY() = %v", got.StdinTTY, IsStdinTTY())
	}
}

func TestCollectEnvironmentWithNothingSet(t *testing.T) {
	// t.Setenv with an empty value still marks the variable as set, so remove
	// them by hand through the same mechanism t.Setenv uses.
	t.Setenv(EnvLang, "")
	t.Setenv(EnvNoColor, "")
	t.Setenv(EnvForceColor, "")

	got := CollectEnvironment("")
	if got.EnvLang != "" || got.EnvNoColor != "" || got.EnvForceColor != "" || got.SettingsLang != "" {
		t.Errorf("CollectEnvironment(\"\") = %+v, want every string field empty", got)
	}
	// An empty chain must still resolve to the GUI's default language.
	if opts := Resolve(Inputs{Environment: got}); opts.Lang != i18n.DefaultLang {
		t.Errorf("Resolve over an empty environment picked language %q, want %q", opts.Lang, i18n.DefaultLang)
	}
}

func TestEnvVarNames(t *testing.T) {
	// These names are documented in the README and in `svc help`; renaming one
	// is a breaking change, so the rename has to be deliberate.
	want := map[string]string{
		"EnvLang":       "SVC_LANG",
		"EnvNoColor":    "NO_COLOR",
		"EnvForceColor": "CLICOLOR_FORCE",
	}
	got := map[string]string{
		"EnvLang": EnvLang, "EnvNoColor": EnvNoColor, "EnvForceColor": EnvForceColor,
	}
	for name, expected := range want {
		if got[name] != expected {
			t.Errorf("%s = %q, want %q", name, got[name], expected)
		}
	}
}

// catalogZh and catalogEn are stand-ins for the embedded frontend catalogs,
// which only the root package can see.
const (
	catalogZh = `{"progress":{"done":"安装配置完成！"},"errors":{"no-backup":"没有可用的备份"}}`
	catalogEn = `{"progress":{"done":"Installation complete!"},"errors":{"no-backup":"No backup available"}}`
)

func TestOptionsCatalog(t *testing.T) {
	catalogs := map[string][]byte{
		i18n.DefaultLang:  []byte(catalogZh),
		i18n.FallbackLang: []byte(catalogEn),
	}

	chinese := Resolve(Inputs{Environment: Environment{SettingsLang: "zh"}})
	catalog, err := chinese.Catalog(catalogs)
	if err != nil {
		t.Fatalf("Catalog for zh: %v", err)
	}
	if catalog.Lang() != "zh" {
		t.Errorf("Lang() = %q, want %q", catalog.Lang(), "zh")
	}
	if got := catalog.T("progress.done", nil); got != "安装配置完成！" {
		t.Errorf("T(progress.done) = %q", got)
	}

	english := Resolve(Inputs{Lang: "en"})
	catalog, err = english.Catalog(catalogs)
	if err != nil {
		t.Fatalf("Catalog for en: %v", err)
	}
	if got := catalog.T("progress.done", nil); got != "Installation complete!" {
		t.Errorf("T(progress.done) = %q", got)
	}

	// Without the mandatory English fallback the catalog cannot be built, and
	// the failure must be an error rather than a half-translated run.
	if _, err := Resolve(Inputs{}).Catalog(map[string][]byte{"zh": []byte(catalogZh)}); err == nil {
		t.Error("Catalog without the en fallback = nil error, want an error")
	}
}
