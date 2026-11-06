package ui

import (
	"testing"

	"github.com/egoist/mygo/internal/platform"
	"github.com/egoist/mygo/internal/surface"
)

// TestDrawWhileOccluded checks that a window whose frames are not held
// out of sight (surface.Conn.DrawWhileOccluded) draws them there: an
// animation asks for each next frame, the frames the surface gives build,
// and a notice is answered by the frame, not as held.
func TestDrawWhileOccluded(t *testing.T) {
	t.Setenv("MYGO_GPU", "0")
	s := &occludedSurface{}
	h := &windowHost{conn: &surface.Conn{Surface: s, DrawWhileOccluded: true, Post: func(f func()) { f() }}}
	builds := 0
	h.rt = newRuntime(func(c *context) {
		builds++
		c.AnimationFrame()
		coreTextf(c, "%d", builds)
	}, h)
	frame := func() { h.event(platform.SurfaceEvent{Kind: platform.SurfaceFrame}) }
	frame()
	s.occluded = true
	h.event(platform.SurfaceEvent{Kind: platform.SurfaceHidden})
	for i := range 3 {
		asked := s.requested
		frame()
		if h.rt.held || s.requested == asked || builds != i+2 {
			t.Fatalf("frame %d out of sight: held %v, asked for the next %v, %d builds", i, h.rt.held, s.requested != asked, builds)
		}
	}
	var p presentations
	h.afterPresent(p.add)
	p.want(t, "asked for out of sight")
	frame()
	p.want(t, "the frame out of sight", platform.Presented)
}
