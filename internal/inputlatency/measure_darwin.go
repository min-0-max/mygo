package main

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/ui"
)

type nsPoint struct{ X, Y float64 }

type nsRect struct{ X, Y, W, H float64 }

// The functions that run on the calling thread are bound; those that run
// on the main thread, through mainthread.Forward, are addresses: the
// AppKit messages that take or return a struct, CFRelease of an event and
// MTLCreateSystemDefaultDevice.
var (
	mediaTime    func() float64
	getMethod    func(cls, sel uintptr) uintptr
	setImp       func(m, imp uintptr) uintptr
	scrollEvent  func(source uintptr, units, count uint32, dy, dx, dz int32) uintptr
	setField     func(event uintptr, field uint32, v int64)
	setLocation  func(event uintptr, p nsPoint)
	getClass     func(obj uintptr) uintptr
	msgSend      uintptr
	cfRelease    uintptr
	createDevice uintptr
)

func load() error {
	libs := map[string]uintptr{}
	for _, path := range []string{
		"/usr/lib/libobjc.A.dylib",
		"/System/Library/Frameworks/QuartzCore.framework/QuartzCore",
		"/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics",
		"/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation",
		"/System/Library/Frameworks/Metal.framework/Metal",
	} {
		h, err := purego.Dlopen(path, purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			return err
		}
		libs[path[strings.LastIndex(path, "/")+1:]] = h
	}
	objcLib, cg := libs["libobjc.A.dylib"], libs["CoreGraphics"]
	purego.RegisterLibFunc(&mediaTime, libs["QuartzCore"], "CACurrentMediaTime")
	purego.RegisterLibFunc(&getMethod, objcLib, "class_getInstanceMethod")
	purego.RegisterLibFunc(&setImp, objcLib, "method_setImplementation")
	purego.RegisterLibFunc(&scrollEvent, cg, "CGEventCreateScrollWheelEvent2")
	purego.RegisterLibFunc(&setField, cg, "CGEventSetIntegerValueField")
	purego.RegisterLibFunc(&setLocation, cg, "CGEventSetLocation")
	purego.RegisterLibFunc(&getClass, objcLib, "object_getClass")
	var err error
	if msgSend, err = purego.Dlsym(objcLib, "objc_msgSend"); err != nil {
		return err
	}
	if cfRelease, err = purego.Dlsym(libs["CoreFoundation"], "CFRelease"); err != nil {
		return err
	}
	createDevice, err = purego.Dlsym(libs["Metal"], "MTLCreateSystemDefaultDevice")
	return err
}

func sel(name string) objc.SEL { return objc.RegisterName(name) }

// An event: when it arrived, and when the main thread began and finished
// handling it.
type event struct{ arrived, handling, handled float64 }

// A frame: when it started (the tick or display that drew it), when the
// main thread presented it, and the events it shows.
type frame struct {
	start, presented float64
	events           []event
}

// A measured event, its frame, and when that reached the screen.
type sample struct {
	event
	frame *frame
	shown float64
}

var (
	mu        sync.Mutex
	measuring bool
	pending   []event               // handled since the last frame presented
	frames    = map[uint64]*frame{} // by drawableID, until shown
	samples   []sample
	shown     []float64 // when the frames presented while measuring were shown
	presents  int       // frames presented while measuring
	notShown  int       // of them
	// The main thread's waits for drawables while measuring, in seconds.
	mainWaits []float64
	// On the main thread: when the frame being drawn started.
	frameStart float64
	presented  objc.Block

	origNext, origPresentIn, origPresent, origTick, origUpdate uintptr
)

