package ui

import "testing"

// An event carries the pointer in the window as the system reported it, so
// a handler that the build of a moved element made, whose Bounds are those
// of the frame before, needs no box to place the event in the window.
func TestAnEventCarriesThePointInTheWindow(t *testing.T) {
	type seen struct {
		ev     InputEvent
		bounds Rect
	}
	var got []seen
	offset := float32(0)
	tt := NewTester(func(c *Context) {
		box := Box(c).Size(100, 100).MarginStart(offset)
		bounds := box.Bounds()
		box.HandleInput(func(ev InputEvent) bool {
			if ev.Kind == InputPointerDown {
				got = append(got, seen{ev, bounds})
			}
			return true
		})
	}, 400, 200)
	defer tt.rt.close()
	tt.Frame()
	offset = 100
	tt.Frame()
	tt.Press(150, 40)
	if len(got) != 1 {
		t.Fatalf("the handler saw %d presses, want 1", len(got))
	}
	ev, bounds := got[0].ev, got[0].bounds
	if bounds.X != 0 {
		t.Fatalf("the handler of the moved box has Bounds %+v, want those of the frame before at x 0", bounds)
	}
	if ev.X != 50 || ev.Y != 40 {
		t.Fatalf("the press lies at %v,%v of the moved box, want 50,40", ev.X, ev.Y)
	}
	if ev.WindowX != 150 || ev.WindowY != 40 {
		t.Fatalf("the press lies at %v,%v of the window, want 150,40 where the system pressed", ev.WindowX, ev.WindowY)
	}
}
