package cli

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"svc/internal/appcore"
	"svc/internal/logger"
)

// `svc doctor` is the one command that answers "is svc itself working?" rather
// than "what is installed?".
//
// The GUI has no counterpart: it repairs silently at startup, through the same
// appcore.Bootstrap this command runs, and reports nothing unless a panel
// happens to show a version that no longer exists. That silence is fine for a
// window a user is looking at and useless over ssh, which is why the checks
// here are printed rather than only performed.
//
// The command bootstraps at write level on purpose. LevelWrite is the level
// that installs the shim binary, rewrites .svc.rc and re-creates the shims of
// any SDK whose recorded layout went stale, so a doctor run does not only find
// the two problems svc can fix by itself -- it fixes them, and says so. What it
// cannot fix it reports as broken and exits 1, which is the number a script
// needs in order to stop.
//
// One limitation is structural and worth stating rather than hiding: the layout
// heal and the shim refresh run at EVERY bootstrap level, including the
// read-only one, so by the time a command can look, they have already run. The
// shim binary is the only check whose before-and-after this command can
// observe, because reading its "before" needs no services at all -- just the
// home, which option resolution already opened. The other checks therefore
// report the state the repair pass left behind, and a `repaired` status is
// reserved for the one that can prove it changed something.

// DoctorCheck is one line of the report.
type DoctorCheck struct {
	// Name is a stable machine token, one of the checkName constants. The
	// sentence a human reads is rendered from the catalog at print time.
	Name string `json:"name"`
	// Status is one of the doctorState constants.
	Status string `json:"status"`
	// Detail explains a status that is not ok: the command that fixes a broken
	// one, or what the repair pass did to a repaired one.
	Detail string `json:"detail,omitempty"`
}

// DoctorResult is what `svc doctor --json` reports.
type DoctorResult struct {
	Checks []DoctorCheck `json:"checks"`
	// Repaired counts what the write-level startup fixed during this run.
	Repaired int `json:"repaired"`
	// Broken counts what still needs the user. Any nonzero value exits 1.
	Broken int `json:"broken"`
}

// The DoctorCheck.Status values.
const (
	doctorOK       = "ok"
	doctorRepaired = "repaired"
	doctorBroken   = "broken"
)

// The DoctorCheck.Name values.
const (
	checkShimBinary      = "shim-binary"
	checkShimsOnPath     = "shims-on-path"
	checkActiveVersions  = "active-versions"
	checkShimsConfig     = "shims-config"
	checkSystemConflicts = "system-conflicts"
)

func (r *runner) newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check svc's own installation and repair what it can",
		Long: "Run every check svc can make about itself, repair what a startup can repair,\n" +
			"and report the rest.\n" +
			"\n" +
			"The checks, in order:\n" +
			"\n" +
			"  shim-binary       the shim executable exists in the shims directory\n" +
			"  shims-on-path     that directory is on PATH, so the shims can be found\n" +
			"  active-versions   every active version still has its directory on disk\n" +
			"  shims-config      every active SDK has an entry in shims.json\n" +
			"  system-conflicts  no system-level PATH or environment entry shadows a shim\n" +
			"\n" +
			"A missing shim binary and a stale shims.json are repaired by the startup this\n" +
			"command runs, and reported as repaired. A directory that is not on PATH, an\n" +
			"active version whose files are gone, and a conflict with a copy of the same SDK\n" +
			"installed outside svc are reported as broken: the first two need a terminal\n" +
			"restart or a 'svc switch', and the last one needs the other installation\n" +
			"removed by hand.\n" +
			"\n" +
			"Exit code is 0 when nothing is broken and 1 when something is.",
		Args:              r.noArgs(),
		ValidArgsFunction: r.noFileCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.runDoctor()
		},
	}
}

func (r *runner) runDoctor() error {
	// The shim binary's state before the repair pass is the one fact doctor
	// needs that Bootstrap destroys, so it is read off the home before the
	// services exist. Option resolution has normally already run -- it is in
	// PersistentPreRunE -- and the guard keeps this from opening a second home
	// when it has.
	if r.home == nil {
		if err := r.resolveOptions(); err != nil {
			return err
		}
	}
	shimBinaryBefore := r.home.ShimMgr.ShimBinaryInstalled()

	core, err := r.writeCore()
	if err != nil {
		return err
	}

	// The order is the failure cascade: without the shim binary nothing else
	// can work, and without the shims directory on PATH the binary is
	// unreachable even when it exists. It is also the order the report is read
	// in, so a user meets the cause before its consequences.
	result := DoctorResult{Checks: []DoctorCheck{
		r.checkShimBinary(core, shimBinaryBefore),
		r.checkShimsOnPath(core),
		r.checkActiveVersions(core),
		r.checkShimsConfig(core),
		r.checkSystemConflicts(core),
	}}
	for _, check := range result.Checks {
		switch check.Status {
		case doctorRepaired:
			result.Repaired++
		case doctorBroken:
			result.Broken++
		}
	}

	if err := r.printer.JSON(result); err != nil {
		return err
	}
	r.printDoctor(result)
	if result.Broken > 0 {
		// The sentence the run failed with is the summary itself, rendered from
		// the catalog rather than assembled in English: reportError prints what
		// an error says, and this error is a result the user has to act on.
		return errors.New(r.printer.T("cli.doctorIssues", map[string]string{
			"broken":   strconv.Itoa(result.Broken),
			"repaired": strconv.Itoa(result.Repaired),
		}))
	}
	r.printLine("%s", r.printer.T("cli.doctorAllOk", nil))
	return nil
}

