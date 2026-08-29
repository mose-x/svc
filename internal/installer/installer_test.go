package installer

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"svc/internal/config"
	"svc/internal/sdk"
	"svc/internal/wailsrt"
)

// recordingRuntime implements wailsrt.Runtime and records every emitted
// event so tests can assert on the frontend-facing event stream.
type recordingRuntime struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	name string
	data []any
}

func (r *recordingRuntime) Context() context.Context { return context.Background() }

func (r *recordingRuntime) EventsEmit(eventName string, data ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedEvent{name: eventName, data: data})
}

func (r *recordingRuntime) OpenFileDialog(title string, filters []wailsrt.FileFilter) (string, error) {
	return "", nil
}

func (r *recordingRuntime) OpenDirectoryDialog(title string) (string, error) { return "", nil }

func (r *recordingRuntime) Quit() {}

// newScanTestService wires a Service rooted in a temp HOME (never the real
// ~/.svc) with a scrubbed PATH so no host binary (node/python/go/...) is
// discovered: every GetLocalStatus stays hermetic and no version probe is
// ever executed on a host binary.
func newScanTestService(t *testing.T, rt wailsrt.Runtime) *Service {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	cfg, err := config.NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetSvcDir(filepath.Join(home, ".svc"))
	cfg.SetHomeDir(home)
	return New(cfg, sdk.NewRegistry(cfg, nil), nil, nil, nil, nil, nil, rt)
}

// TestGetAllSdkStatus_EmitsScanProgress pins the startup loading-screen
// contract: one sdk:scan-progress event per SDK, in registry order, with
// 1-based index, the total count and the display name, so the UI can show
// "Checking Python (4/14)..." while the scan runs.
func TestGetAllSdkStatus_EmitsScanProgress(t *testing.T) {
	rt := &recordingRuntime{}
	svc := newScanTestService(t, rt)

	statuses := svc.GetAllSdkStatus()
	want := sdk.AllSdkTypes()
	if len(statuses) != len(want) {
		t.Fatalf("GetAllSdkStatus returned %d statuses; want %d", len(statuses), len(want))
	}

	var scans []sdk.ScanProgress
	for _, ev := range rt.events {
		if ev.name != "sdk:scan-progress" {
			t.Fatalf("unexpected event %q during GetAllSdkStatus", ev.name)
		}
		p, ok := ev.data[0].(sdk.ScanProgress)
		if !ok {
			t.Fatalf("sdk:scan-progress payload is %T; want sdk.ScanProgress", ev.data[0])
		}
		scans = append(scans, p)
	}
	if len(scans) != len(want) {
		t.Fatalf("emitted %d sdk:scan-progress events; want %d", len(scans), len(want))
	}
	for i, sdkType := range want {
		if scans[i].SdkType != sdkType {
			t.Errorf("scan[%d].SdkType = %q; want %q", i, scans[i].SdkType, sdkType)
		}
		if scans[i].DisplayName != sdk.SdkDisplayName(sdkType) {
			t.Errorf("scan[%d].DisplayName = %q; want %q", i, scans[i].DisplayName, sdk.SdkDisplayName(sdkType))
		}
		if scans[i].Index != i+1 || scans[i].Total != len(want) {
			t.Errorf("scan[%d] = (%d/%d); want (%d/%d)", i, scans[i].Index, scans[i].Total, i+1, len(want))
		}
	}
}

// TestGetAllSdkStatus_NilRt_NoPanic covers the rt-less path: the progress
// emit is best-effort and must never break the status scan itself.
func TestGetAllSdkStatus_NilRt_NoPanic(t *testing.T) {
	svc := newScanTestService(t, nil)
	statuses := svc.GetAllSdkStatus()
	if len(statuses) != len(sdk.AllSdkTypes()) {
		t.Fatalf("GetAllSdkStatus with nil runtime returned %d statuses; want %d",
			len(statuses), len(sdk.AllSdkTypes()))
	}
}

// TestGetAllSdkStatus_NilRegistry covers the uninitialized-service path.
func TestGetAllSdkStatus_NilRegistry(t *testing.T) {
	svc := New(nil, nil, nil, nil, nil, nil, nil, nil)
	if got := svc.GetAllSdkStatus(); got != nil {
		t.Fatalf("GetAllSdkStatus with nil registry = %v; want nil", got)
	}
}

