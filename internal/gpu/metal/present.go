//go:build darwin

package metal

import (
	"image"
	"runtime"
	"sync"
	"time"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/internal/gpu"
	"github.com/egoist/mygo/internal/platform"
	"github.com/egoist/mygo/internal/scene"
)

// A presenter fetches the layer's next drawable on a thread of its own,
// after a frame presented as soon as drawn that followed another
// (follows), so that the main thread never waits for the display.
// nextDrawable blocks while the layer's
// drawables are all in use, as the display holds them until its next
// refreshes: a frame following another would wait most of a refresh,
// with the main thread able to do nothing else. Instead, a frame that
// comes while the presenter fetches is built and waits (pendingFrame),
// and the main thread draws it into the drawable once the presenter has
// it; frames paced by the display wait for that (Ready).
//
// The main thread waits for a fetch in flight before changing the layer
// (its drawables' size or format, presenting with the transaction,
// trimming), so a fetch never races those, and frames presented with the
// transaction take their drawable themselves, as a live resize's must.
// Waiting on a channel, the thread costs nothing while frames stop.
type presenter struct {
	layer id
	fetch chan struct{}

	mu   sync.Mutex
	done sync.Cond
	// drawable is the one fetched, retained, until a frame takes it;
	// fetching tells that the thread is fetching one, and wake what it
	// calls once it has (Ready).
	drawable id
	fetching bool
	wake     func()
	started  bool
}

// followRefreshes is how many of the display's refreshes after the last
// frame a frame counts as following it. Only tests change it.
var followRefreshes = 2.0

// framesForThird is how many frames in a row must follow the last before
// the layer may have a third drawable.
const framesForThird = 6

// qosUserInteractive is QOS_CLASS_USER_INTERACTIVE, the main thread's.
const qosUserInteractive = 0x21

var (
	qosOnce sync.Once
	setQoS  func(class uint32, priority int32) int32
	// nextDrawable returns the layer's next drawable, autoreleased, for
	// the presenter, and frameDrawable for a frame that takes its own, on
	// the main thread. renderEncoder returns a command buffer's encoder
	// for a render pass, autoreleased. The renderer never changes them;
	// only tests replace them, to stand for the system giving none.
	nextDrawable  = func(layer id) id { return send(layer, "nextDrawable") }
	frameDrawable = func(layer id) id { return send(layer, "nextDrawable") }
	renderEncoder = func(cb, pass id) id { return send(cb, "renderCommandEncoderWithDescriptor:", pass) }
)

// start has the thread fetch the next drawable, unless it is or has one.
func (p *presenter) start(layer id) {
	p.mu.Lock()
	if p.fetching || p.drawable != 0 {
		p.mu.Unlock()
		return
	}
	p.fetching = true
	if !p.started {
		p.started = true
		p.layer = layer
		p.done.L = &p.mu
		p.fetch = make(chan struct{}, 1)
		go p.run()
	}
	p.mu.Unlock()
	p.fetch <- struct{}{}
}

func (p *presenter) run() {
	// The autorelease pools belong to the thread.
	runtime.LockOSThread()
	qosOnce.Do(func() {
		if h, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW); err == nil {
			mainthread.Bind(&setQoS, h, "pthread_set_qos_class_self_np")
		}
	})
	if setQoS != nil {
		setQoS(qosUserInteractive, 0)
	}
	for range p.fetch {
		pool, _, _ := purego.SyscallN(poolPush)
		d := nextDrawable(p.layer)
		if d != 0 {
			send(d, "retain")
		}
		purego.SyscallN(poolPop, pool)
		p.mu.Lock()
		p.drawable, p.fetching = d, false
		wake := p.wake
		p.wake = nil
		p.done.Broadcast()
		p.mu.Unlock()
		if wake != nil {
			wake()
		}
	}
}

// ready reports whether a frame can take a drawable at once, as none is
// being fetched; otherwise wake runs once one is, on the presenter's
// thread.
func (p *presenter) ready(wake func()) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.fetching {
		return true
	}
	p.wake = wake
	return false
}

