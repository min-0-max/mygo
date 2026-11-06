//go:build darwin

package metal

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/internal/platform"
	"github.com/egoist/mygo/internal/scene"
	"github.com/egoist/mygo/internal/wait"
)

// The states of a command buffer (MTLCommandBufferStatus).
const (
	cbCommitted = 2
	cbScheduled = 3
)

// layerRenderer returns a renderer drawing into a layer of its own.
func layerRenderer(t *testing.T) *Renderer {
	t.Helper()
	if err := load(); err != nil {
		t.Skip("no Metal:", err)
	}
	var layer id
	pool(func() { layer = sendMain(class("CALayer"), "new") })
	if layer == 0 {
		t.Skip("no Core Animation")
	}
	t.Cleanup(func() { pool(func() { release(&layer) }) })
	r, err := New(uintptr(layer))
	if err != nil {
		unavailable(t, err)
	}
	t.Cleanup(r.Release)
	return r
}

// squareAt returns a scene of 64×32 pixels with a square at x.
func squareAt(x int) *scene.Scene {
	s := &scene.Scene{}
	s.Reset(64, 32, scene.Color{A: 255})
	s.Ops = append(s.Ops, scene.Op{Kind: scene.OpFill, Rect: scene.Rect{X: float32(x), W: 8, H: 8}, Color: scene.Color{R: 255, A: 255}})
	return s
}

// noticed records what came of the frames it was given to, the errors
// that came with it, and how many times the frames handed the notice on
// to the next (again).
type noticed struct {
	got    []platform.Presentation
	errs   []error
	agains int
}

func (n *noticed) add(p platform.Presentation, err error) {
	n.got, n.errs = append(n.got, p), append(n.errs, err)
}

func (n *noticed) again() { n.agains++ }

func (n *noticed) want(t *testing.T, what string, want ...platform.Presentation) {
	t.Helper()
	if !slices.Equal(n.got, want) {
		t.Fatalf("%s: noticed %v, want %v", what, n.got, want)
	}
}

