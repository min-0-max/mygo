//go:build darwin

package metal

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/internal/gpu"
	"github.com/egoist/mygo/internal/platform"
	"github.com/egoist/mygo/internal/scene"
	"github.com/egoist/mygo/internal/wait"
)

// TestPresenter checks the presenter's fetches: a frame is not ready while
// one is in flight, the presenter wakes the window once it has the
// drawable, and taking or draining one waits for a fetch in flight.
func TestPresenter(t *testing.T) {
	if err := load(); err != nil {
		t.Skip("no Objective-C runtime:", err)
	}
	gate := make(chan struct{})
	fetched := 0
	nextDrawable = func(id) id {
		<-gate
		fetched++
		return send(send(class("NSMutableString"), "new"), "autorelease")
	}
	defer func() { nextDrawable = func(layer id) id { return send(layer, "nextDrawable") } }()
	var p presenter
	defer p.stop()

	if !p.ready(nil) {
		t.Fatal("not ready before any fetch")
	}
	p.start(0)
	woke := make(chan struct{})
	if p.ready(func() { close(woke) }) {
		t.Fatal("ready while a fetch is in flight")
	}
	gate <- struct{}{}
	wait.For(t, "the presenter's wake", woke)
	if !p.ready(nil) {
		t.Fatal("not ready once fetched")
	}
	// A fetched drawable is kept until taken: starting again fetches none.
	p.start(0)
	d := p.take()
	if d == 0 || fetched != 1 {
		t.Fatalf("took %#x after %d fetches", d, fetched)
	}
	pool(func() { send(d, "release") })

	// Taking waits for the fetch in flight, as draining does.
	p.start(0)
	go func() { gate <- struct{}{} }()
	if d = p.take(); d == 0 || fetched != 2 {
		t.Fatalf("took %#x after %d fetches", d, fetched)
	}
	pool(func() { send(d, "release") })
	p.start(0)
	go func() { gate <- struct{}{} }()
	p.drain()
	if fetched != 3 || p.drawable != 0 || p.fetching {
		t.Fatalf("drained after %d fetches: drawable %#x, fetching %v", fetched, p.drawable, p.fetching)
	}
}

// TestPrefetchedFrames renders frames following each other into a layer:
// the first, changing the layer, takes its drawable on the main thread;
// each after presents as soon as drawn and has the presenter fetch the
// next frame's, which that frame takes; a frame resizing the layer drains
// it.
func TestPrefetchedFrames(t *testing.T) {
	if err := load(); err != nil {
		t.Skip("no Metal:", err)
	}
	var layer id
	pool(func() { layer = sendMain(class("CALayer"), "new") })
	if layer == 0 {
		t.Skip("no Core Animation")
	}
	defer pool(func() { release(&layer) })
	r, err := New(uintptr(layer))
	if err != nil {
		unavailable(t, err)
	}
	defer r.Release()
	var fetched int
	next := nextDrawable
	nextDrawable = func(l id) id { fetched++; return next(l) }
	defer func() { nextDrawable = next }()
	frame := func(w, i int) {
		t.Helper()
		s := &scene.Scene{}
		s.Reset(w, 32, scene.Color{A: 255})
		s.Ops = append(s.Ops, scene.Op{Kind: scene.OpFill, Rect: scene.Rect{X: float32(i), W: 8, H: 8}, Color: scene.Color{R: 255, A: 255}})
		r.pre.wait()
		parked := drawableID(r.pre.drawable)
		if err := r.Render(s); err != nil {
			t.Fatal(err)
		}
		r.pre.wait()
		if parked != 0 && drawableID(r.pre.drawable) == parked {
			t.Fatalf("frame %d did not take the drawable fetched for it", i)
		}
		if !r.pre.ready(nil) {
			t.Fatalf("frame %d: not ready after the fetch", i)
		}
	}
	frame(64, 0)
	if r.synced == false || r.pre.started {
		t.Fatalf("the first frame: synced %v, presenter started %v", r.synced, r.pre.started)
	}
	for i := 1; i <= 5; i++ {
		frame(64, i)
		if r.pre.drawable == 0 {
			t.Fatalf("frame %d: no drawable fetched for the next", i)
		}
	}
	// The presenter fetched after each frame but the first, which
	// presented with the transaction.
	if fetched != 5 {
		t.Errorf("%d fetches after 6 frames, want 5", fetched)
	}
	frame(96, 6)
	if r.pre.drawable != 0 || !r.synced {
		t.Errorf("a resize: drawable fetched %#x, synced %v", r.pre.drawable, r.synced)
	}
}

