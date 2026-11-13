package ui

import (
	"image/color"
	"testing"
)

// TestTheImageOfAFrameIsConvertedOnce reads the last frame twice and
// after the next frame: the reads of one frame share the image that one
// conversion made, and a new frame gives a new image and leaves the image
// of the frame before it as it was.
func TestTheImageOfAFrameIsConvertedOnce(t *testing.T) {
	fill := color.RGBA{R: 200, A: 255}
	tt := NewTester(func(c *Context) {
		Box(c).Width(40).Height(40).Background(Color{R: fill.R, G: fill.G, B: fill.B, A: fill.A})
	}, 40, 40)
	first := tt.Image()
	if again := tt.Image(); again != first {
		t.Fatal("a second read of the same frame converted the frame again")
	}
	fill = color.RGBA{G: 200, A: 255}
	tt.Frame()
	next := tt.Image()
	if next == first {
		t.Fatal("the read after a new frame gave the image of the frame before it")
	}
	if got := first.RGBAAt(20, 20); got != (color.RGBA{R: 200, A: 255}) {
		t.Fatalf("the new frame changed the image of the frame before it to %v", got)
	}
	if got := next.RGBAAt(20, 20); got != fill {
		t.Fatalf("the image of the new frame is %v, want %v", got, fill)
	}
}