// swizzle wraps -[CAMetalLayer nextDrawable], the presents of drawables,
// and the surface view's frame methods. The wrappers are methods of the
// worker (mainthread.Method), and the implementations that they wrap run
// on the main thread.
func swizzle() error {
	wrap := func(class uintptr, name string, returnsObject bool, fn any) (uintptr, error) {
		m := getMethod(class, uintptr(sel(name)))
		if m == 0 {
			return 0, fmt.Errorf("no method %s", name)
		}
		return setImp(m, mainthread.Method(purego.NewCallback(fn), returnsObject)), nil
	}
	var err error
	origNext, err = wrap(uintptr(objc.GetClass("CAMetalLayer")), "nextDrawable", true, func(self, cmd uintptr) uintptr {
		asked := mediaTime()
		d := mainthread.Forward[uintptr](origNext, self, cmd)
		if mainthread.IsMainThread() {
			mu.Lock()
			if measuring {
				mainWaits = append(mainWaits, mediaTime()-asked)
			}
			mu.Unlock()
		}
		return d
	})
	if err != nil {
		return err
	}
	// The frame showing the events handled until it presents: frames
	// present on the main thread, as soon as drawn (the command buffer's
	// presentDrawable:) or with the transaction (the drawable's present).
	queue := objc.ID(mainthread.Forward[uintptr](createDevice)).Send(sel("newCommandQueue"))
	buffer := queue.Send(sel("commandBuffer"))
	origPresentIn, err = wrap(uintptr(getClass(uintptr(buffer))), "presentDrawable:", false, func(self, cmd, d uintptr) {
		present(d)
		mainthread.Forward[struct{}](origPresentIn, self, cmd, d)
	})
	if err != nil {
		return err
	}
	origPresent, err = wrap(uintptr(objc.GetClass("CAMetalDrawable")), "present", false, func(self, cmd uintptr) {
		present(self)
		mainthread.Forward[struct{}](origPresent, self, cmd)
	})
	if err != nil {
		return err
	}
	view := uintptr(objc.GetClass("MyGoSurfaceView"))
	origTick, err = wrap(view, "mygoTick:", false, func(self, cmd, link uintptr) {
		frameStart = mediaTime()
		mainthread.Forward[struct{}](origTick, self, cmd, link)
	})
	if err != nil {
		return err
	}
	origUpdate, err = wrap(view, "updateLayer", false, func(self, cmd uintptr) {
		frameStart = mediaTime()
		mainthread.Forward[struct{}](origUpdate, self, cmd)
	})
	return err
}

// present notes the frame presenting drawable d, once: a command
// buffer's presentDrawable: may present it with present.
func present(d uintptr) {
	f := &frame{start: frameStart, presented: mediaTime()}
	id := mainthread.Send[uint64](objc.ID(d), sel("drawableID"))
	mu.Lock()
	if frames[id] != nil {
		mu.Unlock()
		return
	}
	f.events, pending = pending, nil
	frames[id] = f
	if measuring {
		presents++
	}
	mu.Unlock()
	mainthread.Send[struct{}](objc.ID(d), sel("addPresentedHandler:"), presented)
}

func init() {
	presented = mainthread.Block(objc.NewBlock(func(_ objc.Block, d objc.ID) {
		at := mainthread.Send[float64](d, sel("presentedTime"))
		id := mainthread.Send[uint64](d, sel("drawableID"))
		mu.Lock()
		defer mu.Unlock()
		f := frames[id]
		delete(frames, id)
		if f == nil || !measuring {
			return
		}
		if at == 0 {
			// Another frame replaced it before the screen showed it,
			// showing its events too: they are not counted.
			notShown++
			return
		}
		shown = append(shown, at)
		for _, e := range f.events {
			samples = append(samples, sample{e, f, at})
		}
	}))
}

// The window's state.
var (
	text      = strings.Repeat("The quick brown fox jumps over the lazy dog. ", 40)
	list      ui.ListState
	offset    ui.Point
	mode      string
	window    *mygo.Window
	nsWindow  uintptr
	surface   uintptr
	errResult error
)

