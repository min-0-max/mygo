//go:build darwin && arm64

package mainstack

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/internal/darwin"
	"github.com/egoist/mygo/internal/surface"
	"github.com/egoist/mygo/internal/wait"
	"github.com/egoist/mygo/ui"
)

// The phases of the stack child, in the order it runs them.
const (
	phaseShown = iota
	phaseFrames
	phaseInput
	phaseRunOnMain
	phaseModal
	phaseMenu
	phaseTrim
	phaseSignals
	phaseFrames2
	phaseInput2
	phaseResize
	phaseClose
	phaseQuit
	phaseCount
)

var phaseNames = [phaseCount]string{
	phaseShown:     "window 1 (native UI with a web view): created and shown",
	phaseFrames:    "window 1: frames",
	phaseInput:     "window 1: injected pointer moves and keys",
	phaseRunOnMain: "window 1: RunOnMain from another goroutine",
	phaseModal:     "window 1: modal panel",
	phaseMenu:      "window 1: menu",
	phaseTrim:      "window 1: the drawable trimmed to 1x1",
	phaseSignals:   "window 1: SIGCHLD and SIGURG",
	phaseFrames2:   "window 2 (native UI without a web view): frames",
	phaseInput2:    "window 2: injected pointer moves and keys",
	phaseResize:    "window 2: live resize",
	phaseClose:     "window 2: close",
	phaseQuit:      "quit",
}

// The main thread runs no code on a stack other than its own while a mygo
// app runs (docs: the main-thread worker): the stack child runs an app
// whose main thread blocks the asynchronous signals as a host blocks them,
// with a window of native UI with a web view and one without, through the
// phases above, while a thread samples the stack pointer of the main
// thread as the collector of a JavaScriptCore VM reads it. Each phase must
// take samples, show that it did what it names, and count no sample
// outside the stack of the main thread.
func TestTheMainThreadRunsNoCodeOutsideItsOwnStack(t *testing.T) {
	out, err := runChild(t, "stack")
	report := parseStackReport(out)
	if err != nil || !report.ended {
		t.Fatalf("the stack child did not run to its end: %v\n%s", err, out)
	}
	if report.failures > 0 {
		t.Errorf("the sampler failed %d times, last with kern_return_t %d", report.failures, report.lastError)
	}
	var lines []string
	outside := false
	for i, name := range phaseNames {
		c, ok := report.counts[i]
		switch {
		case !ok || c.samples == 0:
			t.Errorf("%s: the sampler took no sample", name)
		case c.outside > 0:
			outside = true
		}
		if e, ok := report.evidence[i]; !ok {
			t.Errorf("%s: the child reported nothing of what the phase did", name)
		} else if e != "" {
			t.Errorf("%s: %s", name, e)
		}
		line := fmt.Sprintf("%s: %d of %d samples outside", name, c.outside, c.samples)
		if c.outside > 0 {
			line += fmt.Sprintf(" (first stack pointer %#x, in %s)", c.firstOutside, report.firstFuncs[i])
		}
		lines = append(lines, line)
	}
	if outside {
		t.Errorf("the main thread ran outside its stack [%#x, %#x]:\n%s", report.low, report.high, strings.Join(lines, "\n"))
	} else {
		t.Logf("stack [%#x, %#x]:\n%s", report.low, report.high, strings.Join(lines, "\n"))
	}
	for k, name := range stepNames {
		s := report.steps[k]
		switch {
		case s.count == 0:
			t.Errorf("window 2: no %s was measured", name)
		case s.max > stepLimits[k]:
			t.Errorf("window 2: a %s forwarded %d calls to the main thread, more than %d (calls:steps %s)", name, s.max, stepLimits[k], s)
		default:
			t.Logf("window 2: a %s forwarded %d calls at most, of %d (calls:steps %s)", name, s.max, stepLimits[k], s)
		}
	}
	switch p := report.presents; {
	case p.count == 0:
		t.Error("window 2: no frame presented during the live resize")
	case p.mismatched > 0:
		t.Errorf("window 2: %d of %d frames presented during the live resize had a drawable of another size than the layer's, first %s", p.mismatched, p.count, p.first)
	}
}

// The kinds of step of window 2 whose forwarded calls the stack child
// counts, and how many each may forward at most (docs: the main-thread
// worker).
const (
	stepFrame = iota
	stepResize
	stepMove
	stepKey
	stepKinds
)

var (
	stepNames  = [stepKinds]string{"frame that changes no window size, layer or placement", "change of the window size in a live resize", "pointer move", "key"}
	stepLimits = [stepKinds]uint64{15, 20, 6, 7}
)

