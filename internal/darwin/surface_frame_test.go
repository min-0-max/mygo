//go:build darwin

package darwin

import (
	"testing"

	"github.com/egoist/mygo/internal/platform"
)

// frameHandler is a window handler that leaves the paced frames it is
// sent to draw later while wait is true.
type frameHandler struct {
	platform.WindowHandler
	wait  bool
	paced []bool
}

func (h *frameHandler) SurfaceEvent(ev platform.SurfaceEvent) bool {
	if ev.Kind != platform.SurfaceFrame {
		return false
	}
	h.paced = append(h.paced, ev.Paced)
	return ev.Paced && h.wait
}

// TestSurfaceKeepsItsMeasures checks that the size, the scale and the
// refresh rate hold until AppKit tells of a change, with no read of the
// view or the window, which this surface does not have, and that a change
// of the size comes with its value.
func TestSurfaceKeepsItsMeasures(t *testing.T) {
	s := &surface{w: &window{}, sized: true, width: 300, height: 200, backing: 2, rate: 120}
	if w, h, f := s.Size(); w != 300 || h != 200 || f != 2 {
		t.Fatalf("Size() = %v, %v, %v, want 300, 200, 2", w, h, f)
	}
	if r := s.RefreshRate(); r != 120 {
		t.Fatalf("RefreshRate() = %v, want 120", r)
	}
	s.sizeChanged(NSSize{Width: 640, Height: 480})
	if w, h, f := s.Size(); w != 640 || h != 480 || f != 2 {
		t.Fatalf("Size() after setFrameSize: = %v, %v, %v, want 640, 480, 2", w, h, f)
	}
	s.scaleChanged()
	if s.backing != 0 {
		t.Fatalf("the scale held %v after a change of the window's backing", s.backing)
	}
	s.screenChanged()
	if s.rate != 0 {
		t.Fatalf("the refresh rate held %v after a change of the screen", s.rate)
	}
}

// TestDeferredFrames checks that the display link keeps running while the
// content leaves a paced frame to draw later, and only then: once FrameNow
// draws it, and once the content draws any other frame instead (as a
// resize's, or a paced one it can draw), which draws what the one left
// would have, as the content then never calls FrameNow for it.
func TestDeferredFrames(t *testing.T) {
	h := &frameHandler{}
	s := &surface{w: &window{h: h}}
	h.wait = true
	s.frameNow(true)
	if !s.deferred {
		t.Fatal("a paced frame left to draw later: not deferred")
	}
	h.wait = false
	s.FrameNow()
	if s.deferred || h.paced[len(h.paced)-1] {
		t.Fatalf("drawn at once: deferred %v, sent paced %v", s.deferred, h.paced[len(h.paced)-1])
	}

	// A frame AppKit displays, not paced, draws the one left.
	h.wait = true
	s.frameNow(true)
	s.frameNow(false)
	if s.deferred {
		t.Fatal("a frame drawn meanwhile left the link running")
	}

	// A paced frame the content draws, as after its renderer was let go.
	s.frameNow(true)
	h.wait = false
	s.frameNow(true)
	if s.deferred {
		t.Fatal("a paced frame drawn left the link running")
	}

	// Refreshes passing while the content waits keep it deferred.
	h.wait = true
	s.frameNow(true)
	s.frameNow(true)
	if !s.deferred {
		t.Fatal("refreshes passing while the content waits: not deferred")
	}
}

// TestATickKeepsTheFrameDue checks that a frame the tick leaves to AppKit's
// display of the view draws by the next tick when AppKit did not display
// it: a layer marked to display waits for a commit of Core Animation, which
// nothing need start, and a frame lost there stops what waits for it. When
// AppKit displays the view, its frame is the one asked for, and no other
// draws.
func TestATickKeepsTheFrameDue(t *testing.T) {
	displays := func() bool { return true }

	h := &frameHandler{}
	s := &surface{w: &window{h: h}, due: true, linkRunning: true}
	if !s.tick(displays) || len(h.paced) != 0 {
		t.Fatalf("the first tick drew %d frames or paused the link, want it left to AppKit", len(h.paced))
	}
	if !s.tick(displays) || len(h.paced) != 1 || !h.paced[0] {
		t.Fatalf("AppKit displayed nothing: the next tick drew %v, want one paced frame", h.paced)
	}
	if s.tick(displays) || s.linkRunning || len(h.paced) != 1 {
		t.Fatalf("nothing is due: the tick drew %v, link running %v, want it paused", h.paced, s.linkRunning)
	}

	// The display of the view in that pass.
	h = &frameHandler{}
	s = &surface{w: &window{h: h}, due: true, linkRunning: true}
	s.tick(displays)
	s.frameNow(false)
	if s.tick(displays) || s.linkRunning || len(h.paced) != 1 || h.paced[0] {
		t.Fatalf("AppKit displayed the view: frames %v, link running %v, want its frame alone and the link paused", h.paced, s.linkRunning)
	}

	// A frame asked for within the frame that draws stays due.
	h = &frameHandler{}
	s = &surface{w: &window{h: h}, due: true, linkRunning: true}
	asking := &askingHandler{frameHandler: h, s: s}
	s.w.h = asking
	s.frameNow(false)
	if !s.due {
		t.Fatal("a frame asked for within a frame was not left due")
	}
}

// askingHandler asks for the next frame within a frame, as the content does
// for what moves.
type askingHandler struct {
	*frameHandler
	s *surface
}

func (h *askingHandler) SurfaceEvent(ev platform.SurfaceEvent) bool {
	h.s.due = true
	return h.frameHandler.SurfaceEvent(ev)
}

// TestDrawSoon checks that a frame asked for after a pause draws from the
// backend's queue, outside AppKit's display of views (#883), once however
// often it is asked for meanwhile, and not at all when another drew
// first, as a resize's does when AppKit displays the view, unless it was
// asked for again after that one.
func TestDrawSoon(t *testing.T) {
	h := &frameHandler{}
	b := &Backend{bySurface: map[id]*surface{}}
	s := &surface{w: &window{b: b, h: h}, view: 1}
	b.bySurface[s.view] = s
	s.drawSoon()
	s.drawSoon()
	if len(h.paced) != 0 {
		t.Fatal("drew at once")
	}
	drainMainQueue()
	if len(h.paced) != 1 || h.paced[0] {
		t.Fatalf("frames drawn from the queue: %v, want one not paced", h.paced)
	}

	s.drawSoon()
	s.frameNow(false)
	drainMainQueue()
	if len(h.paced) != 2 {
		t.Fatalf("drew %d frames, want the one drawn meanwhile alone", len(h.paced)-1)
	}

	// One asked for after the frame drawn meanwhile, as once the window
	// whose first frame AppKit drew while the window was out of sight
	// shows (SurfaceShown), still draws: dropped with the first, what
	// moves would stop for good.
	s.drawSoon()
	s.frameNow(false)
	s.drawSoon()
	drainMainQueue()
	if len(h.paced) != 4 || s.soon {
		t.Fatalf("asked for after the frame drawn meanwhile: drew %d frames from the queue, want 1", len(h.paced)-3)
	}

	// A window closed meanwhile draws none.
	s.drawSoon()
	s.w.closed = true
	drainMainQueue()
	if len(h.paced) != 4 || s.soon {
		t.Fatalf("a closed window drew %d frames (soon %v)", len(h.paced)-4, s.soon)
	}
}