// TestFilterResidualVersionDirs pins the display-layer filter that keeps the
// atomic-replace byproducts ("<version>.old" / "<version>.new") out of the
// installed-versions list shown to the user.
func TestFilterResidualVersionDirs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "mixed list drops .old and .new leftovers",
			in:   []string{"1.21.0", "1.21.0.old", "1.22.0.new", "2.0.0"},
			want: []string{"1.21.0", "2.0.0"},
		},
		{
			name: "all leftovers",
			in:   []string{"1.0.old", "1.0.new"},
			want: []string{},
		},
		{
			name: "no leftovers unchanged",
			in:   []string{"17.0.2", "21.0.1"},
			want: []string{"17.0.2", "21.0.1"},
		},
		{
			name: "empty input",
			in:   []string{},
			want: []string{},
		},
		{
			name: "nil input",
			in:   nil,
			want: []string{},
		},
		{
			name: "similar-but-not-suffix names kept",
			in:   []string{"1.0.oldish", "1.0.newest", "old", "new"},
			want: []string{"1.0.oldish", "1.0.newest", "old", "new"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterResidualVersionDirs(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("filterResidualVersionDirs(%v) = %v (len %d); want %v (len %d)",
					tt.in, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("filterResidualVersionDirs(%v)[%d] = %q; want %q", tt.in, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestFilterResidualVersionDirs_DoesNotAliasInput verifies the result is a
// fresh slice (filtering must not mutate the caller's backing array).
func TestFilterResidualVersionDirs_DoesNotAliasInput(t *testing.T) {
	in := []string{"1.0", "1.0.old", "2.0"}
	got := filterResidualVersionDirs(in)
	if len(got) != 2 || got[0] != "1.0" || got[1] != "2.0" {
		t.Fatalf("unexpected result: %v", got)
	}
	if in[0] != "1.0" || in[1] != "1.0.old" || in[2] != "2.0" {
		t.Errorf("input slice was mutated by filtering: %v", in)
	}
}

// TestWaitForInstallExit_Immediate pins the fast path: an install that has
// already exited (done closed) returns true without burning the timeout.
func TestWaitForInstallExit_Immediate(t *testing.T) {
	done := make(chan struct{})
	close(done)
	start := time.Now()
	if !waitForInstallExit(done, time.Second) {
		t.Fatal("waitForInstallExit on closed channel = false; want true")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("waitForInstallExit took %v on a closed channel; want immediate", elapsed)
	}
}

// TestWaitForInstallExit_Timeout pins the bounded-wait path: an install that
// never exits must not block the new install forever.
func TestWaitForInstallExit_Timeout(t *testing.T) {
	done := make(chan struct{}) // never closed
	start := time.Now()
	if waitForInstallExit(done, 50*time.Millisecond) {
		t.Fatal("waitForInstallExit on never-closed channel = true; want false (timeout)")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waitForInstallExit blocked for %v; want ~50ms", elapsed)
	}
}

// TestWaitForInstallExit_EventualExit covers the realistic race: the old
// install exits shortly AFTER the cancel, within the timeout window.
func TestWaitForInstallExit_EventualExit(t *testing.T) {
	done := make(chan struct{})
	go func() {
		time.Sleep(30 * time.Millisecond)
		close(done)
	}()
	if !waitForInstallExit(done, time.Second) {
		t.Fatal("waitForInstallExit = false; want true once the old install closes done")
	}
}

// TestFetcherLockPerSdk verifies the per-SDK mutexes used to serialize
// SetHTTPClient + dependent fetcher calls: same key -> same mutex (so the
// background refresh and an install of the SAME SDK are serialized),
// different keys -> different mutexes (so unrelated SDKs don't block each
// other).
func TestFetcherLockPerSdk(t *testing.T) {
	var locks fetcherLocks // zero value usable: lazy init path

	a1 := locks.get("go")
	a2 := locks.get("go")
	if a1 != a2 {
		t.Fatal("fetcherLocks.get returned different mutexes for the same SDK key")
	}
	b := locks.get("jdk")
	if b == a1 {
		t.Fatal("fetcherLocks.get returned the same mutex for different SDK keys")
	}

	// The map must be safe for concurrent first-time access.
	var par fetcherLocks
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			par.get("go").Lock()
			par.get("go").Unlock()
		}()
	}
	wg.Wait()
}