// checkShimBinary reports the one thing the write-level startup repairs: the
// shim executable every command in the shims directory is a link to. before is
// its state ahead of that startup, which is what makes "repaired" a fact here
// rather than an assumption.
func (r *runner) checkShimBinary(core *appcore.Core, before bool) DoctorCheck {
	check := DoctorCheck{Name: checkShimBinary, Status: doctorOK}
	if !core.Home.ShimMgr.ShimBinaryInstalled() {
		check.Status = doctorBroken
		check.Detail = r.printer.T("cli.doctorRunInit", nil)
		return check
	}
	if !before {
		check.Status = doctorRepaired
		check.Detail = r.printer.T("cli.doctorShimReinstalled", nil)
	}
	return check
}

// checkShimsOnPath reports whether the shims directory is reachable at all. A
// missing entry is not something svc fixes by itself on Unix -- the rc line is
// sourced by the shell, so an already-open terminal will not see it -- which is
// why the detail is the GUI's own "reopen your terminal" sentence.
func (r *runner) checkShimsOnPath(core *appcore.Core) DoctorCheck {
	check := DoctorCheck{Name: checkShimsOnPath, Status: doctorOK}
	if !pathHasEntry(core.Home.Cfg.ShimsDir(), os.Getenv("PATH"), runtime.GOOS) {
		check.Status = doctorBroken
		check.Detail = r.printer.T("detail.reopenTerminalHint", nil)
	}
	return check
}

// checkActiveVersions reports every SDK whose recorded active version has no
// directory on disk. This is the state the GUI's home page marks with
// detail.needsSwitch, and the reason a shim resolves to nothing: config.json
// names a version the tree no longer holds, usually because it was deleted
// behind svc's back.
//
// The check is a stat per SDK rather than a full status scan on purpose.
// storage.GetAllSdkStatus also probes each SDK's own version command, which
// means a subprocess per SDK and a scan-progress stream on stderr -- far too
// much machinery for a question that is entirely about directories existing.
func (r *runner) checkActiveVersions(core *appcore.Core) DoctorCheck {
	check := DoctorCheck{Name: checkActiveVersions, Status: doctorOK}
	var missing []string
	for _, fetcher := range core.Registry.All() {
		sdkType := string(fetcher.Type())
		version := core.Home.Cfg.GetActiveVersion(sdkType)
		if version == "" {
			continue
		}
		if _, err := os.Stat(core.Home.Cfg.SdkVersionDir(sdkType, version)); err != nil {
			missing = append(missing, sdkType+" "+version)
		}
	}
	if len(missing) > 0 {
		check.Status = doctorBroken
		check.Detail = strings.Join(missing, ", ") + ": " + r.printer.T("detail.needsSwitch", nil)
	}
	return check
}

// checkShimsConfig reports every active SDK that has no entry in shims.json.
// Without one the shim binary cannot resolve the SDK's bin directories, so its
// commands are missing from PATH even though the version directory is there.
// The write-level startup's shim refresh usually fixes this, so what reaches
// the report is a case it could not.
func (r *runner) checkShimsConfig(core *appcore.Core) DoctorCheck {
	check := DoctorCheck{Name: checkShimsConfig, Status: doctorOK}
	configured := core.Home.ShimMgr.GetShimConfig().SdkTypes
	var missing []string
	for _, fetcher := range core.Registry.All() {
		sdkType := string(fetcher.Type())
		if core.Home.Cfg.GetActiveVersion(sdkType) == "" {
			continue
		}
		if _, ok := configured[sdkType]; !ok {
			missing = append(missing, sdkType)
		}
	}
	if len(missing) > 0 {
		check.Status = doctorBroken
		check.Detail = strings.Join(missing, ", ") + ": " + r.printer.T("cli.doctorRunSwitch", nil)
	}
	return check
}