// stepCount is how many steps of a kind the child measured, the most
// calls that one forwarded, and how many forwarded each count.
type stepCount struct {
	count, max uint64
	counts     map[uint64]uint64
}

// String lists the counts of forwarded calls with how many steps
// forwarded each, as "6:200 7:20".
func (s stepCount) String() string {
	var b strings.Builder
	for n := range s.max + 1 {
		if c := s.counts[n]; c > 0 {
			fmt.Fprintf(&b, " %d:%d", n, c)
		}
	}
	return strings.TrimSpace(b.String())
}

type presentCount struct {
	count, mismatched uint64
	first             string
}

type stackReport struct {
	ended               bool
	low, high           uint64
	failures, lastError int64
	counts              map[int]sampleCount
	firstFuncs          map[int]string
	evidence            map[int]string
	steps               [stepKinds]stepCount
	presents            presentCount
}

// parseStackReport reads the lines of the stack child:
//
//	stack <low> <high>
//	phase <index> <samples> <outside> <first outside> <its function>
//	evidence <index> <what failed, empty when nothing did>
//	sampler <failures> <last error>
//	steps <kind> <count> <most forwarded calls> <calls:steps ...>
//	presents <count> <of another size> <the first of another size>
//	end
func parseStackReport(out string) stackReport {
	r := stackReport{counts: map[int]sampleCount{}, firstFuncs: map[int]string{}, evidence: map[int]string{}}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.SplitN(sc.Text(), "\t", 6)
		num := func(i int) uint64 {
			if i >= len(f) {
				return 0
			}
			n, _ := strconv.ParseUint(f[i], 0, 64)
			return n
		}
		switch f[0] {
		case "stack":
			r.low, r.high = num(1), num(2)
		case "phase":
			r.counts[int(num(1))] = sampleCount{samples: num(2), outside: num(3), firstOutside: uintptr(num(4))}
			if len(f) > 5 {
				r.firstFuncs[int(num(1))] = f[5]
			}
		case "evidence":
			if len(f) == 3 {
				r.evidence[int(num(1))] = f[2]
			}
		case "sampler":
			r.failures, r.lastError = int64(num(1)), int64(num(2))
		case "steps":
			if k := num(1); k < stepKinds {
				r.steps[k] = stepCount{count: num(2), max: num(3), counts: map[uint64]uint64{}}
				if len(f) > 4 {
					for _, pair := range strings.Fields(f[4]) {
						var n, c uint64
						fmt.Sscanf(pair, "%d:%d", &n, &c)
						r.steps[k].counts[n] = c
					}
				}
			}
		case "presents":
			r.presents.count, r.presents.mismatched = num(1), num(2)
			if len(f) > 3 {
				r.presents.first = f[3]
			}
		case "end":
			r.ended = true
		}
	}
	return r
}

// stackRun is the state of the stack child.
type stackRun struct {
	mu       sync.Mutex
	contents []surface.Automated
	evidence [phaseCount]string
	reported [phaseCount]bool

	content      [2]atomic.Value // surface.Automated
	animate      [2]atomic.Bool
	lastFrame    [2]uint64       // in a step
	animated     [2]atomic.Int32 // the frames built while animate is set
	framed       [2]chan struct{}
	web          *mygo.WebView // main thread only
	moves, keys  [2]atomic.Int32
	ran          atomic.Int32
	drawn        map[[2]int]bool // window 2's sizes that frames built
	live         [][2]int        // window 2's sizes during a live resize
	quitSecure   atomic.Bool
	quitListened atomic.Bool

	// What the steps of window 2 forward to the main thread: spent counts
	// the calls that the child forwards itself in those steps, which a
	// mark leaves out of the count of forwarded calls, and own how many
	// one mainSend forwards.
	measuring  atomic.Bool
	frameMarks []uint64
	drag       func(i int) // posts the drag of the live resize after i
	dragged    int         // the sizes of the live resize whose frame posted the next drag
	sizeMarks  []uint64
	spent      atomic.Uint64
	own        uint64
	steps      [stepKinds]stepCount
	presents   presentCount
}

// mark returns how many calls the steps forwarded to the main thread so
// far, leaving out those that the child forwards itself.
func (r *stackRun) mark() uint64 { return forwarded() - r.spent.Load() }

// spend runs fn, which forwards calls itself, in a step, and leaves its
// calls out of the marks.
func (r *stackRun) spend(fn func()) {
	before := forwarded()
	fn()
	r.spent.Add(forwarded() - before)
}