// TestPendingFrames renders a frame while its drawable is being fetched:
// it is built at once and waits, frames paced by the display are not
// ready meanwhile, and the presenter has it drawn on the main thread
// (post) once it has the drawable; a frame not paced, as a resize's,
// draws the pending one first.
func TestPendingFrames(t *testing.T) {
	if err := load(); err != nil {
		t.Skip("no Metal:", err)
	}
	var layer id
	pool(func() { layer = sendMain(class("CALayer"), "new") })
	if layer == 0 {
		t.Skip("no Core Animation")
	}
	defer pool(func() { release(&layer) })
	r, err := New(uintptr(layer))
	if err != nil {
		unavailable(t, err)
	}
	defer r.Release()
	posted := make(chan func(), 4)
	r.SetPost(func(fn func()) { posted <- fn })
	gate := make(chan struct{})
	next := nextDrawable
	nextDrawable = func(l id) id { <-gate; return next(l) }
	defer func() { nextDrawable = next }()
	frame := func(w, i int) {
		t.Helper()
		s := &scene.Scene{}
		s.Reset(w, 32, scene.Color{A: 255})
		s.Ops = append(s.Ops, scene.Op{Kind: scene.OpFill, Rect: scene.Rect{X: float32(i), W: 8, H: 8}, Color: scene.Color{R: 255, A: 255}})
		if err := r.Render(s); err != nil {
			t.Fatal(err)
		}
	}
	frame(64, 0) // with the transaction: no fetch after
	frame(64, 1) // takes its drawable, then the presenter fetches the next
	if !r.pre.fetching {
		t.Fatal("no drawable being fetched after a frame presented as soon as drawn")
	}
	frame(64, 2) // built, waiting for the fetch
	if r.pending.cb == 0 {
		t.Fatal("the frame did not wait for the drawable being fetched")
	}
	woke := false
	if r.Ready(func() { woke = true }) {
		t.Fatal("ready while a frame waits")
	}
	gate <- struct{}{} // the fetch ends
	finish := wait.For(t, "the post", posted)
	finish()
	if r.pending.cb != 0 || !woke {
		t.Fatalf("after the drawable came: pending %v, woke %v", r.pending.cb != 0, woke)
	}
	if !r.Ready(nil) {
		t.Fatal("not ready once drawn")
	}
	// A frame waiting is drawn first by a frame not paced, as a resize's.
	frame(64, 3)
	if r.pending.cb == 0 {
		t.Fatal("the frame did not wait")
	}
	go func() { gate <- struct{}{} }()
	frame(96, 4)
	if r.pending.cb != 0 || !r.synced || r.pre.fetching {
		t.Fatalf("a resize: pending %v, synced %v, fetching %v", r.pending.cb != 0, r.synced, r.pre.fetching)
	}
	close(gate)
	// The post of the fetch the resize waited for finds nothing pending.
	select {
	case fn := <-posted:
		fn()
	default:
	}
}

// drawableID returns the identity of drawable d, or 0 for none: its
// address may come back for another.
func drawableID(d id) uintptr {
	if d == 0 {
		return 0
	}
	return sendMain(d, "drawableID")
}

