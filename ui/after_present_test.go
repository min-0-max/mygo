package ui

import (
	"errors"
	"image"
	"slices"
	"testing"

	"github.com/egoist/mygo/internal/platform"
	"github.com/egoist/mygo/internal/scene"
	"github.com/egoist/mygo/internal/surface"
)

// presentations records what came of the frames a window host was asked
// about (windowHost.afterPresent), and the errors that came with it.
type presentations struct {
	got  []platform.Presentation
	errs []error
}

func (p *presentations) add(r platform.Presentation, err error) {
	p.got, p.errs = append(p.got, r), append(p.errs, err)
}

func (p *presentations) want(t *testing.T, what string, want ...platform.Presentation) {
	t.Helper()
	if !slices.Equal(p.got, want) {
		t.Fatalf("%s: answered %v, want %v", what, p.got, want)
	}
}

// TestAfterPresentPresented checks that a notice asks for a frame built
// anew and runs once, after that frame presented.
func TestAfterPresentPresented(t *testing.T) {
	o := newOccludedHost(t, false)
	o.frame()
	var p presentations
	asked, builds := o.s.requested, o.builds
	o.afterPresent(p.add)
	if o.s.requested == asked {
		t.Fatal("the notice asked for no frame")
	}
	shown := o.s.pixels
	o.frame()
	p.want(t, "after the frame", platform.Presented)
	if o.s.pixels != shown+1 || o.builds == builds {
		t.Fatalf("the frame presented %d times and built %d views", o.s.pixels-shown, o.builds-builds)
	}
	o.rt.changed()
	o.frame()
	p.want(t, "after the next frame", platform.Presented)
}

// TestAfterPresentHeld checks that the window holding its frames answers
// a notice so, once: asked for while it does, as the frame it waits for
// comes, and as the window goes out of sight before that frame came.
func TestAfterPresentHeld(t *testing.T) {
	o := newOccludedHost(t, false)
	o.frame()

	o.s.occluded = true
	var asked presentations
	o.afterPresent(asked.add)
	asked.want(t, "asked for out of sight", platform.PresentHeld)

	o.s.occluded = false
	var framed presentations
	o.afterPresent(framed.add)
	o.s.occluded = true
	o.frame()
	framed.want(t, "the frame out of sight", platform.PresentHeld)

	o.s.occluded = false
	o.event(platform.SurfaceEvent{Kind: platform.SurfaceShown})
	o.frame()
	var hidden presentations
	o.afterPresent(hidden.add)
	o.s.occluded = true
	o.event(platform.SurfaceEvent{Kind: platform.SurfaceHidden})
	hidden.want(t, "the window out of sight", platform.PresentHeld)
	o.s.occluded = false
	o.event(platform.SurfaceEvent{Kind: platform.SurfaceShown})
	o.frame()
	asked.want(t, "once shown again", platform.PresentHeld)
	framed.want(t, "once shown again", platform.PresentHeld)
	hidden.want(t, "once shown again", platform.PresentHeld)
}

// TestAfterPresentSurfaceHiddenIsNoInput checks that the window going out
// of sight is not input, which draws a frame even then.
func TestAfterPresentSurfaceHiddenIsNoInput(t *testing.T) {
	o := newOccludedHost(t, false)
	o.frame()
	o.s.occluded = true
	o.event(platform.SurfaceEvent{Kind: platform.SurfaceHidden})
	if o.rt.input {
		t.Fatal("SurfaceHidden counted as input")
	}
}

// TestAfterPresentDetached checks that a notice waiting as the window
// closes, whether for the frame the surface asks for or held by the GPU
// renderer, is answered so, as is one asked for once it closed.
func TestAfterPresentDetached(t *testing.T) {
	o := newOccludedHost(t, false)
	o.frame()
	var p presentations
	o.afterPresent(p.add)
	o.detach()
	p.want(t, "closed before the frame", platform.PresentClosed)
	var late presentations
	o.afterPresent(late.add)
	late.want(t, "asked for once closed", platform.PresentClosed)

	g := &noticeGPU{hold: true}
	h, _, frame := gpuHost(t, func() (gpuRenderer, error) { return g, nil })
	frame()
	var held presentations
	h.afterPresent(held.add)
	frame()
	held.want(t, "while the GPU renderer holds the frame")
	h.detach()
	held.want(t, "closed while the GPU renderer held the frame", platform.PresentClosed)
}