// count adds the forwarded calls of one step of kind k.
func (r *stackRun) count(k int, n uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.steps[k].counts == nil {
		r.steps[k].counts = map[uint64]uint64{}
	}
	r.steps[k].count++
	r.steps[k].max = max(r.steps[k].max, n)
	r.steps[k].counts[n]++
}

// countMarks adds the forwarded calls between each mark and the next as
// the calls of a step of kind k.
func (r *stackRun) countMarks(k int, marks []uint64) {
	for i := 1; i < len(marks); i++ {
		r.count(k, marks[i]-marks[i-1])
	}
}

func (r *stackRun) note(phase int, failure string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reported[phase] = true
	if failure != "" && r.evidence[phase] == "" {
		r.evidence[phase] = failure
	}
}

// stackChild runs the app of the stack test on the main thread and prints
// its report.
func stackChild() int {
	fail := func(err error) int {
		fmt.Println("error:", err)
		return 1
	}
	if err := loadNative(); err != nil {
		return fail(err)
	}
	if err := captureMainThread(); err != nil {
		return fail(err)
	}
	if err := blockAsyncSignals(); err != nil {
		return fail(err)
	}
	r := &stackRun{drawn: map[[2]int]bool{}, framed: [2]chan struct{}{make(chan struct{}, 1), make(chan struct{}, 1)}}
	surface.Automate = func(_ *surface.Conn, c surface.Automated) {
		r.mu.Lock()
		r.contents = append(r.contents, c)
		r.mu.Unlock()
	}
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnQuit(r.quitListener)
	mygo.App.WhenReady(func() { go r.drive() })
	if err := startSampler(); err != nil {
		return fail(err)
	}
	runErr := mygo.App.Run()
	counts, failures, lastError, err := stopSampler()
	if err != nil {
		return fail(err)
	}
	if runErr != nil {
		return fail(runErr)
	}
	r.note(phaseQuit, "")
	if !r.quitListened.Load() {
		r.note(phaseQuit, "the OnQuit listener did not run")
	} else if !r.quitSecure.Load() {
		r.note(phaseQuit, "the app delegate did not answer applicationSupportsSecureRestorableState:")
	}
	low, high := stackBounds()
	fmt.Printf("stack\t%#x\t%#x\n", low, high)
	for i := range phaseCount {
		c := counts[i]
		fmt.Printf("phase\t%d\t%d\t%d\t%#x\t%s\n", i, c.samples, c.outside, c.firstOutside, funcAt(c.firstPC))
		if r.reported[i] {
			fmt.Printf("evidence\t%d\t%s\n", i, r.evidence[i])
		}
	}
	fmt.Printf("sampler\t%d\t%d\n", failures, lastError)
	for k, s := range r.steps {
		fmt.Printf("steps\t%d\t%d\t%d\t%s\n", k, s.count, s.max, s)
	}
	fmt.Printf("presents\t%d\t%d\t%s\n", r.presents.count, r.presents.mismatched, r.presents.first)
	fmt.Println("end")
	return 0
}

// funcAt names the Go function at pc, or the image and symbol of the
// native code there.
func funcAt(pc uintptr) string {
	if pc == 0 {
		return ""
	}
	if f := runtime.FuncForPC(pc); f != nil {
		return f.Name()
	}
	var info dlInfo
	if dladdr(pc, &info) != 0 && info.symbol != nil {
		return fmt.Sprintf("%s (%s)", cString(info.symbol), cString(info.image))
	}
	return fmt.Sprintf("%#x", pc)
}

// quitListener messages a method of a class that mygo registers, its app
// delegate's applicationSupportsSecureRestorableState:, while App.Quit
// runs, and ends the last phase.
func (r *stackRun) quitListener() {
	app := mainSend[objc.ID](class("NSApplication"), sel("sharedApplication"))
	delegate := mainSend[objc.ID](app, sel("delegate"))
	r.quitSecure.Store(mainSend[bool](delegate, sel("applicationSupportsSecureRestorableState:"), app))
	r.quitListened.Store(true)
	setPhase(-1)
}

// frameOf returns the count of frames that a window's content built.
func frameOf(c surface.Automated) uint64 {
	var n uint64
	mygo.RunOnMain(func() { n = frameNumber(c) })
	return n
}

// frameNumber returns the frame number of the engine of a window's
// content (rt.frame), in a step.
func frameNumber(c surface.Automated) uint64 { return c.(interface{ Frame() uint64 }).Frame() }

func (r *stackRun) latestContent() surface.Automated {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.contents) == 0 {
		return nil
	}
	return r.contents[len(r.contents)-1]
}