// TestDroppedPendingFrame checks that a frame waiting for its drawable,
// dropped as the drawables are trimmed or the renderer let go, wakes what
// waited for it (Ready): the frame paced by the display waiting draws
// then, rather than never.
func TestDroppedPendingFrame(t *testing.T) {
	if err := load(); err != nil {
		t.Skip("no Metal:", err)
	}
	var layer id
	pool(func() { layer = sendMain(class("CALayer"), "new") })
	if layer == 0 {
		t.Skip("no Core Animation")
	}
	defer pool(func() { release(&layer) })
	r, err := New(uintptr(layer))
	if err != nil {
		unavailable(t, err)
	}
	defer r.Release()
	r.SetPost(func(func()) {})
	gate := make(chan struct{})
	next := nextDrawable
	nextDrawable = func(l id) id { <-gate; return next(l) }
	defer func() { nextDrawable = next }()
	frame := func(i int) {
		t.Helper()
		s := &scene.Scene{}
		s.Reset(64, 32, scene.Color{A: 255})
		s.Ops = append(s.Ops, scene.Op{Kind: scene.OpFill, Rect: scene.Rect{X: float32(i), W: 8, H: 8}, Color: scene.Color{R: 255, A: 255}})
		if err := r.Render(s); err != nil {
			t.Fatal(err)
		}
	}
	frame(0) // with the transaction: no fetch after
	frame(1) // takes its drawable, then the presenter fetches the next
	frame(2) // built, waiting for the fetch
	if r.pending.cb == 0 {
		t.Fatal("the frame did not wait for the drawable being fetched")
	}
	woke := false
	if r.Ready(func() { woke = true }) {
		t.Fatal("ready while a frame waits")
	}
	pool(r.dropPending)
	if r.pending.cb != 0 || !woke {
		t.Fatalf("dropped: pending %v, woke %v", r.pending.cb != 0, woke)
	}
	close(gate)
	r.pre.drain()
}

// TestStalePost checks that the post of a fetch whose frame pending a
// frame not paced drew meanwhile neither blocks the main thread for the
// next frame's drawable, being fetched, nor draws that frame before it
// comes: the next fetch posts it.
func TestStalePost(t *testing.T) {
	if err := load(); err != nil {
		t.Skip("no Metal:", err)
	}
	var layer id
	pool(func() { layer = sendMain(class("CALayer"), "new") })
	if layer == 0 {
		t.Skip("no Core Animation")
	}
	defer pool(func() { release(&layer) })
	r, err := New(uintptr(layer))
	if err != nil {
		unavailable(t, err)
	}
	defer r.Release()
	posted := make(chan func(), 4)
	r.SetPost(func(fn func()) { posted <- fn })
	gate := make(chan struct{})
	next := nextDrawable
	nextDrawable = func(l id) id { <-gate; return next(l) }
	defer func() { nextDrawable = next }()
	defer close(gate)
	frame := func(i int) {
		t.Helper()
		s := &scene.Scene{}
		s.Reset(64, 32, scene.Color{A: 255})
		s.Ops = append(s.Ops, scene.Op{Kind: scene.OpFill, Rect: scene.Rect{X: float32(i), W: 8, H: 8}, Color: scene.Color{R: 255, A: 255}})
		if err := r.Render(s); err != nil {
			t.Fatal(err)
		}
	}
	frame(0) // with the transaction: no fetch after
	frame(1) // takes its drawable, then the presenter fetches the next
	frame(2) // built, waiting for the fetch
	gate <- struct{}{}
	stale := wait.For(t, "the post", posted)
	// A frame not paced draws the one pending, the presenter fetches the
	// next drawable, and this frame waits for it.
	frame(3)
	if r.pending.cb == 0 || !r.pre.fetching {
		t.Fatalf("pending %v, fetching %v", r.pending.cb != 0, r.pre.fetching)
	}
	stale() // would wait for the fetch, the gate closed
	if r.pending.cb == 0 {
		t.Fatal("the stale post drew the frame pending")
	}
	gate <- struct{}{}
	finish := wait.For(t, "the post of the next fetch", posted)
	finish()
	if r.pending.cb != 0 {
		t.Fatal("the frame pending was not drawn once its drawable came")
	}
}

