package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"svc/internal/cli/i18n"
	"svc/internal/sdk"
	"svc/internal/update"
)

// The guards in this file read the shipped sources rather than copies of them.
// A3 of the CLI contract says the terminal words progress exactly like the
// window does, and that only holds while the stage names and the catalog are
// the same artifacts both shells read. Paths are relative to this package,
// which is the working directory `go test` uses.

// repoRootRel walks from internal/cli up to the repository root.
const repoRootRel = "../../"

func repoFile(t *testing.T, rel string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRootRel, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return raw
}

func loadShippedCatalog(t *testing.T, lang string) *i18n.Catalog {
	t.Helper()
	catalog, err := i18n.New(map[string][]byte{
		i18n.DefaultLang:  repoFile(t, "frontend/src/i18n/zh.json"),
		i18n.FallbackLang: repoFile(t, "frontend/src/i18n/en.json"),
	}, lang)
	if err != nil {
		t.Fatalf("build the %s catalog from the shipped files: %v", lang, err)
	}
	return catalog
}

// stageDocRE finds the InstallProgress.Stage contract comment, which lists the
// stages the installer emits. It is the same expression i18n's drift guard uses.
var stageDocRE = regexp.MustCompile(`(?m)^\s*Stage\s+string\s+` + "`" + `json:"stage"` + "`" + `\s*//\s*(.+)$`)

// contractStages parses that comment into the stage names.
func contractStages(t *testing.T) []string {
	t.Helper()
	match := stageDocRE.FindStringSubmatch(string(repoFile(t, "internal/sdk/types.go")))
	if match == nil {
		t.Fatal("cannot find the InstallProgress.Stage contract comment in internal/sdk/types.go; stageDocRE has rotted")
	}
	var stages []string
	for _, part := range strings.Split(match[1], "|") {
		if stage := strings.Trim(strings.TrimSpace(part), `"`); stage != "" {
			stages = append(stages, stage)
		}
	}
	if len(stages) < 6 {
		t.Fatalf("parsed %d stages, want the 6 the installer emits: %v", len(stages), stages)
	}
	return stages
}

// cliStages is the set this package switches on. Adding a stage to the backend
// contract without adding it here means the CLI prints the raw message instead
// of a translated stage, so the two lists are pinned together.
func cliStages() []string {
	return []string{
		StageDownloading, StageExtracting, StageVerifying,
		StageConfiguringPath, StageDone, StageError,
	}
}

func TestStageConstantsMatchTheSdkContract(t *testing.T) {
	contract := contractStages(t)
	ours := cliStages()

	for _, stage := range contract {
		if !slices.Contains(ours, stage) {
			t.Errorf("internal/sdk emits stage %q but this package has no constant for it", stage)
		}
	}
	for _, stage := range ours {
		if !slices.Contains(contract, stage) {
			t.Errorf("stage constant %q is not in the internal/sdk contract comment", stage)
		}
	}
	if len(ours) != len(slices.Compact(slices.Sorted(slices.Values(ours)))) {
		t.Errorf("cliStages has duplicates: %v", ours)
	}
}

// TestProgressTextWithTheShippedCatalogs renders every contract stage through
// the real frontend catalogs. The failure this catches is the ugly one: a stage
// whose key was renamed in the GUI would print "progress.extracting" verbatim
// in the terminal while the window still looked fine.
func TestProgressTextWithTheShippedCatalogs(t *testing.T) {
	for _, lang := range []string{i18n.DefaultLang, i18n.FallbackLang} {
		catalog := loadShippedCatalog(t, lang)
		for _, stage := range contractStages(t) {
			progress := sdk.InstallProgress{Stage: stage, Message: "backend detail"}
			if stage == StageDownloading {
				progress.DownloadedBytes = 42
				progress.TotalBytes = 100
			}

			text := ProgressText(catalog, progress)
			if strings.TrimSpace(text) == "" {
				t.Errorf("%s: stage %q rendered as an empty string", lang, stage)
			}
			if strings.Contains(text, "progress.") {
				t.Errorf("%s: stage %q leaked a catalog key: %q", lang, stage, text)
			}
			if strings.HasPrefix(text, "[svc:") {
				t.Errorf("%s: stage %q leaked an apperr marker: %q", lang, stage, text)
			}

			switch stage {
			case StageDownloading:
				// The percentage must survive interpolation in both languages.
				if !strings.Contains(text, "42") {
					t.Errorf("%s: the download line lost its percentage: %q", lang, text)
				}
			case StageError:
				// The backend message is the payload of the error stage; a
				// catalog entry that drops {{error}} would swallow the reason.
				if !strings.Contains(text, "backend detail") {
					t.Errorf("%s: the error line lost the backend message: %q", lang, text)
				}
			default:
				if strings.Contains(text, "{{") {
					t.Errorf("%s: stage %q left a placeholder unfilled: %q", lang, stage, text)
				}
			}
		}
	}
}

// TestProgressTextUnknownStageWithTheShippedCatalogs pins the fallback: a stage
// the contract does not list yet must show the backend's own message rather
// than a key, so a new stage added server-side degrades into readable text.
func TestProgressTextUnknownStageWithTheShippedCatalogs(t *testing.T) {
	catalog := loadShippedCatalog(t, i18n.DefaultLang)
	progress := sdk.InstallProgress{Stage: "quarantining", Message: "waiting for the antivirus"}
	if got := ProgressText(catalog, progress); got != "waiting for the antivirus" {
		t.Errorf("ProgressText for an unlisted stage = %q, want the backend message", got)
	}
}

