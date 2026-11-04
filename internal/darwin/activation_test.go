//go:build darwin

package darwin

import (
	"testing"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo/internal/platform"
)

// activationCalls counts the messages that the stand-ins of
// activationStandIns received.
var activationCalls struct {
	// activate counts activateIgnoringOtherApps: to the application.
	activate int
	// keyAndFront counts makeKeyAndOrderFront: to the window.
	keyAndFront int
}

// activationStandIns returns objects that stand in for NSApp, with the
// regular activation policy, and for an NSWindow: they count the messages
// that activate the application and that make the window key and order
// it front, and do neither, so the tests activate nothing and show
// nothing.
func activationStandIns(t *testing.T) (app, win id) {
	t.Helper()
	load()
	if objc.GetClass("MyGoTestApplication") == 0 {
		classDef("MyGoTestApplication", "NSObject", nil, []objc.MethodDef{
			method("activationPolicy", func(self id, _ objc.SEL) int { return nsApplicationActivationPolicyRegular }),
			method("activateIgnoringOtherApps:", func(self id, _ objc.SEL, flag bool) { activationCalls.activate++ }),
		})
		classDef("MyGoTestWindow", "NSObject", nil, []objc.MethodDef{
			method("makeKeyAndOrderFront:", func(self id, _ objc.SEL, sender id) { activationCalls.keyAndFront++ }),
		})
	}
	if objc.GetClass("MyGoAppDelegate") == 0 {
		registerAppDelegate()
	}
	activationCalls.activate, activationCalls.keyAndFront = 0, 0
	app, win = alloc("MyGoTestApplication"), alloc("MyGoTestWindow")
	t.Cleanup(func() {
		release(app)
		release(win)
	})
	return app, win
}

// readyHandler counts Ready.
type readyHandler struct {
	platform.AppHandler
	ready int
}

func (h *readyHandler) Ready() { h.ready++ }

// The application activates itself as it finishes launching, unless
// AppOptions.NoSelfActivation leaves that to the system (U5).
func TestLaunchActivation(t *testing.T) {
	for _, c := range []struct {
		name             string
		noSelfActivation bool
		activations      int
	}{
		{"by default", false, 1},
		{"without self-activation", true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			app, _ := activationStandIns(t)
			h := &readyHandler{}
			b := New()
			b.h, b.app, b.noSelfActivation = h, app, c.noSelfActivation
			kept := theBackend
			theBackend = b
			t.Cleanup(func() { theBackend = kept })
			delegate := alloc("MyGoAppDelegate")
			defer release(delegate)
			send(delegate, "applicationDidFinishLaunching:", 0)
			if activationCalls.activate != c.activations {
				t.Errorf("activateIgnoringOtherApps: as the launch finished: %d, want %d", activationCalls.activate, c.activations)
			}
			if h.ready != 1 {
				t.Errorf("Ready as the launch finished: %d, want 1", h.ready)
			}
		})
	}
}

// Show makes a window key and orders it front, and activates the
// application for a window that is not a panel, unless
// AppOptions.NoSelfActivation keeps it from activating the application
// (U5).
func TestShowActivation(t *testing.T) {
	for _, c := range []struct {
		name             string
		noSelfActivation bool
		panel            bool
		activations      int
	}{
		{"by default", false, false, 1},
		{"a panel, by default", false, true, 0},
		{"without self-activation", true, false, 0},
		{"a panel, without self-activation", true, true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			app, win := activationStandIns(t)
			b := New()
			b.app, b.noSelfActivation = app, c.noSelfActivation
			w := &window{b: b, opts: &platform.WindowOptions{Panel: c.panel}, win: win}
			w.Show()
			if activationCalls.keyAndFront != 1 {
				t.Errorf("makeKeyAndOrderFront: as Show showed the window: %d, want 1", activationCalls.keyAndFront)
			}
			if activationCalls.activate != c.activations {
				t.Errorf("activateIgnoringOtherApps: as Show showed the window: %d, want %d", activationCalls.activate, c.activations)
			}
		})
	}
}