// view builds the content of window i: a box that takes the keyboard and
// the pointer, and in window 1 the web view under it.
func (r *stackRun) view(i int) func(c *ui.Context) {
	return func(c *ui.Context) {
		if r.animate[i].Load() {
			c.AnimationFrame()
		}
		// A frame may build the content more than once.
		if content, _ := r.content[i].Load().(surface.Automated); content != nil {
			if f := frameNumber(content); f != r.lastFrame[i] {
				r.lastFrame[i] = f
				if r.animate[i].Load() && r.animated[i].Add(1) == framesAnimated {
					select {
					case r.framed[i] <- struct{}{}:
					default:
					}
				}
				if i == 1 && r.measuring.Load() {
					r.mu.Lock()
					r.frameMarks = append(r.frameMarks, r.mark())
					r.mu.Unlock()
				}
			}
		}
		if i == 1 {
			w, h := c.Size()
			s := [2]int{int(math.Round(float64(w))), int(math.Round(float64(h)))}
			r.mu.Lock()
			r.drawn[s] = true
			// The frame of the latest size of a live resize posts the next
			// drag.
			n, drag := len(r.live), r.drag
			next := drag != nil && n > r.dragged && r.live[n-1] == s
			if next {
				r.dragged = n
			}
			r.mu.Unlock()
			if next {
				r.spend(func() { drag(n) })
			}
		}
		ui.Column(c).Fill().AlignItems(ui.Stretch).Children(func() {
			ui.Box(c).Height(120).FillWidth().Focusable().AutoFocus().HandleInput(func(ev ui.InputEvent) bool {
				switch ev.Kind {
				case ui.InputPointerMove:
					r.moves[i].Add(1)
				case ui.InputKeyDown:
					r.keys[i].Add(1)
				}
				return true
			})
			if i == 0 && r.web != nil {
				ui.WebView(c, r.web).Grow(1)
			}
		})
	}
}