// wait waits for a fetch in flight.
func (p *presenter) wait() {
	p.mu.Lock()
	for p.fetching {
		p.done.Wait()
	}
	p.wake = nil
	p.mu.Unlock()
}

// take returns the drawable fetched, retained, or 0, once no fetch is in
// flight.
func (p *presenter) take() id {
	p.wait()
	p.mu.Lock()
	d := p.drawable
	p.drawable = 0
	p.mu.Unlock()
	return d
}

// drain lets go of the drawable fetched, once no fetch is in flight.
func (p *presenter) drain() {
	if d := p.take(); d != 0 {
		byReceiver(d, "release")
	}
}

// stop drains the presenter and ends its thread.
func (p *presenter) stop() {
	p.drain()
	p.mu.Lock()
	if p.started {
		close(p.fetch)
		p.started = false
	}
	p.mu.Unlock()
}

// A pendingFrame is a frame built while its drawable was being fetched,
// to draw into it once the presenter has it: its command buffer, retained,
// holding what the frame drew before the drawable (canvases, blurred
// images), and what drawing the rest takes of its scene, which the
// content may build anew meanwhile (a capture).
type pendingFrame struct {
	cb id
	// size and clear are the scene's, and whole tells that its effects
	// read their backdrops, which drawing in part would add to the area
	// drawn: it is drawn whole.
	size  scene.Scene
	whole bool
	// done and again are the frame's notice (RenderNotice).
	done  func(platform.Presentation, error)
	again func()
}

// SetPost gives the renderer post, which runs a function on the main
// thread from any goroutine: a frame following another builds while its
// drawable is being fetched, and draws once the presenter has it, through
// post. Without it, frames wait for their drawable.
func (r *Renderer) SetPost(post func(func())) {
	r.post = post
	finish := func() {
		pool(func() {
			// A frame not paced may have drawn the frame pending this was
			// posted for, and another may be pending, whose drawable is
			// being fetched: its fetch posts again once done, rather than
			// the main thread waiting for it.
			if r.pending.cb == 0 || !r.pre.ready(r.fetched) {
				return
			}
			if err := r.finishPending(true); err != nil {
				r.err = err
				r.wakeContent() // the next frame finds the error
			}
		})
	}
	r.fetched = func() { post(finish) }
}

// SetRefreshRate gives the renderer rate, which returns the display's
// refresh rate, 0 when it is not known. Without it, the display counts as a
// 60 Hz one.
func (r *Renderer) SetRefreshRate(rate func() float64) { r.refreshRate = rate }

// follows reports whether a frame since after the last one follows it, so
// that the next may find no drawable free and the presenter fetches one
// ahead. A drawable the display holds goes free once it shows the next
// frame, which a frame presented reaches within a refresh. So two refreshes
// after a frame, the frame before has let go of its drawable, and a frame
// coming at that pace finds it free at once: a fetch would only have the
// system make a third. Frames coming sooner can find both in use, and would
// wait most of a refresh.
func (r *Renderer) follows(since time.Duration) bool {
	hz := 60.0
	if r.refreshRate != nil {
		if rate := r.refreshRate(); rate > 0 {
			hz = rate
		}
	}
	return since.Seconds() < followRefreshes/hz
}

// countFollowing counts a frame about to draw. Two drawables hold a few frames
// in a row, as opening a view draws: the system makes a drawable when one is
// asked for with all in use, so with three allowed those frames would cost a
// third until the window idles, and with two the last of them waits at most
// for the display to let go of one. They are too few for frames that keep
// coming on a ProMotion display, which would wait for one most of each
// refresh, so frames that keep following get a third, also as Core Animation
// makes while a window animates, until the idle trim.
func (r *Renderer) countFollowing() {
	if !r.follows(time.Since(r.lastRender)) {
		r.following = 0
		return
	}
	r.following++
	if r.following >= framesForThird && !r.third {
		// No drawable is being fetched while the layer changes.
		r.pre.wait()
		r.setDrawables(3)
	}
}