// commits keeps the commits that a renderer reports outside its frames, and
// hears each command buffer complete, which Metal reports on a thread of its
// own, so the clock counts atomically.
type commits struct {
	kinds     []gpu.CommitKind
	times     []uint64
	clock     atomic.Uint64
	completed chan [2]uint64
}

func (c *commits) watch(r *Renderer) {
	c.completed = make(chan [2]uint64, 16)
	r.ReportFrames(func() uint64 { return c.clock.Add(1) }, func(kind gpu.CommitKind, at uint64) {
		c.kinds, c.times = append(c.kinds, kind), append(c.times, at)
	}, func(committed, completed uint64) { c.completed <- [2]uint64{committed, completed} })
}

// complete checks that the next command buffer that completes is the one
// committed at committed, and that it completed after it committed.
func (c *commits) complete(t *testing.T, what string, committed uint64) {
	t.Helper()
	got := wait.For(t, what+" completes", c.completed)
	if got[0] != committed || got[1] <= committed {
		t.Errorf("%s committed at %d reported the completion %v", what, committed, got)
	}
}

// TestFrameFacts checks what a renderer tells of each kind of frame: one that
// presents with the transaction, one at once, one that changed nothing, and one
// that waits for its drawable, whose command buffer commits outside its frame.
func TestFrameFacts(t *testing.T) {
	if err := load(); err != nil {
		t.Skip("no Metal:", err)
	}
	var layer id
	pool(func() { layer = sendMain(class("CALayer"), "new") })
	if layer == 0 {
		t.Skip("no Core Animation")
	}
	defer pool(func() { release(&layer) })
	r, err := New(uintptr(layer))
	if err != nil {
		unavailable(t, err)
	}
	defer r.Release()
	var c commits
	c.watch(r)
	posted := make(chan func(), 4)
	r.SetPost(func(fn func()) { posted <- fn })
	gate := make(chan struct{})
	next := nextDrawable
	nextDrawable = func(l id) id { <-gate; return next(l) }
	defer func() { nextDrawable = next }()
	defer close(gate)
	frame := func(i int) gpu.FrameFacts {
		t.Helper()
		s := &scene.Scene{}
		s.Reset(64, 32, scene.Color{A: 255})
		s.Ops = append(s.Ops, scene.Op{Kind: scene.OpFill, Rect: scene.Rect{X: float32(i), W: 8, H: 8}, Color: scene.Color{R: 255, A: 255}})
		if err := r.Render(s); err != nil {
			t.Fatal(err)
		}
		return r.FrameFacts()
	}
	f := frame(0)
	if f.Outcome != gpu.FrameTransaction || !f.CommandBuffer || f.Committed == 0 || f.Request < f.Committed || f.Damage != 64*32 {
		t.Errorf("a frame that changes the layer: %+v", f)
	}
	c.complete(t, "the frame that changes the layer", f.Committed)
	f = frame(1)
	if f.Outcome != gpu.FrameImmediate || !f.CommandBuffer || f.Request == 0 || f.Committed < f.Request || f.Damage == 0 || f.Damage >= 64*32 {
		t.Errorf("a frame that presents at once: %+v", f)
	}
	c.complete(t, "the frame that presents at once", f.Committed)
	f = frame(1)
	if f.Outcome != gpu.FrameUndamaged || f.CommandBuffer || f.Committed != 0 || f.Request != 0 || f.Damage != 0 {
		t.Errorf("a frame that changed nothing: %+v", f)
	}
	if !r.pre.fetching {
		t.Fatal("no drawable being fetched")
	}
	f = frame(2)
	if f.Outcome != gpu.FrameStashed || f.CommandBuffer || f.Committed != 0 || f.Request != 0 || f.Damage == 0 {
		t.Errorf("a frame that waits for its drawable: %+v", f)
	}
	if len(c.kinds) != 0 {
		t.Fatalf("commits %v before the drawable came", c.kinds)
	}
	gate <- struct{}{}
	wait.For(t, "the post", posted)()
	if len(c.kinds) != 1 || c.kinds[0] != gpu.CommitStashed || c.times[0] == 0 {
		t.Fatalf("the frame that waited committed %v at %v", c.kinds, c.times)
	}
	c.complete(t, "the frame that waited", c.times[0])
}

