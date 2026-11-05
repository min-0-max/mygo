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
// The canvas shown only blurred keeps its two blurs alone, not a texture
// of its own.
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
	held := map[string]int{}
	for _, h := range r.heldTextures() {
		held[h.Name]++
	}
	if held["canvas"] != 2 || held["blurred image"] != 2 {
		t.Errorf("%d canvas textures and %d blurred images, want 2 and 2", held["canvas"], held["blurred image"])
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

// A canvas shown only blurred is blurred once while it does not change:
// the next frames keep the blurred image alone, without the canvas's
// texture or those of the passes, and show the same. A change of the
// canvas, or of the blur, draws the canvas and blurs it again.
func TestABlurKeptForACanvasFollowsItsChanges(t *testing.T) {
	o, err := NewOffscreen(64, 64)
	if err != nil {
		t.Skip("no Metal:", err)
	}
	defer o.Release()
	cv := scene.NewCanvas(64, 64)
	paint := func(c scene.Color) {
		cv.Scene.Ops = []scene.Op{{Kind: scene.OpFill, Rect: scene.Rect{W: 64, H: 64}, Color: c}}
		cv.Changed()
	}
	shown := func(blur float32, drawn bool) *scene.Scene {
		s := &scene.Scene{Width: 64, Height: 64, Ops: []scene.Op{{Kind: scene.OpImage, Rect: scene.Rect{W: 64, H: 64}, Image: cv, Src: scene.Rect{W: 64, H: 64}, Blur: blur}}}
		if drawn {
			s.Canvases = []*scene.Image{cv}
		}
		return s
	}
	center := func() [4]byte {
		pix, err := o.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		i := (32*64 + 32) * 4
		return [4]byte{pix[i], pix[i+1], pix[i+2], pix[i+3]}
	}
	held := func() map[string]int {
		n := map[string]int{}
		for _, h := range o.HeldTextures() {
			n[h.Name]++
		}
		return n
	}
	red, blue := scene.Color{R: 220, G: 30, B: 30, A: 255}, scene.Color{R: 30, G: 30, B: 220, A: 255}
	steps := []struct {
		name  string
		blur  float32
		drawn bool
		color *scene.Color
		bgra  [4]byte
		// canvas and passes are the textures held after the frame.
		canvas, passes int
	}{
		{"first", 4, true, &red, [4]byte{30, 30, 220, 255}, 1, 2},
		{"unchanged", 4, false, nil, [4]byte{30, 30, 220, 255}, 0, 0},
		{"unchanged again", 4, false, nil, [4]byte{30, 30, 220, 255}, 0, 0},
		{"canvas changed", 4, true, &blue, [4]byte{220, 30, 30, 255}, 1, 2},
		{"unchanged after the change", 4, false, nil, [4]byte{220, 30, 30, 255}, 0, 0},
		{"blur changed", 6, false, nil, [4]byte{220, 30, 30, 255}, 1, 2},
	}
	for _, st := range steps {
		if st.color != nil {
			paint(*st.color)
		}
		if _, err := o.Render(shown(st.blur, st.drawn)); err != nil {
			t.Fatal(err)
		}
		got := center()
		for i := range got {
			if d := int(got[i]) - int(st.bgra[i]); d > 2 || d < -2 {
				t.Errorf("%s: the center is BGRA %v, want %v", st.name, got, st.bgra)
				break
			}
		}
		h := held()
		if h["canvas"] != st.canvas || h["blur pass"] != st.passes || h["blurred image"] != 1 {
			t.Errorf("%s: %d canvas, %d pass and %d blurred image textures held, want %d, %d and 1", st.name, h["canvas"], h["blur pass"], h["blurred image"], st.canvas, st.passes)
		}
	}
}