// drive runs the phases on a goroutine of its own, and quits.
func (r *stackRun) drive() {
	defer mygo.App.Quit()

	// Window 1: native UI with a web view.
	setPhase(phaseShown)
	w1 := mygo.NewWindow(mygo.WindowOptions{Title: "Main stack 1", X: 40, Y: 80, Width: 480, Height: 360, AlwaysOnTop: true, Content: ui.View(r.view(0))})
	handle1 := w1.NativeHandle()
	content1 := r.latestContent()
	r.note(phaseShown, "")
	if content1 == nil {
		r.note(phaseShown, "the window's content did not attach")
		return
	}
	r.content[0].Store(content1)
	// A window that the window of another app covers builds no frames.
	mygo.App.Focus()
	w1.Focus()
	if !wait.Poll(mygo.App.IsActive) {
		r.note(phaseShown, "the app did not become active")
	}
	if !wait.Poll(func() bool { return frameOf(content1) > 0 }) {
		r.note(phaseShown, "no frame came")
	}
	v, err := w1.NewWebView(mygo.WebViewOptions{})
	if err != nil {
		r.note(phaseShown, "the web view: "+err.Error())
		return
	}
	w1.Update(func() { r.web = v })
	v.Page().LoadHTML(`<!doctype html><body style="margin:0;background:#fff"><script>window.ready = true</script></body>`, "")
	if !wait.Poll(func() bool { ok, err := mygo.EvalAs[bool](v.Page(), "window.ready === true"); return err == nil && ok }) {
		r.note(phaseShown, "the web view's page did not load")
	}
	if !wait.Poll(func() (shown bool) {
		mygo.RunOnMain(func() {
			shown = darwin.TestOnScreen(handle1) && findView(mainSend[objc.ID](objc.ID(handle1), sel("contentView")), "WKWebView") != 0
		})
		return shown
	}) {
		r.note(phaseShown, "the window or its web view did not show")
	}

	setPhase(phaseFrames)
	r.frames(phaseFrames, w1, content1, 0)

	setPhase(phaseInput)
	r.input(phaseInput, handle1, 0)

	setPhase(phaseRunOnMain)
	r.note(phaseRunOnMain, "")
	r.repeat(phaseRunOnMain, func() bool {
		const calls = 200
		start := r.ran.Load()
		for range calls {
			mygo.RunOnMain(func() { r.ran.Add(1) })
		}
		if n := r.ran.Load() - start; n != calls {
			r.note(phaseRunOnMain, fmt.Sprintf("%d of %d functions ran", n, calls))
			return false
		}
		return true
	})

	// AppKit hides the panel while the app is not active.
	setPhase(-1)
	mygo.App.Focus()
	w1.Focus()
	if !wait.Poll(mygo.App.IsActive) {
		r.note(phaseModal, "the app did not become active")
	}
	setPhase(phaseModal)
	r.modal(handle1)

	clicked := make(chan struct{}, 1)
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: "Test", Submenu: []*mygo.MenuItem{
			{Label: "Ping", Click: func(*mygo.MenuItem, *mygo.Window) { clicked <- struct{}{} }},
		}},
	}))
	setPhase(phaseMenu)
	r.note(phaseMenu, "")
	r.repeat(phaseMenu, func() bool {
		var menuErr error
		mygo.RunOnMain(func() { menuErr = darwin.TestPerformMenuItem("Test", "Ping") })
		if menuErr != nil {
			r.note(phaseMenu, menuErr.Error())
			return false
		}
		select {
		case <-clicked:
			return true
		case <-time.After(wait.Guard):
			r.note(phaseMenu, "the item's Click did not run")
			return false
		}
	})

	var layer1 objc.ID
	mygo.RunOnMain(func() { layer1 = metalLayer(handle1) })
	trimmed := func() (trimmed bool) {
		mygo.RunOnMain(func() { s := mainSend[size](layer1, sel("drawableSize")); trimmed = s.W == 1 && s.H == 1 })
		return trimmed
	}
	// A frame makes the drawables at full size again, and the renderer
	// trims them once the window stayed idle.
	setPhase(phaseTrim)
	r.note(phaseTrim, "")
	switch {
	case layer1 == 0:
		r.note(phaseTrim, "the window has no CAMetalLayer")
	case !func() bool { w1.Invalidate(); return wait.Poll(func() bool { return !trimmed() }) }():
		r.note(phaseTrim, "a frame did not make the drawable at full size")
	case !wait.Poll(trimmed):
		r.note(phaseTrim, "the drawable did not shrink to 1x1")
	}

	setPhase(phaseSignals)
	r.signals(w1, content1)

	// Window 2: native UI without a web view.
	setPhase(-1)
	w2 := mygo.NewWindow(mygo.WindowOptions{Title: "Main stack 2", X: 560, Y: 80, Width: 400, Height: 300, AlwaysOnTop: true, Content: ui.View(r.view(1))})
	handle2 := w2.NativeHandle()
	content2 := r.latestContent()
	if content2 == content1 || content2 == nil {
		r.note(phaseFrames2, "the window's content did not attach")
		return
	}
	r.content[1].Store(content2)
	mygo.App.Focus()
	w2.Focus()
	if !wait.Poll(func() (shown bool) {
		mygo.RunOnMain(func() { shown = darwin.TestOnScreen(handle2) })
		return shown && frameOf(content2) > 0
	}) {
		r.note(phaseFrames2, "the window did not show or build a frame")
	}

	setPhase(phaseFrames2)
	r.measuring.Store(true)
	r.frames(phaseFrames2, w2, content2, 1)
	r.measuring.Store(false)
	r.mu.Lock()
	marks := r.frameMarks
	r.mu.Unlock()
	r.countMarks(stepFrame, marks)

	setPhase(phaseInput2)
	r.input(phaseInput2, handle2, 1)

	setPhase(-1)
	mygo.App.Focus()
	w2.Focus()
	w2.OnResize(func() {
		// In a step, as the window resizes: the calls that the steps
		// forwarded since the last change of its size in the live resize
		// are those of that change.
		mark := r.mark()
		var live bool
		var f rect
		r.spend(func() {
			view := mainSend[objc.ID](objc.ID(handle2), sel("contentView"))
			live = mainSend[bool](view, sel("inLiveResize"))
			f = mainSend[rect](view, sel("frame"))
		})
		if !live {
			return
		}
		s := [2]int{int(math.Round(f.Size.W)), int(math.Round(f.Size.H))}
		r.mu.Lock()
		if n := len(r.live); n == 0 || r.live[n-1] != s {
			r.live = append(r.live, s)
			r.sizeMarks = append(r.sizeMarks, mark)
		}
		r.mu.Unlock()
	})
	var layer2 uintptr
	mygo.RunOnMain(func() { layer2 = uintptr(metalLayer(handle2)) })
	mygo.RunOnMain(func() { onPresent(func(layer, drawable uintptr) { r.presented(handle2, layer2, layer, drawable) }) })
	setPhase(phaseResize)
	r.resize(w2, content2)
	mygo.RunOnMain(func() { onPresent(nil) })
	r.mu.Lock()
	marks = r.sizeMarks
	r.mu.Unlock()
	r.countMarks(stepResize, marks)

	setPhase(-1)
	var associated bool
	mygo.RunOnMain(func() { associated = associateWeak(metalLayer(handle2), &weakLayerObject) })
	setPhase(phaseClose)
	r.note(phaseClose, "")
	if !associated {
		r.note(phaseClose, "the window has no CAMetalLayer")
	}
	w2.Close()
	if !wait.Poll(w2.IsDestroyed) {
		r.note(phaseClose, "the window was not destroyed")
	}
	if associated && !wait.Poll(func() bool {
		obj := objcLoadWeakRetained(&weakLayerObject)
		if obj != 0 {
			objc.ID(obj).Send(sel("release"))
		}
		return obj == 0
	}) {
		r.note(phaseClose, "the window's CAMetalLayer was not deallocated")
	}
	if associated {
		objcDestroyWeak(&weakLayerObject)
	}

	setPhase(phaseQuit)
	r.note(phaseQuit, "")
}