// TestDrawableWaitOfAFrame checks that a frame tells how long it waited for its
// drawable, on the clock of the report.
func TestDrawableWaitOfAFrame(t *testing.T) {
	if err := load(); err != nil {
		t.Skip("no Metal:", err)
	}
	var layer id
	pool(func() { layer = sendMain(class("CALayer"), "new") })
	if layer == 0 {
		t.Skip("no Core Animation")
	}
	defer pool(func() { release(&layer) })
	r, err := New(uintptr(layer))
	if err != nil {
		unavailable(t, err)
	}
	defer r.Release()
	var c commits
	c.watch(r)
	s := &scene.Scene{}
	s.Reset(64, 32, scene.Color{A: 255})
	if err := r.Render(s); err != nil {
		t.Fatal(err)
	}
	if f := r.FrameFacts(); f.DrawableWait == 0 {
		t.Errorf("the frame read no wait for its drawable: %+v", f)
	}
}

// TestPixelsCommitOutsideAFrame checks that a render to pixels reports its
// commit.
func TestPixelsCommitOutsideAFrame(t *testing.T) {
	r, err := newRenderer()
	if err != nil {
		t.Skip("no Metal:", err)
	}
	defer r.Release()
	var c commits
	c.watch(r)
	s := &scene.Scene{}
	s.Reset(64, 32, scene.Color{A: 255})
	if _, err := r.renderOffscreen(s); err != nil {
		t.Fatal(err)
	}
	if len(c.kinds) != 1 || c.kinds[0] != gpu.CommitPixels || c.times[0] == 0 {
		t.Fatalf("a render to pixels committed %v at %v", c.kinds, c.times)
	}
	c.complete(t, "the render to pixels", c.times[0])
}

// TestAFrameAloneFetchesNoDrawable draws a frame after a pause, as a key or
// a click does: the display still shows the last frame and the GPU has this
// one, so a fetch would have the system make another drawable, which would
// only hold memory until the window idles. A frame following it fetches.
func TestAFrameAloneFetchesNoDrawable(t *testing.T) {
	if err := load(); err != nil {
		t.Skip("no Metal:", err)
	}
	var layer id
	pool(func() { layer = sendMain(class("CALayer"), "new") })
	if layer == 0 {
		t.Skip("no Core Animation")
	}
	defer pool(func() { release(&layer) })
	r, err := New(uintptr(layer))
	if err != nil {
		unavailable(t, err)
	}
	defer r.Release()
	refreshes := followRefreshes
	followRefreshes = 2
	defer func() { followRefreshes = refreshes }()
	// A refresh a second: however long a frame takes here, the next follows
	// it.
	r.SetRefreshRate(func() float64 { return 1 })
	var fetched int
	next := nextDrawable
	nextDrawable = func(l id) id { fetched++; return next(l) }
	defer func() { nextDrawable = next }()
	frame := func(i int) {
		t.Helper()
		s := &scene.Scene{}
		s.Reset(64, 32, scene.Color{A: 255})
		s.Ops = append(s.Ops, scene.Op{Kind: scene.OpFill, Rect: scene.Rect{X: float32(i), W: 8, H: 8}, Color: scene.Color{R: 255, A: 255}})
		if err := r.Render(s); err != nil {
			t.Fatal(err)
		}
		r.pre.wait()
	}
	frame(0)
	if !r.synced {
		t.Fatal("the first frame did not present with the transaction")
	}
	r.lastRender = time.Now().Add(-time.Minute)
	frame(1)
	if r.synced {
		t.Fatal("the frame alone presented with the transaction")
	}
	if fetched != 0 || r.pre.drawable != 0 {
		t.Errorf("the frame alone fetched %d drawables, holding %#x", fetched, r.pre.drawable)
	}
	frame(2)
	if fetched != 1 || r.pre.drawable == 0 {
		t.Errorf("the frame following it fetched %d drawables, holding %#x", fetched, r.pre.drawable)
	}
}

