//go:build darwin

package darwin

import "testing"

// TestDarkFocusRing reads the ring of the desktop: a color of its own with
// an alpha, unlike the opaque accent, which it does not change.
func TestDarkFocusRing(t *testing.T) {
	load()
	p := theme{b: &Backend{app: send(class("NSApplication"), "sharedApplication")}}.Preferences()
	if p.DarkFocusRing.A == 0 || p.DarkFocusRing.A == 255 {
		t.Errorf("the ring is %+v", p.DarkFocusRing)
	}
	if p.Accent.A != 255 {
		t.Errorf("the accent is %+v", p.Accent)
	}
}
