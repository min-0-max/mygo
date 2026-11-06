//go:build darwin

package e2e

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/internal/darwin"
	"github.com/egoist/mygo/internal/wait"
	"github.com/egoist/mygo/ui"
)

// TestDrawWhileOccluded checks that an animation in a window out of sight
// draws its frames with WindowOptions.DrawWhileOccluded, each after a tick
// of the window's display link or a firing of its frame timer: the frames
// after the first are no more than those between the first and the last.
// Without the option, the window holds them.
func TestDrawWhileOccluded(t *testing.T) {
	for _, draw := range []bool{true, false} {
		name := "held"
		if draw {
			name = "drawn"
		}
		t.Run(name, func(t *testing.T) { occludedAnimation(t, draw) })
	}
}

// occludedFrames is the number of frames an animation in a window out of
// sight draws before the test looks at them.
const occludedFrames = 20

func occludedAnimation(t *testing.T, draw bool) {
	var animate atomic.Bool
	var handle uintptr
	// firings holds, for each frame of the animation, the ticks and
	// firings of the window so far; read on the main thread.
	var firings []int
	w := newWindow(t, mygo.WindowOptions{Title: "Occluded animation", Width: 300, Height: 200, DrawWhileOccluded: draw, Content: ui.View(func(c *ui.Context) {
		ui.Box(c).Fill().Background(ui.RGB(255, 255, 255))
		if animate.Load() {
			firings = append(firings, darwin.TestFrameFirings(handle))
			c.AnimationFrame()
		}
	})})
	waitOnScreen(w)
	mygo.RunOnMain(func() { handle = w.NativeHandle() })
	w.Hide()
	if !wait.Poll(func() (on bool) {
		mygo.RunOnMain(func() { on = darwin.TestOnScreen(handle) })
		return !on
	}) {
		t.Fatal("the window still shows once hidden")
	}
	animate.Store(true)
	w.Invalidate()
	frames := func() (n int) {
		mygo.RunOnMain(func() { n = len(firings) })
		return n
	}
	if !draw {
		time.Sleep(time.Second)
		if n := frames(); n > 1 {
			t.Fatalf("a window out of sight drew %d frames of the animation in a second, without DrawWhileOccluded", n)
		}
		return
	}
	eventually(t, "the frames of the animation out of sight", func() bool { return frames() >= occludedFrames })
	var got []int
	mygo.RunOnMain(func() {
		animate.Store(false)
		got = append(got, firings...)
	})
	after, between := len(got)-1, got[len(got)-1]-got[0]
	t.Logf("%d frames after the first, %d ticks and timer firings between the first and the last", after, between)
	if after > between {
		t.Fatalf("%d frames after the first, past the %d ticks and timer firings between the first and the last", after, between)
	}
}