// render draws s with notice n, which nil leaves out.
func render(t *testing.T, r *Renderer, s *scene.Scene, n *noticed) {
	t.Helper()
	var err error
	if n == nil {
		err = r.Render(s)
	} else {
		err = r.RenderNotice(s, n.add, n.again)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestAfterPresentRunsAfterThePresent checks that the notice of a frame
// presented as soon as drawn runs once, after its command buffer, which
// presents its drawable, was committed and the presenter went on to the
// next frame's drawable.
func TestAfterPresentRunsAfterThePresent(t *testing.T) {
	r := layerRenderer(t)
	if err := r.Render(squareAt(0)); err != nil { // changes the layer
		t.Fatal(err)
	}
	var n noticed
	var status uintptr
	var next bool
	err := r.RenderNotice(squareAt(1), func(p platform.Presentation, err error) {
		n.add(p, err)
		status = send(r.inflight[r.inUse], "status")
		r.pre.mu.Lock()
		next = r.pre.fetching || r.pre.drawable != 0
		r.pre.mu.Unlock()
	}, n.again)
	if err != nil {
		t.Fatal(err)
	}
	if r.synced {
		t.Fatal("the frame presented with the transaction")
	}
	n.want(t, "a frame presented", platform.Presented)
	if status < cbCommitted || !next {
		t.Fatalf("in the notice: command buffer status %d, next drawable being fetched %v: before the present", status, next)
	}
}

// TestAfterPresentInsideTheTransaction checks that the notice of a frame
// presenting with the Core Animation transaction runs once its drawable's
// present ran, inside the transaction, before it commits.
func TestAfterPresentInsideTheTransaction(t *testing.T) {
	r := layerRenderer(t)
	tx := class("CATransaction")
	pool(func() { sendMain(tx, "begin") })
	committed := false
	var n noticed
	var open bool
	var status uintptr
	err := r.RenderNotice(squareAt(0), func(p platform.Presentation, err error) { // changes the layer
		n.add(p, err)
		open = !committed
		status = send(r.inflight[r.inUse], "status")
	}, n.again)
	pool(func() { sendMain(tx, "commit") })
	committed = true
	if err != nil {
		t.Fatal(err)
	}
	if !r.synced {
		t.Fatal("the frame changing the layer did not present with the transaction")
	}
	n.want(t, "a frame presented with the transaction", platform.Presented)
	if !open || status < cbScheduled {
		t.Fatalf("in the notice: transaction open %v, command buffer status %d", open, status)
	}
}

// TestAfterPresentNoDamage checks that a scene changing nothing of the
// last frame is answered so, at once.
func TestAfterPresentNoDamage(t *testing.T) {
	r := layerRenderer(t)
	if err := r.Render(squareAt(0)); err != nil {
		t.Fatal(err)
	}
	var n noticed
	render(t, r, squareAt(0), &n)
	n.want(t, "the same scene again", platform.PresentNoDamage)
}

// TestAfterPresentNoDrawable checks that a frame finding no drawable, as
// when the display holds them all past nextDrawable's second, is answered
// so.
func TestAfterPresentNoDrawable(t *testing.T) {
	r := layerRenderer(t)
	if err := r.Render(squareAt(0)); err != nil {
		t.Fatal(err)
	}
	take := frameDrawable
	frameDrawable = func(id) id { return 0 }
	defer func() { frameDrawable = take }()
	var n noticed
	render(t, r, squareAt(1), &n)
	n.want(t, "with no drawable", platform.PresentNoDrawable)
}

// pendingRenderer returns a renderer whose next frame waits for its
// drawable, which the presenter fetches once gate gives way, and the
// channel of what it posts to the main thread. The frames before it must
// have presented: a process that made all the drawables it can, about
// 16383, gets none, from any layer, even once it let them go, and its
// frames would not wait.
func pendingRenderer(t *testing.T) (r *Renderer, gate chan struct{}, posted chan func()) {
	t.Helper()
	r = layerRenderer(t)
	posted = make(chan func(), 4)
	r.SetPost(func(fn func()) { posted <- fn })
	gate = make(chan struct{})
	next := nextDrawable
	nextDrawable = func(l id) id { <-gate; return next(l) }
	t.Cleanup(func() { nextDrawable = next })
	var first noticed
	render(t, r, squareAt(0), &first) // with the transaction: no fetch after
	render(t, r, squareAt(1), &first) // takes its drawable, then the presenter fetches the next
	first.want(t, "the frames before", platform.Presented, platform.Presented)
	return r, gate, posted
}

// TestAfterPresentPendingFrames checks that the notice of a frame waiting
// for its drawable runs once the frame drew into it and presented.
func TestAfterPresentPendingFrames(t *testing.T) {
	r, gate, posted := pendingRenderer(t)
	defer close(gate)
	var drawn noticed
	render(t, r, squareAt(2), &drawn)
	if r.pending.cb == 0 {
		t.Fatal("the frame did not wait for the drawable being fetched")
	}
	drawn.want(t, "while the frame waits for its drawable")
	gate <- struct{}{} // the fetch ends
	wait.For(t, "the post", posted)()
	drawn.want(t, "once its drawable came", platform.Presented)
	if drawn.agains != 0 {
		t.Fatalf("handed on %d times", drawn.agains)
	}
}

// TestAfterPresentPendingDropped checks that a frame waiting for its
// drawable that is dropped, as the renderer trims its drawables or is let
// go, hands its notice on to the frame drawn next, which shows what it
// would have, and answers nothing itself.
func TestAfterPresentPendingDropped(t *testing.T) {
	r, gate, _ := pendingRenderer(t)
	var dropped noticed
	render(t, r, squareAt(2), &dropped)
	if r.pending.cb == 0 {
		t.Fatal("the frame did not wait for the drawable being fetched")
	}
	pool(r.dropPending)
	close(gate)
	r.pre.drain()
	if dropped.agains != 1 || len(dropped.got) != 0 {
		t.Fatalf("dropped before its drawable came: handed on %d times, answered %v", dropped.agains, dropped.got)
	}
}

// TestAfterPresentFailed checks that a frame waiting for its drawable that
// fails to draw into it is answered so, with the error.
func TestAfterPresentFailed(t *testing.T) {
	r, gate, posted := pendingRenderer(t)
	defer close(gate)
	var failed noticed
	render(t, r, squareAt(2), &failed)
	if r.pending.cb == 0 {
		t.Fatal("the frame did not wait for the drawable being fetched")
	}
	encoder := renderEncoder
	renderEncoder = func(id, id) id { return 0 }
	defer func() { renderEncoder = encoder }()
	r.checked = false
	gate <- struct{}{}
	wait.For(t, "the post", posted)()
	failed.want(t, "the frame failing to draw", platform.PresentFailed)
	if err := failed.errs[0]; err == nil || !strings.Contains(err.Error(), "no render encoder") {
		t.Fatalf("the frame failing to draw: error %v", err)
	}
}
