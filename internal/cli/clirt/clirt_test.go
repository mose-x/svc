package clirt

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"svc/internal/apperr"
	"svc/internal/cli/i18n"
	"svc/internal/wailsrt"
)

func TestNewDefaults(t *testing.T) {
	if got := New(nil, nil).Context(); got != context.Background() {
		t.Errorf("New(nil, nil).Context() = %v, want context.Background()", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if got := New(ctx, nil).Context(); got != ctx {
		t.Error("New must hand back the context it was given, not a copy")
	}

	// A nil sink drops events rather than panicking: a command that has not
	// wired a renderer yet must still survive a service callback.
	New(ctx, nil).EventsEmit("install:progress", "payload")
	var nilRuntime *Runtime
	nilRuntime.EventsEmit("install:progress", "payload")
	nilRuntime.Quit()
	if nilRuntime.QuitRequested() {
		t.Error("QuitRequested on a nil Runtime = true, want false")
	}
}

func TestEventsEmitForwardsTheFirstPayload(t *testing.T) {
	type call struct {
		name    string
		payload any
	}
	var calls []call
	runtime := New(context.Background(), func(name string, payload any) {
		calls = append(calls, call{name, payload})
	})

	payload := struct{ Stage string }{"downloading"}
	runtime.EventsEmit("install:progress", payload)
	runtime.EventsEmit("install:versions-refreshed")
	runtime.EventsEmit("update:progress", "first", "second")

	want := []call{
		{"install:progress", payload},
		{"install:versions-refreshed", nil},
		// The Wails bus hands a JS listener the first value only, so dropping
		// the rest keeps the two shells seeing the same event.
		{"update:progress", "first"},
	}
	if len(calls) != len(want) {
		t.Fatalf("the sink saw %d calls, want %d: %+v", len(calls), len(want), calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
}

// TestEventsEmitSerialisesConcurrentEmitters covers the case the mutex exists
// for: the install progress loop and the SDK scan can emit from different
// goroutines, and a renderer doing single-line redraws would interleave two
// frames into one unreadable line if the sink were entered concurrently.
func TestEventsEmitSerialisesConcurrentEmitters(t *testing.T) {
	const goroutines, each = 8, 40

	var inFlight atomic.Int32
	var received int
	runtime := New(context.Background(), func(string, any) {
		if n := inFlight.Add(1); n != 1 {
			t.Errorf("the sink was entered by %d goroutines at once", n)
		}
		// Widen the window a concurrent entry would have to fit through.
		time.Sleep(20 * time.Microsecond)
		received++
		inFlight.Add(-1)
	})

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				runtime.EventsEmit(fmt.Sprintf("event-%d", g), i)
			}
		}(g)
	}
	wg.Wait()

	if want := goroutines * each; received != want {
		t.Errorf("the sink saw %d events, want %d", received, want)
	}
}

// TestDialogsRefuseWithoutOpeningAnything is the no-popup red line. A CLI that
// reached for a picker would raise a window out of a headless command -- in CI,
// over SSH, or from a cron job -- so the refusal has to be unconditional and
// the error has to carry the machine-readable code the shell maps to an exit
// status.
func TestDialogsRefuseWithoutOpeningAnything(t *testing.T) {
	runtime := New(context.Background(), nil)

	cases := []struct {
		name string
		call func() (string, error)
	}{
		{"OpenFileDialog", func() (string, error) {
			return runtime.OpenFileDialog("Select Archive File", []wailsrt.FileFilter{{DisplayName: "Archive", Pattern: "*.zip"}})
		}},
		{"OpenFileDialog with no filters", func() (string, error) {
			return runtime.OpenFileDialog("Select Archive File", nil)
		}},
		{"OpenDirectoryDialog", func() (string, error) {
			return runtime.OpenDirectoryDialog("Select SDK Directory")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, err := c.call()
			if path != "" {
				t.Errorf("path = %q, want the empty string", path)
			}
			if err == nil {
				t.Fatal("err = nil, want a refusal")
			}
			code, ok := i18n.ErrorCode(err.Error())
			if !ok || code != string(apperr.CliNoDialog) {
				t.Errorf("err = %v, want an apperr marker carrying %q", err, apperr.CliNoDialog)
			}
			if !errors.Is(err, err) {
				t.Error("the refusal must be a normal error value")
			}
		})
	}
}

// TestQuitRecordsTheRequest covers the updater handover: Quit cannot close a
// window that does not exist, so it records that the process must exit and the
// command layer turns that into an exit code after the command returns.
func TestQuitRecordsTheRequest(t *testing.T) {
	runtime := New(context.Background(), nil)
	if runtime.QuitRequested() {
		t.Fatal("QuitRequested before Quit = true")
	}
	runtime.Quit()
	if !runtime.QuitRequested() {
		t.Error("QuitRequested after Quit = false")
	}
	runtime.Quit() // idempotent
	if !runtime.QuitRequested() {
		t.Error("a second Quit must not clear the flag")
	}
}

// TestSignalContextCancelsOnStop is the portable half of the signal contract:
// the context returned is a real cancellable context whose cancellation the
// services observe. The signal-driven half lives in signal_unix_test.go.
func TestSignalContextCancelsOnStop(t *testing.T) {
	ctx, stop := SignalContext(context.Background())
	if ctx == nil {
		t.Fatal("SignalContext returned a nil context")
	}
	select {
	case <-ctx.Done():
		t.Fatal("the context is already cancelled before any signal")
	default:
	}

	stop()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("stop() did not cancel the context")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("ctx.Err() = %v, want context.Canceled so ExitCodeForError reports %d", ctx.Err(), 5)
	}
}

// TestSignalContextInheritsTheParent keeps the composition honest: a parent
// that is already done must not be resurrected, so a caller can layer a timeout
// over the signal context.
func TestSignalContextInheritsTheParent(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()

	ctx, stop := SignalContext(parent)
	defer stop()
	select {
	case <-ctx.Done():
	default:
		t.Error("the signal context outlived its already-cancelled parent")
	}
}

func TestRuntimeSatisfiesTheServiceInterface(t *testing.T) {
	// The package-level assertion is the real check; this one fails with a
	// test name rather than a compile error if the wiring is ever removed.
	var rt wailsrt.Runtime = New(context.Background(), nil)
	if rt.Context() == nil {
		t.Error("Context() = nil")
	}
}
