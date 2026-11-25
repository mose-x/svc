package cli

import (
	"testing"

	"svc/internal/sdk"
	"svc/internal/update"
)

// TestRouteEventDispatchesEveryKnownName covers the happy path: each event the
// service layer emits reaches the handler registered for it, with the payload
// it carried, and RouteEvent reports the name as known.
func TestRouteEventDispatchesEveryKnownName(t *testing.T) {
	install := sdk.InstallProgress{Stage: StageDownloading, DownloadedBytes: 10, TotalBytes: 100}
	scan := sdk.ScanProgress{DisplayName: "Go", Index: 3, Total: 14}
	selfUpdate := update.UpdateProgress{Stage: "downloading", Percent: 40}

	var gotInstall sdk.InstallProgress
	var gotScan sdk.ScanProgress
	var gotUpdate update.UpdateProgress
	handlers := EventHandlers{
		InstallProgress: func(p sdk.InstallProgress) { gotInstall = p },
		ScanProgress:    func(p sdk.ScanProgress) { gotScan = p },
		UpdateProgress:  func(p update.UpdateProgress) { gotUpdate = p },
	}

	cases := []struct {
		name    string
		event   string
		payload any
	}{
		{"install progress", EventInstallProgress, install},
		{"scan progress", EventScanProgress, scan},
		{"self-update progress", EventUpdateProgress, selfUpdate},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !RouteEvent(handlers, c.event, c.payload) {
				t.Fatalf("RouteEvent(%q) = false, want the name to be known", c.event)
			}
		})
	}

	if gotInstall != install {
		t.Errorf("install handler got %+v, want %+v", gotInstall, install)
	}
	if gotScan != scan {
		t.Errorf("scan handler got %+v, want %+v", gotScan, scan)
	}
	if gotUpdate != selfUpdate {
		t.Errorf("update handler got %+v, want %+v", gotUpdate, selfUpdate)
	}
}

// TestRouteEventToleratesAnUnwiredHandler is the case a read-only command hits
// constantly: it bootstraps the service graph, the services emit, and nothing
// is listening. That must be quiet rather than fatal -- progress is decoration,
// and a panic inside a service callback would abort an install that was
// otherwise succeeding.
func TestRouteEventToleratesAnUnwiredHandler(t *testing.T) {
	for _, name := range []string{EventInstallProgress, EventScanProgress, EventUpdateProgress, EventVersionsRefreshed} {
		t.Run(name, func(t *testing.T) {
			if !RouteEvent(EventHandlers{}, name, sdk.InstallProgress{}) {
				t.Errorf("RouteEvent(%q) = false with no handlers, want the name to stay known", name)
			}
		})
	}
}

// TestRouteEventDropsAMistypedPayload pins the second half of that tolerance: a
// known name carrying the wrong type is dropped, not type-asserted into a
// panic. The emitter is on the other side of an interface{}, so the compiler
// cannot help here.
func TestRouteEventDropsAMistypedPayload(t *testing.T) {
	called := false
	handlers := EventHandlers{InstallProgress: func(sdk.InstallProgress) { called = true }}

	cases := []struct {
		name    string
		event   string
		payload any
	}{
		{"wrong struct", EventInstallProgress, sdk.ScanProgress{}},
		{"pointer instead of value", EventInstallProgress, &sdk.InstallProgress{}},
		{"nil payload", EventInstallProgress, nil},
		{"a string", EventScanProgress, "go"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !RouteEvent(handlers, c.event, c.payload) {
				t.Errorf("RouteEvent(%q, %v) = false, want a mistyped payload to stay a known event", c.event, c.payload)
			}
			if called {
				t.Errorf("RouteEvent(%q, %v) reached the handler", c.event, c.payload)
			}
		})
	}
}

// TestRouteEventReportsAnUnknownName covers the drift detector: a name no case
// knows about returns false, which is what lets a test (or a future caller)
// notice that the service layer grew an event the CLI never renders.
func TestRouteEventReportsAnUnknownName(t *testing.T) {
	called := false
	handlers := EventHandlers{InstallProgress: func(sdk.InstallProgress) { called = true }}

	for _, name := range []string{"", "install", "Install:Progress", "install:done", "typo:progress"} {
		if RouteEvent(handlers, name, sdk.InstallProgress{}) {
			t.Errorf("RouteEvent(%q) = true, want an unknown name to be reported", name)
		}
	}
	if called {
		t.Error("an unknown name reached the install handler")
	}
}
