package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"svc/internal/apperr"
	"svc/internal/config"
	"svc/internal/sdk"
)

// hermeticRegistry builds the real registry against a temporary HOME with a
// scrubbed PATH. Nothing here resolves versions, so no host binary is ever
// probed; the registry is needed only for the set of identifiers it knows,
// which is the same set the GUI offers.
func hermeticRegistry(t *testing.T) *sdk.Registry {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	cfg, err := config.NewConfig()
	if err != nil {
		t.Fatalf("config.NewConfig: %v", err)
	}
	cfg.SetHomeDir(home)
	cfg.SetSvcDir(filepath.Join(home, ".svc"))
	return sdk.NewRegistry(cfg, nil)
}

// TestResolveSdkTypeAcceptsTheSpellingsAHumanTypes covers the three accepted
// forms. The GUI never needs this because it hands the backend the identifier
// the backend gave it; a command line is typed by somebody reading a screen.
func TestResolveSdkTypeAcceptsTheSpellingsAHumanTypes(t *testing.T) {
	registry := hermeticRegistry(t)

	cases := []struct {
		name  string
		input string
		want  sdk.SdkType
	}{
		{"the identifier verbatim", "nodejs", sdk.NodeJS},
		{"the identifier in any case", "NodeJS", sdk.NodeJS},
		{"the identifier shouted", "NODEJS", sdk.NodeJS},
		{"surrounding space is trimmed", "  nodejs\n", sdk.NodeJS},
		{"the display name", "Node.js", sdk.NodeJS},
		{"the display name in lower case", "node.js", sdk.NodeJS},
		{"a short identifier", "go", sdk.Golang},
		{"a short display name", "Go", sdk.Golang},
		{"a display name that is not an identifier", ".NET", sdk.DotNet},
		{"that same name in lower case", ".net", sdk.DotNet},
		{"and its identifier", "dotnet", sdk.DotNet},
		{"an identifier that equals its display name", "jdk", sdk.JDK},
		{"the last registered SDK", "dart", sdk.Dart},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveSdkType(registry, c.input)
			if err != nil {
				t.Fatalf("ResolveSdkType(%q) = %v, want it accepted", c.input, err)
			}
			if got != c.want {
				t.Errorf("ResolveSdkType(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

// TestResolveSdkTypeRejectsEverythingElse pins the boundary. Prefix matching in
// particular is refused on purpose: "d" names both dart and dotnet, and a
// command that silently picks one is worse than one that asks.
func TestResolveSdkTypeRejectsEverythingElse(t *testing.T) {
	registry := hermeticRegistry(t)

	cases := []struct {
		name  string
		input string
	}{
		{"nothing at all", ""},
		{"only whitespace", "   "},
		{"an unknown name", "klingon"},
		{"a prefix of one SDK", "node"},
		{"a prefix of two SDKs", "d"},
		{"a single letter", "j"},
		{"an identifier with a version glued on", "nodejs/20"},
		{"two identifiers", "nodejs go"},
		{"a command the SDK provides", "npm"},
		{"a path", "./nodejs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveSdkType(registry, c.input)
			if err == nil {
				t.Fatalf("ResolveSdkType(%q) = %q, want it refused", c.input, got)
			}
			if got != "" {
				t.Errorf("ResolveSdkType(%q) returned %q alongside the error, want the zero value", c.input, got)
			}
			// The failure has to name what was typed, or the user has no idea
			// which argument was wrong.
			if !strings.Contains(err.Error(), strings.TrimSpace(c.input)) && strings.TrimSpace(c.input) != "" {
				t.Errorf("the error %q does not name the input %q", err, c.input)
			}
			// And it has to be the service layer's marker, which is what maps
			// it to exit 3 rather than a generic failure.
			if code := ExitCodeForError(err); code != ExitNotFound {
				t.Errorf("ExitCodeForError = %d, want %d", code, ExitNotFound)
			}
		})
	}
}

// TestResolveSdkTypeWithoutARegistry covers the command layer reaching for a
// registry before the run was resolved. That is a programming error rather than
// a user mistake, so it reports the uninitialized marker and exits 1.
func TestResolveSdkTypeWithoutARegistry(t *testing.T) {
	got, err := ResolveSdkType(nil, "nodejs")
	if err == nil {
		t.Fatalf("ResolveSdkType(nil) = %q, want an error", got)
	}
	if code := ExitCodeForError(err); code != ExitError {
		t.Errorf("ExitCodeForError = %d, want %d", code, ExitError)
	}
	if !strings.Contains(err.Error(), string(apperr.AppNotInitialized)) {
		t.Errorf("the error %q does not carry the app-not-initialized marker", err)
	}
}

// TestResolveSdkTypeRendersThroughTheCatalog ties the marker to the wording the
// window shows for the same mistake, in both languages.
func TestResolveSdkTypeRendersThroughTheCatalog(t *testing.T) {
	registry := hermeticRegistry(t)
	_, err := ResolveSdkType(registry, "klingon")
	if err == nil {
		t.Fatal("ResolveSdkType accepted an unknown name")
	}

	zh := loadShippedCatalog(t, "zh").RenderErrorText(err.Error())
	en := loadShippedCatalog(t, "en").RenderErrorText(err.Error())
	if zh == err.Error() || en == err.Error() {
		t.Errorf("the unknown-SDK marker did not translate: zh=%q en=%q", zh, en)
	}
	for lang, text := range map[string]string{"zh": zh, "en": en} {
		if !strings.Contains(text, "klingon") {
			t.Errorf("%s: the rendered text %q lost the name that was typed", lang, text)
		}
		if strings.Contains(text, "[svc:") {
			t.Errorf("%s: the rendered text %q leaked the marker", lang, text)
		}
	}
	if zh == en {
		t.Errorf("zh and en rendered identically (%q); the language is not taking effect", zh)
	}
}

// TestSdkTypeNamesEnumeratesTheRegistry pins the list backing the "did you
// mean" hint and shell completion to the registry itself, so neither can drift
// into accepting a name ResolveSdkType refuses.
func TestSdkTypeNamesEnumeratesTheRegistry(t *testing.T) {
	registry := hermeticRegistry(t)

	names := SdkTypeNames(registry)
	if len(names) != len(sdk.AllSdkTypes()) {
		t.Fatalf("got %d names, want the %d registered SDKs: %v", len(names), len(sdk.AllSdkTypes()), names)
	}
	for i, kind := range sdk.AllSdkTypes() {
		if names[i] != string(kind) {
			t.Errorf("name %d = %q, want %q (registry order must be display order)", i, names[i], kind)
		}
		// Every name must round-trip through the resolver, which is the whole
		// reason the hint lists these and not a hand-written set.
		if got, err := ResolveSdkType(registry, names[i]); err != nil || got != kind {
			t.Errorf("ResolveSdkType(%q) = %q, %v; want %q and no error", names[i], got, err, kind)
		}
	}
	if got := SdkTypeNames(nil); got != nil {
		t.Errorf("SdkTypeNames(nil) = %v, want nil", got)
	}
}

func TestSdkTypeHintListsEveryAcceptedName(t *testing.T) {
	registry := hermeticRegistry(t)

	hint := SdkTypeHint(registry)
	if hint == "" {
		t.Fatal("SdkTypeHint returned nothing for a populated registry")
	}
	for _, name := range SdkTypeNames(registry) {
		if !strings.Contains(hint, name) {
			t.Errorf("the hint %q does not mention %q", hint, name)
		}
	}
	if want := strings.Join(SdkTypeNames(registry), ", "); hint != want {
		t.Errorf("SdkTypeHint = %q, want %q", hint, want)
	}
	if got := SdkTypeHint(nil); got != "" {
		t.Errorf("SdkTypeHint(nil) = %q, want it empty", got)
	}
}
