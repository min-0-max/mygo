//go:build darwin

package metal

import (
	"runtime"
	"testing"

	"github.com/egoist/mygo/internal/gpu/gputest"
	"github.com/egoist/mygo/internal/scene"
)

// Canvases draw into textures as the CPU renderer draws them into pixels,
// once each: the second frame draws none anew, and shows them the same.
func TestCanvases(t *testing.T) {
	r, err := newRenderer()
	if err != nil {
		t.Skip("no Metal:", err)
	}
	defer r.Release()
	s := gputest.CanvasScene()
	for frame := range 2 {
		pix, err := r.renderOffscreen(s)
		if err != nil {
			t.Fatal(err)
		}
		gputest.Compare(t, "metal-canvas", pix, s.Width*4, s)
		if frame == 0 {
			s.Canvases = nil // drawn: only shown from now on
		}
	}
	if len(r.canvases) != 3 {
		t.Errorf("%d canvas textures, want 3", len(r.canvases))
	}
}

// A transient canvas's texture stays while frames draw it, and goes once
// they stop, though the canvas is still about; another canvas's stays
// until it is collected.
func TestTransientCanvas(t *testing.T) {
	r, err := newRenderer()
	if err != nil {
		t.Skip("no Metal:", err)
	}
	defer r.Release()
	layer, kept := scene.NewCanvas(64, 64), scene.NewCanvas(64, 64)
	layer.Transient = true
	s := &scene.Scene{Width: 64, Height: 64}
	s.Canvases = []*scene.Image{layer, kept}
	if _, err := r.renderOffscreen(s); err != nil {
		t.Fatal(err)
	}
	r.sweep()
	if r.canvases[layer.ID()] == nil || r.canvases[kept.ID()] == nil {
		t.Fatal("a sweep after the frame that drew them freed a canvas's texture")
	}
	r.frame++ // a frame that draws neither
	r.sweep()
	if r.canvases[layer.ID()] != nil || r.canvases[kept.ID()] == nil {
		t.Errorf("transient canvas kept %v, other kept %v", r.canvases[layer.ID()] != nil, r.canvases[kept.ID()] != nil)
	}
	runtime.KeepAlive(layer)
	runtime.KeepAlive(kept)
}

// What an offscreen renderer holds counts the texture of a canvas it drew,
// as the GPU allocated it, and reading its frame back gives the pixels
// drawn.
func TestOffscreenHoldsACanvasAndReadsItsFrame(t *testing.T) {
	o, err := NewOffscreen(64, 32)
	if err != nil {
		t.Skip("no Metal:", err)
	}
	defer o.Release()
	empty := &scene.Scene{Width: 64, Height: 32}
	if _, err := o.Render(empty); err != nil {
		t.Fatal(err)
	}
	before := o.HeldTextureBytes()
	cv := scene.NewCanvas(512, 256)
	cv.Scene.Ops = []scene.Op{{Kind: scene.OpFill, Rect: scene.Rect{W: 512, H: 256}, Color: scene.Color{R: 37, G: 99, B: 235, A: 255}}}
	s := &scene.Scene{Width: 64, Height: 32, Canvases: []*scene.Image{cv},
		Ops: []scene.Op{{Kind: scene.OpImage, Rect: scene.Rect{W: 64, H: 32}, Image: cv, Src: scene.Rect{W: 512, H: 256}}}}
	if _, err := o.Render(s); err != nil {
		t.Fatal(err)
	}
	if got, want := o.HeldTextureBytes()-before, 512*256*4; got < want {
		t.Errorf("the canvas added %d bytes held, want at least its %d", got, want)
	}
	pix, err := o.Pixels()
	if err != nil {
		t.Fatal(err)
	}
	if len(pix) != 64*32*4 {
		t.Fatalf("%d bytes read, want %d", len(pix), 64*32*4)
	}
	if b, g, r, a := pix[0], pix[1], pix[2], pix[3]; b != 235 || g != 99 || r != 37 || a != 255 {
		t.Errorf("first pixel BGRA %d %d %d %d, want 235 99 37 255", b, g, r, a)
	}
	runtime.KeepAlive(cv)
}
