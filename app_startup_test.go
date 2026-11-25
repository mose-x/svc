package main

import (
	"os"
	"strings"
	"testing"
)

// The GUI and the CLI must produce identical on-disk state for the same
// operation. The only thing that guarantees it is that both shells get their
// services from internal/appcore. These tests pin that down: they fail the
// moment assembly code creeps back into app.go, which is exactly how the two
// shells would start to drift (risk R1 in note/cli-design.md).

// readAppGo returns app.go as text. The guards below are deliberately textual:
// they check what the source says, not what a run happens to do.
func readAppGo(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatalf("read app.go: %v", err)
	}
	return string(raw)
}

// funcBody returns the full `func ... { ... }` text starting at sig, tracking
// brace depth so nested blocks don't end it early. app.go has no raw string
// literals with unbalanced braces, so depth counting is enough.
func funcBody(t *testing.T, src, sig string) string {
	t.Helper()
	from := strings.Index(src, sig)
	if from < 0 {
		t.Fatalf("app.go: cannot find %q", sig)
	}
	open := strings.Index(src[from:], "{")
	if open < 0 {
		t.Fatalf("app.go: no opening brace after %q", sig)
	}
	depth := 0
	for i := from + open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[from+open : i+1]
			}
		}
	}
	t.Fatalf("app.go: unbalanced braces after %q", sig)
	return ""
}

// stripLineComments drops `//` comments so a guard never trips on prose that
// merely mentions a constructor.
func stripLineComments(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "//"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

// assemblySteps lists every construction call and startup side effect that
// appcore.Bootstrap owns. None of them may reappear in a shell.
var assemblySteps = []string{
	"config.NewConfig(",
	"config.NewSettingsManager(",
	"shimmanager.New(",
	"pathmgr.NewPathManager(",
	"downloader.NewDownloader(",
	"proxy.New(",
	"settings.New(",
	"sdk.NewRegistry(",
	"storage.NewManager(",
	"update.NewUpdater(",
	"pkgmgr.New(",
	"installer.New(",
	"importer.New(",
	"sdk.InitMirrorsFile(",
	"sdk.InitVersionCacheDir(",
	"EnsureSetup(",
	"HealActiveVersions(",
	"RefreshStaleShims(",
	"CleanStaleBackups(",
	"SetSvcDir(",
}

func TestNewAppBuildsItsHomeThroughAppcore(t *testing.T) {
	body := stripLineComments(funcBody(t, readAppGo(t), "func NewApp("))

	if !strings.Contains(body, "appcore.OpenHome(") {
		t.Error("NewApp no longer builds its Home through appcore.OpenHome")
	}
	for _, step := range assemblySteps {
		if strings.Contains(body, step) {
			t.Errorf("NewApp re-acquired %q; appcore.NewHome owns the pre-Bootstrap services", step)
		}
	}
}

func TestStartupDelegatesAssemblyToAppcore(t *testing.T) {
	body := stripLineComments(funcBody(t, readAppGo(t), "func (a *App) startup("))

	if !strings.Contains(body, "a.home.Bootstrap(") {
		t.Error("startup no longer calls a.home.Bootstrap")
	}
	// LevelWrite, not LevelRead: the GUI has always run EnsureSetup plus the
	// temp-dir and stale-backup cleanups at launch.
	if !strings.Contains(body, "appcore.LevelWrite") {
		t.Error("startup must bootstrap at appcore.LevelWrite")
	}
	for _, step := range assemblySteps {
		if strings.Contains(body, step) {
			t.Errorf("startup re-acquired assembly step %q; move it into internal/appcore so the CLI gets it too", step)
		}
	}

	// The two GUI-only steps stay in app.go: they need a window or a dialog,
	// which the CLI must never trigger.
	for _, want := range []string{"migrate.RepairShortcutIcons()", "migrate.MaybeShowLegacyMigrationPrompt("} {
		if !strings.Contains(body, want) {
			t.Errorf("startup lost the GUI-only step %q", want)
		}
	}
}
