package ui

import (
	"slices"
	"testing"
)

// TestAnInputDuringAFrameAppliesAfterTheFrame checks that a press that
// reaches the engine while the view builds, as a nested step of the main
// thread delivers it, waits until the frame ends: its observers run outside
// the frame, where the element tree of the frame can be read, and the record
// of the frame counts it.
func TestAnInputDuringAFrameAppliesAfterTheFrame(t *testing.T) {
	var tt *Tester
	var during []bool
	inject := false
	tt = coreNewTester(func(c *context) {
		coreColumn(c).Fill().ObserveInput(func(ev InputEvent) {
			if ev.Kind == InputPointerDown {
				during = append(during, tt.rt.inFrame)
			}
		}).Children(func() {
			coreButton(c, "A")
		})
		if inject {
			inject = false
			tt.Press(10, 10)
		}
	}, 200, 100)
	var r recorded
	tt.rt.recorder = r.recorder()
	inject = true
	tt.Frame()
	if !slices.Equal(during, []bool{false}) {
		t.Fatalf("the observer of the press ran inside the frame %v times, want once outside it: %v", len(during), during)
	}
	deferred := 0
	for _, f := range r.frames {
		deferred += f.Deferred
	}
	if deferred != 1 {
		t.Fatalf("the frames recorded %d inputs that came during them, want 1: %+v", deferred, r.frames)
	}
}