// TestFormatBytesIsUsedByTheProgressLine ties the byte formatter to the progress
// renderer with the shipped catalogs: the counters on a download line must be
// the strings FormatBytes produces, or the CLI would describe one size two ways.
func TestFormatBytesIsUsedByTheProgressLine(t *testing.T) {
	progress := event(StageDownloading, 19_200_000, 45_700_000)
	progress.SpeedBytesPerSec = 2_200_000

	text := BytesText(progress)
	for _, bytes := range []int64{19_200_000, 45_700_000, 2_200_000} {
		if !strings.Contains(text, FormatBytes(bytes)) {
			t.Errorf("BytesText = %q, which does not contain FormatBytes(%d) = %q", text, bytes, FormatBytes(bytes))
		}
	}
}

// updateTernaryRE finds one branch of the update modal's stage ternary:
// `downloadProgress.stage === '<stage>' ? t('<key>')`. The modal is formatted by
// prettier across several lines, so the whitespace between the comparison and
// the call is matched rather than spelled out.
var updateTernaryRE = regexp.MustCompile(`downloadProgress\.stage\s*===\s*'([^']+)'\s*\?\s*t\('([^']+)'\)`)

// TestUpdateProgressTextMatchesTheGUITernary pins UpdateProgressText to the
// window's own mapping, read out of SettingsPage.tsx rather than copied.
//
// The self-update stream reuses the install stream's stage tokens for different
// sentences -- `done` is "Download complete" here and "Installation complete!"
// there -- and the only thing keeping that from becoming a wrong word in a
// terminal is this test. A stage re-pointed at another key, or a key renamed,
// fails here instead of shipping.
func TestUpdateProgressTextMatchesTheGUITernary(t *testing.T) {
	page := string(repoFile(t, "frontend/src/components/Settings/SettingsPage.tsx"))
	matches := updateTernaryRE.FindAllStringSubmatch(page, -1)
	if len(matches) != 3 {
		t.Fatalf("found %d stage branches in the update modal's ternary, want the 3 the CLI mirrors; updateTernaryRE may have rotted:\n%s",
			len(matches), page)
	}

	branches := map[string]string{}
	for _, match := range matches {
		branches[match[1]] = match[2]
	}
	// The three stages the modal names are the three this package switches on.
	// A fourth branch in the window with no counterpart here would silently
	// print the backend's English message in a Chinese terminal.
	for _, stage := range []string{StageDownloading, StageVerifying, StageDone} {
		if _, ok := branches[stage]; !ok {
			t.Errorf("the update modal no longer has a branch for stage %q", stage)
		}
	}
	if len(branches) != 3 {
		t.Errorf("the update modal names %d stages (%v), want the 3 UpdateProgressText covers", len(branches), branches)
	}

	for _, lang := range []string{i18n.DefaultLang, i18n.FallbackLang} {
		catalog := loadShippedCatalog(t, lang)
		for stage, key := range branches {
			// No total: this is the shape in which the CLI uses the window's
			// own sentence for every stage, downloading included.
			progress := update.UpdateProgress{Stage: stage, Message: "backend detail"}
			got := UpdateProgressText(catalog, progress)
			want := catalog.T(key, nil)
			if got != want {
				t.Errorf("%s: stage %q rendered as %q, want the window's %s = %q", lang, stage, got, key, want)
			}
			if strings.TrimSpace(got) == "" || strings.Contains(got, "progress.") {
				t.Errorf("%s: stage %q leaked or lost its catalog key: %q", lang, stage, got)
			}
		}
	}

	// The three branches above are matched with no total, which is the shape in
	// which the CLI uses the window's sentence verbatim. With a total the
	// downloading line carries the percentage instead, through the install
	// line's own key: antd prints the number beside the modal's Progress bar and
	// a pipe has no bar to put it in. That key is shared with the GUI too, so it
	// is pinned here rather than merely asserted in progress_test.go.
	catalog := loadShippedCatalog(t, i18n.DefaultLang)
	withTotal := update.UpdateProgress{Stage: StageDownloading, Percent: 42, TotalBytes: 100}
	if got, want := UpdateProgressText(catalog, withTotal),
		catalog.T("progress.downloadingPercent", map[string]string{"percent": "42"}); got != want {
		t.Errorf("a download with a known total rendered as %q, want the shared percent key's %q", got, want)
	}
	if !strings.Contains(UpdateProgressText(catalog, withTotal), "42") {
		t.Errorf("the download line lost its percentage: %q", UpdateProgressText(catalog, withTotal))
	}

	// The ternary's else branch is downloadProgress.message, and the CLI keeps
	// it: a stage neither shell names yet must degrade into readable text.
	if got := UpdateProgressText(catalog, update.UpdateProgress{Stage: "unpacking", Message: "backend detail"}); got != "backend detail" {
		t.Errorf("UpdateProgressText for a stage the modal does not name = %q, want the backend message", got)
	}
	if got := UpdateProgressText(catalog, update.UpdateProgress{Stage: StageDone}); got == "backend detail" {
		t.Errorf("UpdateProgressText fell through to the empty message for a stage the modal does name")
	}
}
