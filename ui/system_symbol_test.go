package ui

import (
	"image"
	"image/color"
	"testing"
)

// A symbol of the system is its mask at the point size and the scale of the window, in the color it is drawn with.
func TestSystemSymbol(t *testing.T) {
	var got Symbol
	var missing Symbol
	asked := 0
	var box = Rect{X: 10, Y: 10, W: 40, H: 40}
	tint := Color{R: 200, G: 30, B: 90, A: 255}
	tt := coreNewTester(func(c *context) {
		got = c.SystemSymbol("disc", 13)
		missing = c.SystemSymbol("none", 13)
		coreBox(c).Size(100, 100).Draw(func(p *Painter, r Rect) {
			if got.Bitmap != nil {
				p.Symbol(got, box, tint)
			}
		})
	}, 100, 100)
	tt.SetScale(2)
	tt.SetSystemSymbols(func(name string, pointSize, scale float64) (image.Image, bool) {
		asked++
		if name != "disc" || pointSize != 13 || scale != 2 {
			return nil, false
		}
		// The size of a symbol in points is the pixels of what it paints at the scale of the window, without the room around them.
		m := image.NewAlpha(image.Rect(0, 0, 38, 24))
		for y := 2; y < 22; y++ {
			for x := 6; x < 36; x++ {
				m.SetAlpha(x, y, color.Alpha{A: 255})
			}
		}
		return m, true
	})
	tt.Frame()
	if missing.Bitmap != nil {
		t.Fatal("a symbol the system does not have has a mask")
	}
	if got.Bitmap == nil || got.W != 15 || got.H != 10 {
		t.Fatalf("the symbol is %+v", got)
	}
	img := tt.Image()
	at := func(x, y int) color.RGBA { return color.RGBAModel.Convert(img.At(x, y)).(color.RGBA) }
	// In the middle of the box at scale 2: x from 22.5 to 37.5, y from 25 to 35.
	if c := at(60, 60); c.R != tint.R || c.G != tint.G || c.B != tint.B {
		t.Fatalf("the middle of the symbol is %v", c)
	}
	if c := at(60, 40); c.R == tint.R {
		t.Fatalf("above the symbol is %v", c)
	}
	before := asked
	tt.Frame()
	tt.Frame()
	if asked != before {
		t.Fatalf("the frames asked the system again: %d after %d", asked, before)
	}
}

// Without a library of symbols, as in a window that has no system's, no symbol has a mask.
func TestSystemSymbolWithoutALibrary(t *testing.T) {
	var got Symbol
	tt := coreNewTester(func(c *context) { got = c.SystemSymbol("magnifyingglass", 13) }, 100, 100)
	tt.Frame()
	if got.Bitmap != nil {
		t.Fatal("a symbol without a library")
	}
}