// TestFramesFollowByTheRefresh checks that a frame follows the last by the
// display's refreshes, so that it means the same at any rate, and that a
// display of no known rate counts as a 60 Hz one.
func TestFramesFollowByTheRefresh(t *testing.T) {
	refreshes := followRefreshes
	followRefreshes = 2
	defer func() { followRefreshes = refreshes }()
	for _, c := range []struct {
		hz      float64
		since   time.Duration
		follows bool
	}{
		{60, 30 * time.Millisecond, true},
		{60, 40 * time.Millisecond, false},
		{120, 15 * time.Millisecond, true},
		{120, 20 * time.Millisecond, false},
		{0, 30 * time.Millisecond, true},
		{0, 40 * time.Millisecond, false},
	} {
		r := &Renderer{}
		r.SetRefreshRate(func() float64 { return c.hz })
		if got := r.follows(c.since); got != c.follows {
			t.Errorf("%v Hz, %v after the last frame: follows %v, want %v", c.hz, c.since, got, c.follows)
		}
	}
	r := &Renderer{}
	if !r.follows(30*time.Millisecond) || r.follows(40*time.Millisecond) {
		t.Error("a renderer without a rate does not count the display as a 60 Hz one")
	}
}

// TestAThirdDrawableAwaitsFramesFollowing checks that the layer keeps two
// drawables through the few frames that opening a view draws in a row, so
// that they cost no third drawable, gets a third once frames keep following,
// and goes back to two with the idle trim.
func TestAThirdDrawableAwaitsFramesFollowing(t *testing.T) {
	r := layerRenderer(t)
	refreshes := followRefreshes
	followRefreshes = 2
	defer func() { followRefreshes = refreshes }()
	// A refresh a second: however long a frame takes here, the next follows
	// it.
	r.SetRefreshRate(func() float64 { return 1 })
	count := func() (n id) {
		pool(func() { n = sendMain(r.layer, "maximumDrawableCount") })
		return n
	}
	frame := func(i int) {
		t.Helper()
		render(t, r, squareAt(i), nil)
		r.pre.wait()
	}
	frame(0)
	for i := 1; i < framesForThird; i++ {
		frame(i)
		if n := count(); n != 2 {
			t.Fatalf("after %d frames following, the layer allows %d drawables, want 2", i, n)
		}
	}
	frame(framesForThird)
	if n := count(); n != 3 {
		t.Fatalf("after %d frames following, the layer allows %d drawables, want 3", framesForThird, n)
	}
	r.trim()
	if n := count(); n != 2 {
		t.Fatalf("after the idle trim, the layer allows %d drawables, want 2", n)
	}
	r.lastRender = time.Now().Add(-time.Minute)
	frame(0)
	frame(1)
	if n := count(); n != 2 {
		t.Fatalf("the count of frames following outlived the trim: the layer allows %d drawables, want 2", n)
	}
}

// takenDrawables counts the drawables that frames take on the main thread.
func takenDrawables(t *testing.T) *int {
	t.Helper()
	taken := new(int)
	take := frameDrawable
	frameDrawable = func(l id) id { *taken++; return take(l) }
	t.Cleanup(func() { frameDrawable = take })
	return taken
}

// transactAt returns squareAt(x) moving the views under its holes, which
// presents with the transaction.
func transactAt(x int) *scene.Scene {
	s := squareAt(x)
	s.Transact = true
	return s
}

// openTransaction opens a Core Animation transaction on the main thread
// and tells r, as an application laying out native views does, and returns
// what commits it and tells r right after.
func openTransaction(r *Renderer) (commit func()) {
	tx := class("CATransaction")
	pool(func() { sendMain(tx, "begin") })
	r.SetTransaction(true)
	return func() {
		pool(func() { sendMain(tx, "commit") })
		r.SetTransaction(false)
	}
}

