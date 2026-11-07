//go:build !js

package mygo

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
)

// This window integration owner uses the desktop fake backend and its TestMain
// (contentWindow). Portable presentation behavior is owned by ui's untagged
// after_present_test.go, including builds for browsers.
// TestAfterPresent checks that the notice of a window showing Content
// runs once on the main thread, after the frame showing the Content as it
// stood presented, or as the window is destroyed first, and that a window
// showing a web page, or destroyed, takes none.
func TestAfterPresent(t *testing.T) {
	n := 0
	w, _, s := contentWindow(t, func(c *ui.Context) { ui.Textf(c, "n = %d", n) })
	shown := s.Frames()
	var got []Presentation
	var main []bool
	var frames []int
	err := w.AfterPresent(func(p Presentation, err error) {
		if err != nil {
			t.Errorf("presented with %v", err)
		}
		got = append(got, p)
		main = append(main, isMainThread())
		frames = append(frames, s.Frames())
	})
	if err != nil {
		t.Fatal(err)
	}
	onMain(func() { s.Frame() })
	onMain(func() { s.Frame() })
	if len(got) != 1 || got[0] != Presented || !main[0] || frames[0] != shown+1 {
		t.Fatalf("noticed %v on the main thread %v, after %v frames of %d", got, main, frames, shown+1)
	}
	if got[0].String() != "presented" {
		t.Errorf("Presented reads %q", got[0])
	}

	page := NewWindow(WindowOptions{Width: 300, Height: 200})
	defer page.Destroy()
	if err := page.AfterPresent(func(Presentation, error) { t.Error("a web page's notice ran") }); !errors.Is(err, errNotContent) {
		t.Errorf("a window showing a web page: %v", err)
	}
	var closed []Presentation
	if err := w.AfterPresent(func(p Presentation, _ error) { closed = append(closed, p) }); err != nil {
		t.Fatal(err)
	}
	w.Destroy()
	if len(closed) != 1 || closed[0] != PresentClosed {
		t.Errorf("destroyed before its frame: noticed %v", closed)
	}
	if err := w.AfterPresent(func(Presentation, error) { t.Error("a destroyed window's notice ran") }); !errors.Is(err, errDestroyed) {
		t.Errorf("a destroyed window: %v", err)
	}
}
