//go:build darwin

package ui

import (
	"fmt"
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/internal/gpu/metal"
	"github.com/egoist/mygo/internal/scene"
)

// blurredWindow is a window of text and colored stripes, blurred by 3
// points as a dialog blurs the workbench behind it, or not, and kept
// (Cache) or drawn anew every frame.
func blurredWindow(blur, kept bool) func(c *context) {
	return func(c *context) {
		e := coreBox(c).Size(600, 380).Background(RGB(245, 245, 240)).Clip()
		if blur {
			e.Blur(3)
		}
		if kept {
			e.Cache()
		}
		e.Children(func() {
			for i := range 14 {
				coreBox(c).Absolute().Left(float32(20+i*40)).Top(0).Size(8, 380).Background(RGB(uint8(30*i), 80, 200-uint8(10*i)))
			}
			for i := range 16 {
				coreText(c, fmt.Sprintf("line %d of the workbench, with words and numbers 0123456789", i))
			}
		})
	}
}

// metalFrames draws the first frames of view, a window of w×h points at
// scale 2, with Metal into a texture, as a window's renderer draws them.
func metalFrames(t *testing.T, view func(c *context), w, h int) *metal.Offscreen {
	t.Helper()
	o, err := metal.NewOffscreen(w*2, h*2)
	if err != nil {
		t.Skip("no Metal:", err)
	}
	host := &stressHost{headless: &headless{w: float32(w), h: float32(h), scale: 2, hz: 60}, render: func(s *scene.Scene) {
		if _, e := o.Render(s); e != nil && err == nil {
			err = e
		}
	}}
	now := time.Unix(1000, 0)
	rt := newRuntime(view, host)
	rt.clock = func() time.Time { return now }
	for range 3 {
		now = now.Add(time.Second / 60)
		rt.runFrame()
	}
	if err != nil {
		o.Release()
		t.Fatal(err)
	}
	return o
}

// A window blurred whole and kept looks, drawn in memory and with Metal,
// the same as the window blurred anew every frame: the frames after the
// first show the blur kept, computed once.
func TestAKeptBlurLooksAsABlurDrawnAnew(t *testing.T) {
	inMemory := func(kept bool) *image.RGBA {
		tt := coreNewTester(blurredWindow(true, kept), 600, 380)
		tt.SetScale(2)
		tt.Frame()
		return tt.Image()
	}
	compareSame(t, "in memory", inMemory(true), inMemory(false))
	withMetal := func(kept bool) *image.RGBA {
		o := metalFrames(t, blurredWindow(true, kept), 600, 380)
		defer o.Release()
		pix, err := o.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 1200, 760))
		for i := 0; i+3 < len(pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = pix[i+2], pix[i+1], pix[i], pix[i+3]
		}
		return img
	}
	compareSame(t, "with Metal", withMetal(true), withMetal(false))
}

// compareSame compares the colors of got with those of want, which are to
// be the same.
func compareSame(t *testing.T, name string, got, want *image.RGBA) {
	t.Helper()
	if want.Bounds() != got.Bounds() {
		t.Fatalf("%s: %v kept, %v drawn anew", name, got.Bounds(), want.Bounds())
	}
	most, sum := 0, 0
	for i := range got.Pix {
		if i%4 == 3 {
			continue
		}
		d := int(got.Pix[i]) - int(want.Pix[i])
		d = max(d, -d)
		most = max(most, d)
		sum += d
	}
	mean := float64(sum) / float64(len(got.Pix)/4*3)
	t.Logf("%s: colors at most %d/255 apart, %.3f/255 on average", name, most, mean)
	if most > 0 {
		t.Errorf("%s: colors up to %d/255 apart, %.3f/255 on average; want the same", name, most, mean)
	}
}