func view(c *ui.Context) {
	switch mode {
	case "type":
		ui.Column(c).Fill().Padding(16).Children(func() {
			ui.TextArea(c, &text).AutoFocus().Grow(1)
		})
	case "scroll":
		ui.List(c, &list, 1_000_000, func(i int) {
			ui.Row(c).Height(float32(28 + i%3*8)).Gap(8).Padding(4).Children(func() {
				ui.Textf(c, "Row %d", i).Grow(1)
				ui.Text(c, "detail").TextColor(c.Theme().TextMuted)
			})
		}).Grow(1)
	case "anim":
		ui.Box(c).Fill().Draw(func(p *ui.Painter, r ui.Rect) {
			// A box crossing the window every second, a frame each refresh.
			x := float32(p.Now().UnixNano()%1e9) / 1e9 * (r.W - 80)
			p.Fill(ui.Rect{X: r.X + x, Y: r.Y + 100, W: 80, H: 80}, c.Theme().Accent, 8)
			p.AnimationFrame()
		})
	case "heavy":
		ui.Box(c).Fill().Draw(func(p *ui.Painter, r ui.Rect) {
			// The same with large boxes casting wide shadows, which keep
			// the GPU busy several milliseconds a frame.
			t := float32(p.Now().UnixNano()%1e9) / 1e9
			for i := range boxes {
				x := float32(i%20)*36 + t*80
				y := float32(i/20)*50 + 20
				p.Shadow(ui.Rect{X: r.X + x, Y: r.Y + y, W: 160, H: 120}, 16, 0, 8, 48, 0, ui.RGBA(0, 0, 0, 0.3))
				p.Fill(ui.Rect{X: r.X + x, Y: r.Y + y, W: 160, H: 120}, c.Theme().Accent, 16)
			}
			p.AnimationFrame()
		})
	case "drag":
		ui.Box(c).Fill().Children(func() {
			ui.Column(c).Fill().Children(func() {
				for i := range 30 {
					ui.Textf(c, "Row %d of the content under the box", i)
				}
			})
			ui.Box(c).Absolute().Width(80).Height(80).Background(c.Theme().Accent).Draggable(&offset, ui.DraggableOptions{})
		})
	}
}

// boxes is how many boxes heavy draws.
var boxes int

func measure(m string, events, warmup, x, y, n int) error {
	boxes = n
	switch m {
	case "type", "scroll", "drag", "anim", "heavy":
	default:
		return fmt.Errorf("unknown mode %q", m)
	}
	mode = m
	if err := load(); err != nil {
		return err
	}
	// Out of the user's way: the app is not activated, and the window
	// neither takes the keyboard nor comes to the front of the others.
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	mygo.App.WhenReady(func() {
		window = mygo.NewWindow(mygo.WindowOptions{Title: "inputlatency", Width: 800, Height: 600, X: x, Y: y, Hidden: true, Content: ui.View(view)})
		nsWindow = window.NativeHandle()
		// As the app the user types into, which is active: App Nap would
		// delay the inactive app's wakeups by milliseconds.
		const userInitiated, latencyCritical = 0x00EFFFFF, 0xFF00000000
		reason := objc.ID(objc.GetClass("NSString")).Send(sel("stringWithUTF8String:"), "measuring input latency\x00")
		info := mainthread.Send[objc.ID](objc.ID(objc.GetClass("NSProcessInfo")), sel("processInfo"))
		// The activity is held, retained, while the process lives.
		mainthread.SendRetained(info, sel("beginActivityWithOptions:reason:"), uint64(userInitiated|latencyCritical), reason)
		mainthread.Send[struct{}](objc.ID(nsWindow), sel("orderFrontRegardless"))
		go func() {
			errResult = run(events, warmup)
			mygo.App.Quit()
		}()
	})
	if err := mygo.App.Run(); err != nil {
		return err
	}
	return errResult
}

func findSurface(v objc.ID) objc.ID {
	if objc.Send[bool](v, sel("isKindOfClass:"), objc.GetClass("MyGoSurfaceView")) {
		return v
	}
	subviews := mainthread.Send[objc.ID](v, sel("subviews"))
	for i := range mainthread.Send[int](subviews, sel("count")) {
		if s := findSurface(mainthread.Send[objc.ID](subviews, sel("objectAtIndex:"), i)); s != 0 {
			return s
		}
	}
	return 0
}

