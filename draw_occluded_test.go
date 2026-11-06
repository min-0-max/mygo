package mygo

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestDrawWhileOccludedOption checks that WindowOptions.DrawWhileOccluded
// reaches the backend's window, whose surface draws the frames asked for
// out of sight, and the Content, which then holds none.
func TestDrawWhileOccludedOption(t *testing.T) {
	for _, draw := range []bool{false, true} {
		w := NewWindow(WindowOptions{Width: 300, Height: 200, DrawWhileOccluded: draw, Content: ui.View(func(c *ui.Context) {})})
		fw := fb.Windows()[len(fb.Windows())-1]
		var conn bool
		onMain(func() { conn = w.conn.DrawWhileOccluded })
		if fw.Opts.DrawWhileOccluded != draw || conn != draw {
			t.Errorf("DrawWhileOccluded %v: the backend's window has %v, the Content %v", draw, fw.Opts.DrawWhileOccluded, conn)
		}
		w.Destroy()
	}
}
