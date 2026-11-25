// Package clirt implements wailsrt.Runtime for a command-line invocation.
//
// The service layer talks to a shell through that one interface: it emits
// progress events, asks for a cancellation context, and -- only in the GUI --
// asks for a file picker. clirt is the CLI's answer, and it differs from the
// Wails adapter in exactly the three places a terminal is not a window:
//
//   - Context comes from signal.NotifyContext, so the first Ctrl-C cancels the
//     running operation the same way the GUI's cancel button does. The
//     notification is then released immediately, because NotifyContext itself
//     only cancels: left registered, a second Ctrl-C would be swallowed into
//     its buffered channel and the process would carry on regardless of how
//     many times the user pressed it.
//   - The dialogs return an apperr instead of opening anything. A headless
//     command must never raise a window; that is a product red line, not a
//     preference, and the error tells the user to pass a path instead.
//   - Quit records a flag rather than closing a window. The updater calls it
//     after launching the platform swap script; the command layer reads the
//     flag afterwards and exits, because the process really must go.
//
// Events are handed to an EventSink rather than to typed callbacks on purpose.
// Typing them here would mean importing the packages that produce the payloads,
// and the renderers that consume them live in internal/cli -- which imports
// this package. The sink keeps the dependency one-directional and puts the
// event-name switch in the command layer, where a new event shows up as a gap
// in that package's own router.
package clirt

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"svc/internal/apperr"
	"svc/internal/logger"
	"svc/internal/wailsrt"
)

// EventSink receives one service event: its name and its first payload value,
// exactly as the Wails event bus would deliver it. A nil payload means the
// emitter passed none. The sink is called from whichever goroutine emitted the
// event and must be safe to call repeatedly; Runtime serialises the calls, so a
// sink that writes to a single stream does not need its own lock.
type EventSink func(eventName string, payload any)

// Runtime is the CLI's wailsrt.Runtime.
type Runtime struct {
	ctx  context.Context
	sink EventSink

	// mu serialises sink calls. Services emit from the goroutine doing the
	// work, and two of them (the install progress loop and the SDK scan) can
	// overlap, so without this a renderer writing single-line redraws could
	// interleave two frames on one line.
	mu sync.Mutex

	// quit records that a service asked the application to exit.
	quit atomic.Bool
}

// Runtime satisfies the interface the service layer depends on. Stating it here
// turns a signature drift into a compile error in this package rather than a
// confusing failure at the Bootstrap call site.
var _ wailsrt.Runtime = (*Runtime)(nil)

// New builds a Runtime whose Context is ctx and whose events go to sink. A nil
// ctx falls back to context.Background and a nil sink drops events, so a caller
// that has not wired a renderer yet cannot crash a service callback.
func New(ctx context.Context, sink EventSink) *Runtime {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Runtime{ctx: ctx, sink: sink}
}

// SignalContext returns a context cancelled by the first SIGINT or SIGTERM,
// together with the release function the caller must defer.
//
// The first signal cancels; the notification is released the moment it does, so
// the second signal meets the default disposition and terminates the process
// outright. That two-step behaviour is the contract, and it is not what
// signal.NotifyContext gives on its own: its watcher calls cancel and nothing
// else, leaving the handler registered, so without the release below every
// Ctrl-C after the first would disappear into a buffered channel and a stuck
// download could never be killed from the keyboard.
//
// SIGTERM is included so `kill <pid>` and a container stop cancel the running
// operation instead of leaving a half-written install behind; on Windows it is
// simply never delivered, where os.Interrupt (Ctrl-C) is the signal that
// matters. SIGKILL cannot be caught on any platform and is not listed.
func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}

// Context returns the cancellation context the services run under.
func (r *Runtime) Context() context.Context { return r.ctx }

// EventsEmit forwards one event to the sink. Extra payload values beyond the
// first are dropped: every emitter in the service layer passes exactly one, and
// the Wails bus itself only forwards the first to a JS listener.
func (r *Runtime) EventsEmit(eventName string, data ...any) {
	if r == nil || r.sink == nil {
		return
	}
	var payload any
	if len(data) > 0 {
		payload = data[0]
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.sink(eventName, payload)
}

// OpenFileDialog refuses. The importer is the only caller, and it only asks on
// behalf of the GUI's "browse" buttons; the CLI takes the path as an argument.
func (r *Runtime) OpenFileDialog(title string, _ []wailsrt.FileFilter) (string, error) {
	return "", errNoDialog(title)
}

// OpenDirectoryDialog refuses, for the same reason as OpenFileDialog.
func (r *Runtime) OpenDirectoryDialog(title string) (string, error) {
	return "", errNoDialog(title)
}

// errNoDialog builds the refusal. The dialog's title goes to the log rather
// than to the user: it is GUI wording ("Select Archive File") and would read as
// nonsense in a terminal, but it is exactly what a maintainer needs to find the
// call site that assumed a window was available.
func errNoDialog(title string) error {
	logger.Info("CLI refused to open a dialog (%q); the command line takes paths as arguments", title)
	return apperr.New(apperr.CliNoDialog, nil)
}

// Quit records that the application must exit. The updater calls it after
// spawning the platform swap script, which replaces the running binary: staying
// alive past that point would let a stale process keep writing to a home the
// new binary is about to own.
func (r *Runtime) Quit() {
	if r == nil {
		return
	}
	r.quit.Store(true)
}

// QuitRequested reports whether a service asked the process to exit. The
// command layer checks it after the command returns and exits 0, because a
// requested quit is a successful handover to the update script rather than a
// failure.
func (r *Runtime) QuitRequested() bool {
	if r == nil {
		return false
	}
	return r.quit.Load()
}