// frames has window i build framesAnimated frames, and checks that its
// frame count advanced by as many.
func (r *stackRun) frames(phase int, w *mygo.Window, c surface.Automated, i int) {
	r.note(phase, "")
	start := frameOf(c)
	r.animated[i].Store(0)
	r.animate[i].Store(true)
	// The first frame starts it: each build asks for the next.
	w.Invalidate()
	// Waiting on a channel, the goroutine sends nothing to the main
	// thread while the frames build.
	select {
	case <-r.framed[i]:
	case <-time.After(wait.Guard):
	}
	r.animate[i].Store(false)
	if end := frameOf(c); end < start+framesAnimated {
		r.note(phase, fmt.Sprintf("the frame count went from %d to %d", start, end))
	}
}

// framesAnimated is how many frames a window's frames phase waits for.
const framesAnimated = 30

// input moves the pointer over the box of window i ten times and presses
// a key five times, and checks that each event reached the box's input
// function, as often as the phase needs to take its samples.
func (r *stackRun) input(phase int, handle uintptr, i int) {
	r.note(phase, "")
	// In window 2, what each move and each key forwards: in a step, the
	// calls forwarded while the main thread handles the event, other than
	// the own of the mainSend that hands it the event, are those of the
	// steps of the event.
	event := func(k int, prepare func() (send func())) {
		mygo.RunOnMain(func() {
			send := prepare()
			before := r.mark()
			send()
			if i == 1 {
				r.count(k, r.mark()-before)
			}
		})
	}
	mygo.RunOnMain(func() {
		before := forwarded()
		mainSend[int](objc.ID(handle), sel("windowNumber"))
		r.own = forwarded() - before
	})
	r.repeat(phase, func() bool {
		const moves, keys = 10, 5
		moved, typed := r.moves[i].Load(), r.keys[i].Load()
		for m := range moves {
			event(stepMove, func() func() { return r.moveOver(handle, 40+float64(8*m), 60) })
		}
		for range keys {
			event(stepKey, func() func() { return r.pressKey(handle) })
		}
		if !wait.Poll(func() bool { return r.moves[i].Load()-moved >= moves && r.keys[i].Load()-typed >= keys }) || r.moves[i].Load()-moved != moves || r.keys[i].Load()-typed != keys {
			r.note(phase, fmt.Sprintf("the input function took %d of %d moves and %d of %d keys", r.moves[i].Load()-moved, moves, r.keys[i].Load()-typed, keys))
			return false
		}
		return true
	})
}

// minSamples is how many samples a phase that repeats what it does takes
// at least, so that the time the main thread spends in Go code shows in
// its count however short one repetition is.
const minSamples = 2000

// repeat runs once until the sampler took minSamples samples for phase,
// and stops when once reports a failure.
func (r *stackRun) repeat(phase int, once func() bool) {
	deadline := time.Now().Add(wait.Guard)
	for phaseSamples(phase) < minSamples {
		if !once() {
			return
		}
		if time.Now().After(deadline) {
			r.note(phase, fmt.Sprintf("the phase took %d of %d samples", phaseSamples(phase), minSamples))
			return
		}
	}
}

// modal shows an application-modal message panel and dismisses it.
func (r *stackRun) modal(handle uintptr) {
	r.note(phaseModal, "")
	returned := make(chan error, 1)
	go func() {
		_, err := mygo.Dialog.Message(mygo.MessageOptions{Message: "Main stack"})
		returned <- err
	}()
	if !wait.Poll(func() (shows bool) {
		mygo.RunOnMain(func() { running, on := darwin.TestModal(handle); shows = running && on })
		return shows
	}) {
		r.note(phaseModal, "the panel did not show")
	}
	var canceled bool
	mygo.RunOnMain(func() { canceled = darwin.TestCancelModal(handle) })
	if !canceled {
		r.note(phaseModal, "no panel to dismiss")
	}
	if !wait.Poll(func() bool { return len(returned) > 0 }) {
		r.note(phaseModal, "Dialog.Message did not return")
	}
}