// setDrawables sets how many drawables the layer may have.
func (r *Renderer) setDrawables(n uintptr) {
	if respondsTo(r.layer, "setMaximumDrawableCount:") {
		sendMain(r.layer, "setMaximumDrawableCount:", n)
	}
	r.third = n == 3
}

// layerChanges reports whether drawing s at scale changes the layer, its
// drawables' size or format.
func (r *Renderer) layerChanges(s *scene.Scene, scale float64) bool {
	return r.layerWide != r.wide || s.Width != r.w || s.Height != r.h || scale != r.scale || r.trimmed ||
		layerBounds(r.superlayer) != r.bounds
}

// stash builds the frame s, which changed damage since the last one, to
// draw once its drawable comes (finishPending), then tell done, or call
// again once dropped (dropPending).
func (r *Renderer) stash(s *scene.Scene, damage []image.Rectangle, known bool, done func(platform.Presentation, error), again func()) error {
	cb, _, err := r.prepare(s)
	if err != nil {
		r.forgetFrames()
		return err
	}
	damage = r.pipelineDamage(damage, known)
	r.ages.Add(damage, !known)
	r.pending = pendingFrame{cb: send(cb, "retain"), whole: len(s.Effects) > 0, done: done, again: again}
	r.pending.size.Width, r.pending.size.Height, r.pending.size.Clear = s.Width, s.Height, s.Clear
	r.diff.Remember(s)
	r.pipelines.Remember()
	return nil
}

// finishPending draws the frame pending into its drawable, waiting for it
// when called before the presenter has it, and presents it, then has the
// presenter fetch the next frame's drawable if prefetch.
func (r *Renderer) finishPending(prefetch bool) error {
	p := r.pending
	r.pending = pendingFrame{}
	defer r.wakeContent()
	drawable := r.prefetched()
	if drawable == 0 {
		drawable = frameDrawable(r.layer)
	}
	if drawable == 0 {
		// None came within a second: the frame is skipped, what it drew
		// before its drawable committed.
		r.forgetFrames()
		byReceiver(p.cb, "commit")
		r.committedOutside(gpu.CommitStashed)
		r.track(p.cb)
		release(&p.cb)
		p.done(platform.PresentNoDrawable, nil)
		return nil
	}
	tex := send(drawable, "texture")
	backdrops := func(d []image.Rectangle) []image.Rectangle { return d }
	if p.whole {
		backdrops = nil
	}
	if err := r.drawBuilt(p.cb, &p.size, tex, &r.drawState, r.partIn(p.size.Width, p.size.Height, tex, backdrops)); err != nil {
		r.forgetFrames()
		release(&p.cb)
		p.done(platform.PresentFailed, err)
		return err
	}
	r.presenting(drawable)
	r.observeRootLayer(gpu.RootLayerPresentRequested)
	send(p.cb, "presentDrawable:", drawable)
	byReceiver(p.cb, "commit")
	r.committedOutside(gpu.CommitStashed)
	if prefetch {
		r.pre.start(r.layer)
	}
	r.track(p.cb)
	release(&p.cb)
	r.lastRender = time.Now()
	r.armTrim()
	p.done(platform.Presented, nil)
	return nil
}

// dropPending drops the frame pending, committing what it drew before its
// drawable: the next draws whole, and answers its notice (again). What
// Ready was given meanwhile runs, as the frame waiting for this one, which
// never comes, can draw now.
func (r *Renderer) dropPending() {
	if r.pending.cb == 0 {
		return
	}
	r.forgetFrames()
	byReceiver(r.pending.cb, "commit")
	r.committedOutside(gpu.CommitStashed)
	r.track(r.pending.cb)
	release(&r.pending.cb)
	again := r.pending.again
	r.pending = pendingFrame{}
	again()
	r.wakeContent()
}

// wakeContent runs what Ready was given while a frame was pending.
func (r *Renderer) wakeContent() {
	if w := r.wake; w != nil {
		r.wake = nil
		w()
	}
}