// checkSystemConflicts reports the PATH and environment entries competing with
// svc's shims -- the check behind the GUI's conflict modal. These are not
// svc's to remove: they belong to another version manager or to the OS, and the
// remedy is the same one the modal gives, which is why the detail is its title
// followed by the entries it found.
//
// A failed check for one SDK is logged and skipped rather than failing the run,
// mirroring the GUI's `.catch(() => {})`: a diagnostic that cannot be computed
// must not break the report it was contributing a line to.
func (r *runner) checkSystemConflicts(core *appcore.Core) DoctorCheck {
	check := DoctorCheck{Name: checkSystemConflicts, Status: doctorOK}
	var found []string
	for _, fetcher := range core.Registry.All() {
		sdkType := string(fetcher.Type())
		if core.Home.Cfg.GetActiveVersion(sdkType) == "" {
			continue
		}
		conflicts, err := core.Installer.CheckSystemConflicts(sdkType)
		if err != nil {
			logger.Warn("Doctor: system conflict check for %s failed: %v", sdkType, err)
			continue
		}
		found = append(found, conflicts...)
	}
	if len(found) > 0 {
		check.Status = doctorBroken
		check.Detail = r.printer.T("detail.systemConflictTitle", nil) + ": " + strings.Join(found, ", ")
	}
	return check
}

func (r *runner) printDoctor(result DoctorResult) {
	table := Table{
		Columns: []Column{
			{Header: r.printer.T("cli.columnCheck", nil)},
			{Header: r.printer.T("cli.columnState", nil)},
			{Header: r.printer.T("cli.columnDetail", nil)},
		},
	}
	for _, check := range result.Checks {
		table.Rows = append(table.Rows, []string{
			r.doctorCheckLabel(check.Name),
			r.doctorStateLabel(check.Status),
			check.Detail,
		})
		// Only a broken row is coloured. Colouring every healthy one green
		// buries the line the user came for, which is the opposite of what a
		// diagnostic is for.
		tone := TonePlain
		if check.Status == doctorBroken {
			tone = ToneError
		}
		table.RowTones = append(table.RowTones, tone)
	}
	r.printer.Table(table)
}

// doctorCheckLabel renders a check's machine token as the sentence a human
// reads. The switch is spelled out rather than built from the token because the
// catalog-key guard in output_guard_test.go can only see a literal: a key
// assembled by concatenation would be invisible to it and unresolved at
// runtime, printing "cli.checkShimBinary" in a column with nothing failing.
func (r *runner) doctorCheckLabel(name string) string {
	switch name {
	case checkShimBinary:
		return r.printer.T("cli.checkShimBinary", nil)
	case checkShimsOnPath:
		return r.printer.T("cli.checkShimsOnPath", nil)
	case checkActiveVersions:
		return r.printer.T("cli.checkActiveVersions", nil)
	case checkShimsConfig:
		return r.printer.T("cli.checkShimsConfig", nil)
	case checkSystemConflicts:
		return r.printer.T("cli.checkSystemConflicts", nil)
	default:
		return name
	}
}

func (r *runner) doctorStateLabel(status string) string {
	switch status {
	case doctorOK:
		return r.printer.T("cli.stateOk", nil)
	case doctorRepaired:
		return r.printer.T("cli.stateRepaired", nil)
	default:
		return r.printer.T("cli.stateBroken", nil)
	}
}

// pathHasEntry reports whether dir appears in a PATH-style list.
//
// The OS is a parameter rather than a runtime.GOOS read because the two rules
// that differ between platforms -- the separator and whether case matters --
// are exactly the two a test cannot exercise on a single host otherwise, and
// AGENTS.md requires the platform-dependent logic to be testable on all three.
func pathHasEntry(dir, pathList, goos string) bool {
	windows := goos == "windows"
	separator := ":"
	if windows {
		separator = ";"
	}
	want := normalizePathEntry(dir, windows)
	if want == "" {
		return false
	}
	for _, entry := range strings.Split(pathList, separator) {
		got := normalizePathEntry(entry, windows)
		if got == "" {
			continue
		}
		if windows {
			// Windows paths are case-insensitive, and a PATH assembled from the
			// registry routinely disagrees with itself about the case of
			// %USERPROFILE%.
			if strings.EqualFold(got, want) {
				return true
			}
			continue
		}
		if got == want {
			return true
		}
	}
	return false
}

// normalizePathEntry trims what a PATH entry carries around a path rather than
// in it: surrounding space, the quotes a Windows PATH sometimes wraps an entry
// in, and a trailing separator, which would make "C:\x\shims\" fail to match
// "C:\x\shims". It deliberately does not call filepath.Clean: that resolves
// against the HOST's separator rules, so a Windows entry inspected by a test
// running on Linux would be cleaned by the wrong rules.
func normalizePathEntry(entry string, windows bool) string {
	entry = strings.TrimSpace(entry)
	entry = strings.Trim(entry, `"`)
	for len(entry) > 1 {
		last := entry[len(entry)-1]
		if last != '/' && !(windows && last == '\\') {
			break
		}
		entry = entry[:len(entry)-1]
	}
	return entry
}