// signals sends SIGCHLD and SIGURG to the process while window 1 builds
// frames, until the process received a thousand SIGCHLD and the window
// built 30 frames: the kernel delivers a signal sent to the process to a
// thread that does not block it.
func (r *stackRun) signals(w *mygo.Window, c surface.Automated) {
	r.note(phaseSignals, "")
	received := make(chan os.Signal, 4096)
	signal.Notify(received, syscall.SIGCHLD)
	defer signal.Stop(received)
	start := frameOf(c)
	r.animate[0].Store(true)
	defer r.animate[0].Store(false)
	w.Invalidate()
	var got int
	pid := os.Getpid()
	if !wait.Poll(func() bool {
		for range 100 {
			syscall.Kill(pid, syscall.SIGCHLD)
			syscall.Kill(pid, syscall.SIGURG)
		}
		for len(received) > 0 {
			<-received
			got++
		}
		return got >= 1000 && frameOf(c) >= start+30
	}) {
		r.note(phaseSignals, fmt.Sprintf("%d SIGCHLD received, %d frames built", got, frameOf(c)-start))
	}
}

// resize drags the lower right corner of window 2 with the left button,
// through events posted to the application's queue, so that AppKit
// resizes it live, and checks that a frame was built for each size that
// the window took during the live resize.
func (r *stackRun) resize(w *mygo.Window, c surface.Automated) {
	r.note(phaseResize, "")
	b := w.Bounds()
	x, y := float64(b.X+b.Width)-3, float64(b.Y+b.Height)-3
	handle := w.NativeHandle()
	// A live resize takes the latest of the drags that wait in the
	// queue: the frame of each size of the window posts the next drag,
	// and that of the last the release.
	drag := func(i int) {
		const leftMouseUp, leftMouseDragged = 2, 6
		switch {
		case i < resizeDrags:
			postMouse(handle, leftMouseDragged, x+float64(12*(i+1)), y+float64(9*(i+1)))
		case i == resizeDrags:
			postMouse(handle, leftMouseUp, x+12*resizeDrags, y+9*resizeDrags)
		}
	}
	mygo.RunOnMain(func() {
		const leftMouseDown = 1
		postMouse(handle, leftMouseDown, x, y)
		drag(0)
		r.mu.Lock()
		r.drag = drag
		r.mu.Unlock()
		// The application sends the press, which runs the live resize,
		// and the release ends it.
		darwin.TestAppMouseAt(handle)
		r.mu.Lock()
		r.drag = nil
		r.mu.Unlock()
	})
	r.mu.Lock()
	live := append([][2]int(nil), r.live...)
	r.mu.Unlock()
	if len(live) < 3 {
		r.note(phaseResize, fmt.Sprintf("the window took %d sizes in a live resize", len(live)))
		return
	}
	if !wait.Poll(func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, s := range live {
			if !r.drawn[s] {
				return false
			}
		}
		return true
	}) {
		r.mu.Lock()
		var missing [][2]int
		for _, s := range live {
			if !r.drawn[s] {
				missing = append(missing, s)
			}
		}
		r.mu.Unlock()
		r.note(phaseResize, fmt.Sprintf("no frame was built for the sizes %v of the live resize", missing))
	}
}

// resizeDrags is how many drags the live resize of window 2 takes.
const resizeDrags = 10

// postMouse posts an event of the left button of type typ at (x, y), in
// points from the top-left corner of the primary screen, to the
// application, for the window handle. In a step.
func postMouse(handle uintptr, typ uint, x, y float64) {
	win := objc.ID(handle)
	screens := mainSend[objc.ID](class("NSScreen"), sel("screens"))
	screen := mainSend[rect](objc.Send[objc.ID](screens, sel("objectAtIndex:"), 0), sel("frame"))
	f := mainSend[rect](win, sel("frame"))
	number := mainSend[int](win, sel("windowNumber"))
	ev := mainSend[objc.ID](class("NSEvent"), sel("mouseEventWithType:location:modifierFlags:timestamp:windowNumber:context:eventNumber:clickCount:pressure:"),
		typ, point{x - f.Origin.X, screen.Size.H - y - f.Origin.Y}, uint(0), float64(0), number, uintptr(0), 0, 1, float32(1))
	app := mainSend[objc.ID](class("NSApplication"), sel("sharedApplication"))
	mainSend[struct{}](app, sel("postEvent:atStart:"), ev, false)
}