// inject has the main thread handle an event arriving now.
func inject(handle func()) {
	e := event{arrived: mediaTime()}
	mygo.RunOnMain(func() {
		e.handling = mediaTime()
		handle()
		e.handled = mediaTime()
		mu.Lock()
		pending = append(pending, e)
		mu.Unlock()
	})
}

func windowPoint(x, y float64) nsPoint {
	return mainthread.Forward[nsPoint](msgSend, surface, sel("convertPoint:toView:"), nsPoint{x, y}, uintptr(0))
}

func sendKey() {
	number := mainthread.Send[int](objc.ID(nsWindow), sel("windowNumber"))
	s := objc.ID(objc.GetClass("NSString")).Send(sel("stringWithUTF8String:"), "a\x00")
	for _, typ := range []uint{10, 11} { // NSEventTypeKeyDown, KeyUp
		ev := mainthread.Forward[objc.ID](msgSend, objc.GetClass("NSEvent"), sel("keyEventWithType:location:modifierFlags:timestamp:windowNumber:context:characters:charactersIgnoringModifiers:isARepeat:keyCode:"),
			typ, nsPoint{}, uint(0), mediaTime(), number, uintptr(0), s, s, false, uint16(0))
		mainthread.Send[struct{}](objc.ID(nsWindow), sel("sendEvent:"), ev)
	}
}

func sendMouse(typ uint, x, y float64) {
	number := mainthread.Send[int](objc.ID(nsWindow), sel("windowNumber"))
	ev := mainthread.Forward[objc.ID](msgSend, objc.GetClass("NSEvent"), sel("mouseEventWithType:location:modifierFlags:timestamp:windowNumber:context:eventNumber:clickCount:pressure:"),
		typ, windowPoint(x, y), uint(0), mediaTime(), number, uintptr(0), 0, 1, float32(1))
	mainthread.Send[struct{}](objc.ID(nsWindow), sel("sendEvent:"), ev)
}

// sendScroll scrolls as a trackpad does, by dy points, in a phase of the
// gesture (kCGScrollPhase: 1 began, 2 changed, 4 ended).
func sendScroll(phase int64, dy int32) {
	ev := scrollEvent(0, 0, 1, dy, 0, 0) // in pixels
	setField(ev, 88, 1)                  // kCGScrollWheelEventIsContinuous
	setField(ev, 99, phase)              // kCGScrollWheelEventScrollPhase
	setField(ev, 96, int64(dy))          // kCGScrollWheelEventPointDeltaAxis1
	// A window-less event's location is on the screen, flipped by Core
	// Graphics: its locationInWindow is then this point.
	p := windowPoint(300, 300)
	screens := mainthread.Send[objc.ID](objc.ID(objc.GetClass("NSScreen")), sel("screens"))
	screen := mainthread.Send[objc.ID](screens, sel("objectAtIndex:"), 0)
	setLocation(ev, nsPoint{p.X, mainthread.Send[nsRect](screen, sel("frame")).H - p.Y})
	nsEvent := mainthread.Send[objc.ID](objc.ID(objc.GetClass("NSEvent")), sel("eventWithCGEvent:"), ev)
	mainthread.Send[struct{}](objc.ID(surface), sel("scrollWheel:"), nsEvent)
	mainthread.ByReceiver(cfRelease, ev)
}