// damageSurface is a test surface presenting the parts of frames that
// changed.
type damageSurface struct {
	testSurface
	damage [][]image.Rectangle
}

func (s *damageSurface) PresentDamage(_ []byte, _, _, _ int, damage []image.Rectangle) {
	s.damage = append(s.damage, damage)
}

// TestAfterPresentNoDamageInMemory checks that a frame drawn in memory
// that changes nothing, presenting nothing, is answered so.
func TestAfterPresentNoDamageInMemory(t *testing.T) {
	t.Setenv("MYGO_GPU", "0")
	s := &damageSurface{}
	h := &windowHost{conn: &surface.Conn{Surface: s}}
	h.rt = newRuntime(func(c *context) { coreText(c, "Hello") }, h)
	frame := func() { h.event(platform.SurfaceEvent{Kind: platform.SurfaceFrame}) }
	frame()
	var p presentations
	h.afterPresent(p.add)
	frame()
	p.want(t, "a frame changing nothing", platform.PresentNoDamage)
}

// noticeGPU is a GPU renderer that tells what came of each frame, with
// err, which it fails to draw while fail is set. While hold is set, it
// holds the frame's notice, as Metal's does a frame waiting for its
// drawable, until it is let go, which hands the notice on (again).
type noticeGPU struct {
	testGPU
	answer platform.Presentation
	err    error
	hold   bool
	again  func()
}

func (g *noticeGPU) RenderNotice(s *scene.Scene, done func(platform.Presentation, error), again func()) error {
	if g.fail {
		return errors.New("the device was removed")
	}
	g.frames++
	if g.hold {
		g.again = again
		return nil
	}
	done(g.answer, g.err)
	return nil
}

func (g *noticeGPU) Release() {
	g.released++
	if again := g.again; again != nil {
		g.again = nil
		again()
	}
}

// TestAfterPresentOnTheGPU checks that a GPU renderer that tells what came
// of a frame answers its notice, one that does not answers it once it
// drew, and a notice whose frame a renderer failed to draw goes with the
// frame drawn in its place.
func TestAfterPresentOnTheGPU(t *testing.T) {
	failed := errors.New("the command buffer failed")
	g := &noticeGPU{answer: platform.PresentFailed, err: failed}
	h, _, frame := gpuHost(t, func() (gpuRenderer, error) { return g, nil })
	frame()
	var told presentations
	h.afterPresent(told.add)
	frame()
	told.want(t, "a renderer telling", platform.PresentFailed)
	if told.errs[0] != failed {
		t.Fatalf("a renderer telling: error %v", told.errs[0])
	}

	plain := &testGPU{}
	h2, _, frame2 := gpuHost(t, func() (gpuRenderer, error) { return plain, nil })
	frame2()
	var drew presentations
	h2.afterPresent(drew.add)
	frame2()
	drew.want(t, "a renderer not telling", platform.Presented)

	failing := &noticeGPU{answer: platform.Presented}
	made := 0
	h3, s3, frame3 := gpuHost(t, func() (gpuRenderer, error) {
		made++
		if made == 1 {
			return failing, nil
		}
		return nil, errors.New("no device")
	})
	frame3()
	failing.fail = true
	var lost presentations
	h3.afterPresent(lost.add)
	shown := s3.pixels
	frame3()
	lost.want(t, "the frame drawn in memory in place of the GPU's", platform.Presented)
	if s3.pixels != shown+1 {
		t.Fatalf("%d frames drawn in memory", s3.pixels-shown)
	}
}

// TestAfterPresentHandedOn checks that a notice whose frame the GPU
// renderer dropped, as it is let go, goes with the next frame, which it
// asks for, and which answers it.
func TestAfterPresentHandedOn(t *testing.T) {
	g := &noticeGPU{hold: true}
	h, _, frame := gpuHost(t, func() (gpuRenderer, error) { return g, nil })
	frame()
	var p presentations
	h.afterPresent(p.add)
	frame()
	g.hold, g.answer = false, platform.Presented
	h.frameAsked = false
	g.Release()
	p.want(t, "dropped by the renderer")
	if !h.frameAsked {
		t.Fatal("dropped by the renderer: no frame asked for")
	}
	frame()
	p.want(t, "the next frame", platform.Presented)
}