// TestAnOpenTransactionHoldsTheNextFrame draws two frames presenting with
// a transaction the application keeps open: of the layer's two drawables,
// the first frame's is bound to the transaction until it commits and the
// other is shown, so the second takes none, which would wait a second for a
// commit that its wait keeps from coming. It waits, built.
func TestAnOpenTransactionHoldsTheNextFrame(t *testing.T) {
	r := layerRenderer(t)
	r.SetPost(func(func()) {})
	taken := takenDrawables(t)
	render(t, r, squareAt(0), nil) // changes the layer
	commit := openTransaction(r)
	defer commit()
	render(t, r, transactAt(1), nil)
	*taken = 0
	var held noticed
	render(t, r, transactAt(2), &held)
	if f := r.FrameFacts(); f.Outcome != gpu.FrameStashed || f.DrawableWait != 0 {
		t.Errorf("the second frame in the transaction: %+v, want it stashed, waiting for no drawable", f)
	}
	if *taken != 0 || r.pending.cb == 0 {
		t.Fatalf("the second frame in the transaction took %d drawables, pending %v", *taken, r.pending.cb != 0)
	}
	held.want(t, "while the transaction is open")
	if r.Ready(func() {}) {
		t.Fatal("ready while a frame waits")
	}
}

// TestACommitDrawsTheHeldFrameOnce checks that the frame waiting for the
// open transaction draws once it commits, once, with the transaction.
func TestACommitDrawsTheHeldFrameOnce(t *testing.T) {
	r := layerRenderer(t)
	posted := make(chan func(), 4)
	r.SetPost(func(fn func()) { posted <- fn })
	taken := takenDrawables(t)
	render(t, r, squareAt(0), nil)
	commit := openTransaction(r)
	render(t, r, transactAt(1), nil)
	var held noticed
	render(t, r, transactAt(2), &held)
	woke := false
	if r.Ready(func() { woke = true }) {
		t.Fatal("ready while a frame waits")
	}
	*taken = 0
	commit()
	if *taken != 1 || r.pending.cb != 0 || !woke || !r.synced || r.bound != 0 {
		t.Fatalf("once committed: took %d drawables, pending %v, woke %v, synced %v, %d bound",
			*taken, r.pending.cb != 0, woke, r.synced, r.bound)
	}
	held.want(t, "once committed", platform.Presented)
	// Nothing posted draws it again.
	for {
		select {
		case fn := <-posted:
			fn()
			continue
		default:
		}
		break
	}
	if *taken != 1 || len(held.got) != 1 || held.agains != 0 {
		t.Fatalf("after: took %d drawables, noticed %v, handed on %d times", *taken, held.got, held.agains)
	}
	if !r.Ready(nil) {
		t.Fatal("not ready once drawn")
	}
}

// TestFramesWithTheTransactionWithoutAnOpenOne checks that without a
// transaction the application keeps open, frames presenting with the
// transaction one after another take their drawables at once, as before.
func TestFramesWithTheTransactionWithoutAnOpenOne(t *testing.T) {
	r := layerRenderer(t)
	r.SetPost(func(func()) {})
	taken := takenDrawables(t)
	render(t, r, squareAt(0), nil)
	*taken = 0
	for i := 1; i <= 3; i++ {
		var n noticed
		render(t, r, transactAt(i), &n)
		if f := r.FrameFacts(); f.Outcome != gpu.FrameTransaction {
			t.Errorf("frame %d: %+v, want it presented with the transaction", i, f)
		}
		n.want(t, "a frame with the transaction", platform.Presented)
	}
	if *taken != 3 || r.pending.cb != 0 || r.bound != 0 {
		t.Fatalf("took %d drawables for 3 frames, pending %v, %d bound", *taken, r.pending.cb != 0, r.bound)
	}
}
