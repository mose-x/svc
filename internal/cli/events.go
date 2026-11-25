package cli

import (
	"svc/internal/logger"
	"svc/internal/sdk"
	"svc/internal/update"
)

// The event names the service layer emits over wailsrt.Runtime.EventsEmit.
// They are the contract between the backend and whichever shell is listening:
// the GUI subscribes to the same four names in App.tsx, DetailPanel.tsx and
// SettingsPage.tsx. They are named here rather than spelled inline so a rename
// is a one-line change that events_guard_test.go can pin against the frontend.
const (
	// EventInstallProgress carries sdk.InstallProgress: the install pipeline.
	EventInstallProgress = "install:progress"
	// EventScanProgress carries sdk.ScanProgress: which SDK a full status
	// scan is currently checking.
	EventScanProgress = "sdk:scan-progress"
	// EventVersionsRefreshed tells the GUI that a background version refresh
	// landed and the list can be swapped in silently.
	EventVersionsRefreshed = "install:versions-refreshed"
	// EventUpdateProgress carries update.UpdateProgress: svc's own self-update.
	EventUpdateProgress = "update:progress"
)

// EventHandlers receives the service events, typed. A nil field means "this
// shell does not render that event", which is normal: a read-only command has
// no progress to show and does not want a renderer wired up.
type EventHandlers struct {
	// InstallProgress receives install:progress.
	InstallProgress func(sdk.InstallProgress)
	// ScanProgress receives sdk:scan-progress.
	ScanProgress func(sdk.ScanProgress)
	// UpdateProgress receives update:progress.
	UpdateProgress func(update.UpdateProgress)
}

// RouteEvent dispatches one event by name and reports whether the name is
// known.
//
// Unknown names are logged rather than ignored silently: a new event in the
// service layer that no shell renders is exactly the kind of drift that leaves
// a terminal silent while the window shows a progress bar. A known name whose
// payload has the wrong type, or whose handler is nil, is dropped -- panicking
// inside a service callback would take down an install that was otherwise
// succeeding, and progress is decoration.
//
// install:versions-refreshed is known and deliberately not handled. It tells a
// listener that a background refresh finished; the CLI refreshes synchronously
// (or through a detached child that outlives it), so there is no in-process
// background refresh to report.
func RouteEvent(handlers EventHandlers, eventName string, payload any) bool {
	switch eventName {
	case EventInstallProgress:
		if progress, ok := payload.(sdk.InstallProgress); ok && handlers.InstallProgress != nil {
			handlers.InstallProgress(progress)
		}
	case EventScanProgress:
		if progress, ok := payload.(sdk.ScanProgress); ok && handlers.ScanProgress != nil {
			handlers.ScanProgress(progress)
		}
	case EventUpdateProgress:
		if progress, ok := payload.(update.UpdateProgress); ok && handlers.UpdateProgress != nil {
			handlers.UpdateProgress(progress)
		}
	case EventVersionsRefreshed:
	default:
		logger.Info("CLI ignored an unknown service event %q", eventName)
		return false
	}
	return true
}