// moveOver makes a pointer move to (x, y), in points from the top-left
// corner of the content of a window, and returns the function that has
// its surface view take it, which forwards the own calls of one
// mainSend. In a step.
func (r *stackRun) moveOver(handle uintptr, x, y float64) (send func()) {
	win := objc.ID(handle)
	content := mainSend[objc.ID](win, sel("contentView"))
	view := findView(content, "MyGoSurfaceView")
	frame := mainSend[rect](content, sel("frame"))
	number := mainSend[int](win, sel("windowNumber"))
	const mouseMoved = 5
	ev := mainSend[objc.ID](class("NSEvent"), sel("mouseEventWithType:location:modifierFlags:timestamp:windowNumber:context:eventNumber:clickCount:pressure:"),
		uint(mouseMoved), point{x, frame.Size.H - y}, uint(0), float64(0), number, uintptr(0), 0, 0, float32(0))
	return func() {
		mainSend[struct{}](view, sel("mouseMoved:"), ev)
		r.spent.Add(r.own)
	}
}

// pressKey makes a key down and up of "a" and returns the function that
// sends them to a window, as the keyboard does, whose forwarded calls are
// those of the key down: the step of a key is that of keyDown:. In a
// step.
func (r *stackRun) pressKey(handle uintptr) (send func()) {
	win := objc.ID(handle)
	number := mainSend[int](win, sel("windowNumber"))
	chars := []byte("a\x00")
	// A Foundation value object, which runs its messages on the calling
	// thread.
	s := class("NSString").Send(sel("alloc")).Send(sel("initWithUTF8String:"), unsafe.Pointer(&chars[0]))
	const keyDown, keyUp = 10, 11
	var evs []objc.ID
	for _, typ := range []uint{keyDown, keyUp} {
		evs = append(evs, mainSend[objc.ID](class("NSEvent"), sel("keyEventWithType:location:modifierFlags:timestamp:windowNumber:context:characters:charactersIgnoringModifiers:isARepeat:keyCode:"),
			typ, point{}, uint(0), float64(0), number, uintptr(0), s, s, false, uint16(0)))
	}
	return func() {
		mainSend[struct{}](win, sel("sendEvent:"), evs[0])
		r.spent.Add(r.own)
		r.spend(func() { mainSend[struct{}](win, sel("sendEvent:"), evs[1]) })
		s.Send(sel("release"))
	}
}

// presented checks, for a frame that the renderer of layer presents in
// drawable, that the texture of the drawable has the drawable size of the
// layer while window 2, whose renderer's layer is layer2, is in a live
// resize. In a step.
func (r *stackRun) presented(handle2, layer2, layer, drawable uintptr) {
	if layer != layer2 {
		return
	}
	var live bool
	var tex, layerSize size
	r.spend(func() {
		live = mainSend[bool](mainSend[objc.ID](objc.ID(handle2), sel("contentView")), sel("inLiveResize"))
		layerSize = mainSend[size](objc.ID(layer), sel("drawableSize"))
	})
	// The drawable's texture runs its messages on the calling thread.
	t := objc.ID(drawable).Send(sel("texture"))
	tex = size{float64(objc.Send[uint](t, sel("width"))), float64(objc.Send[uint](t, sel("height")))}
	if !live {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.presents.count++
	if tex != layerSize {
		r.presents.mismatched++
		if r.presents.first == "" {
			r.presents.first = fmt.Sprintf("%vx%v for %vx%v", tex.W, tex.H, layerSize.W, layerSize.H)
		}
	}
}

// metalLayer returns the CAMetalLayer of the surface view of a window. In
// a step.
func metalLayer(handle uintptr) objc.ID {
	view := findView(mainSend[objc.ID](objc.ID(handle), sel("contentView")), "MyGoSurfaceView")
	if view == 0 {
		return 0
	}
	return findLayer(mainSend[objc.ID](view, sel("layer")), "CAMetalLayer")
}

// associationKey is the key of the association, and weakLayerObject the
// weak reference to the associated object, which the runtime keeps the
// address of: package-level variables stay in place.
var (
	associationKey  byte
	weakLayerObject uintptr
)

// associateWeak associates a new object with layer, which then holds the
// only reference to it, and points the weak reference weak at the object,
// so that weak reads nil once the layer is deallocated. The object and the
// runtime run their calls on the calling thread.
func associateWeak(layer objc.ID, weak *uintptr) bool {
	if layer == 0 {
		return false
	}
	obj := class("NSObject").Send(sel("alloc")).Send(sel("init"))
	const retainNonatomic = 1
	objcSetAssociatedObj(uintptr(layer), uintptr(unsafe.Pointer(&associationKey)), uintptr(obj), retainNonatomic)
	objcInitWeak(weak, uintptr(obj))
	obj.Send(sel("release"))
	return true
}