func run(events, warmup int) error {
	// The first frames, at full speed.
	time.Sleep(1500 * time.Millisecond)
	var err error
	var refresh float64
	mygo.RunOnMain(func() {
		surface = uintptr(findSurface(mainthread.Send[objc.ID](objc.ID(nsWindow), sel("contentView"))))
		if surface == 0 {
			err = errors.New("no MyGoSurfaceView in the window")
			return
		}
		screen := mainthread.Send[objc.ID](objc.ID(nsWindow), sel("screen"))
		refresh = float64(mainthread.Send[int](screen, sel("maximumFramesPerSecond")))
		err = swizzle()
	})
	if err != nil {
		return err
	}
	between := func(lo, hi float64) time.Duration {
		return time.Duration((lo + rand.Float64()*(hi-lo)) * float64(time.Millisecond))
	}
	n := 0
	next := func() bool {
		if n == warmup {
			mu.Lock()
			measuring = true
			mu.Unlock()
		}
		n++
		return n <= warmup+events
	}
	switch mode {
	case "type":
		for next() {
			inject(sendKey)
			time.Sleep(between(60, 140))
		}
	case "scroll":
		for n < warmup+events {
			mygo.RunOnMain(func() { sendScroll(1, -2) })
			for range 60 {
				if !next() {
					break
				}
				inject(func() { sendScroll(2, -3) })
				time.Sleep(between(4, 12))
			}
			mygo.RunOnMain(func() { sendScroll(4, 0) })
			time.Sleep(300 * time.Millisecond)
		}
	case "anim", "heavy":
		// warmup and events count refreshes.
		time.Sleep(time.Duration(float64(warmup) / refresh * float64(time.Second)))
		mu.Lock()
		measuring = true
		mu.Unlock()
		time.Sleep(time.Duration(float64(events) / refresh * float64(time.Second)))
	case "drag":
		for n < warmup+events {
			var x, y float64
			mygo.RunOnMain(func() {
				x, y = 40+float64(offset.X), 40+float64(offset.Y)
				sendMouse(1, x, y) // NSEventTypeLeftMouseDown
			})
			time.Sleep(50 * time.Millisecond)
			for i := range 60 {
				if !next() {
					break
				}
				if i < 30 {
					x += 3
				} else {
					x -= 3
				}
				px := x
				inject(func() { sendMouse(6, px, y) }) // NSEventTypeLeftMouseDragged
				time.Sleep(between(4, 12))
			}
			mygo.RunOnMain(func() { sendMouse(2, x, y) }) // NSEventTypeLeftMouseUp
			time.Sleep(300 * time.Millisecond)
		}
	}
	// The last frames reach the screen.
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	measuring = false
	ss, at, waits, np, ns := samples, shown, mainWaits, presents, notShown
	mu.Unlock()
	if np == 0 {
		return errors.New("no frame presented")
	}
	var f figures
	f.refresh, f.events = refresh, float64(len(ss))
	// The frames: how long the main thread waited for drawables, and how
	// many refreshes, from the first frame shown to the last, showed a new
	// one (all, for an animation, but for frames not drawn in time).
	for _, w := range waits {
		f.mainWait += w * 1000 / float64(np)
		if w > 0.004 {
			f.blocked += 100 / float64(np)
		}
	}
	f.notShown = 100 * float64(ns) / float64(np)
	if len(at) > 1 && refresh > 0 {
		slices.Sort(at)
		refreshes := map[int64]bool{}
		for _, t := range at {
			refreshes[int64(math.Round(t*refresh))] = true
		}
		span := math.Round((at[len(at)-1]-at[0])*refresh) + 1
		f.newFrames = 100 * float64(len(refreshes)) / span
	}
	if len(ss) == 0 {
		if mode != "anim" && mode != "heavy" {
			return errors.New("no event reached the screen")
		}
		fmt.Println(f.line())
		return nil
	}
	var total []float64
	k := float64(len(ss))
	for _, s := range ss {
		total = append(total, (s.shown-s.arrived)*1000)
		f.queue += (s.handling - s.arrived) * 1000 / k
		f.handle += (s.handled - s.handling) * 1000 / k
		f.toPresent += (s.frame.presented - s.handled) * 1000 / k
		f.toScreen += (s.shown - s.frame.presented) * 1000 / k
	}
	slices.Sort(total)
	for _, t := range total {
		f.mean += t / k
	}
	f.p50, f.p90, f.p99 = percentile(total, .5), percentile(total, .9), percentile(total, .99)
	fmt.Println(f.line())
	return nil
}
