//go:build darwin

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/internal/darwin"
	"github.com/egoist/mygo/internal/gpu/metal"
	"github.com/egoist/mygo/ui"
)

const nativePage = `<!doctype html><body style="margin:0;background:#00f"><input id=a></body>`

// nativeRig is a window of native UI with a native view, in which a holder
// holds a web view and another a view showing an IOSurface, each named by
// a native view element with the inner view as its key view.
type nativeRig struct {
	w                      *mygo.Window
	handle, root, surface  uintptr
	webHolder, web         uintptr
	imageHolder, image, io uintptr
	// log is what the frames did, in order, on the main thread: "place W"
	// with the width of the web view's holder as the application set it,
	// "present" for a frame presenting on its own and "transact" for one
	// with the transaction.
	log []string
	// views is the last list of placements, on the main thread.
	views []mygo.NativePlacement
	// hideGone hides the views the frame does not show, as an application
	// does.
	hideGone bool

	showWeb, showImage, popover, focused atomic.Bool
	color, clicks                        atomic.Int32
	outside                              atomic.Int32
}

func newNativeRig(t *testing.T, noHit bool) *nativeRig {
	t.Helper()
	r := &nativeRig{hideGone: true}
	r.showWeb.Store(true)
	r.showImage.Store(true)
	r.color.Store(200)
	r.w = newWindow(t, mygo.WindowOptions{Title: "Native views", Width: 400, Height: 300, Content: ui.View(r.view)})
	waitOnScreen(r.w)
	mygo.RunOnMain(func() {
		r.handle = r.w.NativeHandle()
		r.surface = darwin.TestSurfaceView(r.handle)
		r.root = darwin.TestNewView(false)
		r.webHolder = darwin.TestNewView(noHit)
		r.imageHolder = darwin.TestNewView(noHit)
		r.web = darwin.TestNewWebKitView(nativePage)
		r.image, r.io = darwin.TestNewSurfaceView(100, 100)
		darwin.TestAddSubview(r.root, r.webHolder)
		darwin.TestAddSubview(r.root, r.imageHolder)
		darwin.TestAddSubview(r.webHolder, r.web)
		darwin.TestAddSubview(r.imageHolder, r.image)
	})
	if err := r.w.NewNativeView(r.root, r.place); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the web view to load", func() (done bool) {
		mygo.RunOnMain(func() { done = darwin.TestWebKitLoaded(r.web) })
		return done
	})
	r.w.Update(func() {})
	r.placed(t, "the first frame", func() bool { return len(r.views) == 2 })
	return r
}

// view shows the holders side by side, under a band that a test can
// recolor without moving anything, with a popover of the content over the
// web view on demand.
func (r *nativeRig) view(c *ui.Context) {
	ui.Column(c).Fill().AlignItems(ui.Stretch).Children(func() {
		ui.Box(c).Height(20).Background(ui.RGB(uint8(r.color.Load()), 0, 0))
		ui.Row(c).Grow(1).Children(func() {
			if r.root == 0 {
				return
			}
			if r.showWeb.Load() {
				e := ui.NativeView(c, r.webHolder, r.web).Grow(1).Label("web")
				if e.Clicked() {
					r.clicks.Add(1)
				}
				r.focused.Store(e.Focused())
				if r.popover.Load() {
					e.Children(func() {
						ui.Box(c).Absolute().Left(10).Top(10).Size(50, 50).Background(ui.RGB(0, 200, 0)).Cursor(ui.CursorCrosshair)
					})
				}
			}
			if r.showImage.Load() {
				ui.NativeView(c, r.imageHolder, r.image).Grow(1)
			}
		})
	})
}

// place applies what a frame shows to the views, as an application does.
func (r *nativeRig) place(views []mygo.NativePlacement) {
	r.views = slices.Clone(views)
	shown := map[uintptr]bool{}
	for _, p := range views {
		shown[p.View] = true
		darwin.TestSetHidden(p.View, false)
		darwin.TestSetFrame(p.View, p.Box.X, p.Box.Y, p.Box.W, p.Box.H)
		inner := r.web
		if p.View == r.imageHolder {
			inner = r.image
		}
		darwin.TestSetFrame(inner, 0, 0, p.Box.W, p.Box.H)
		if p.View == r.webHolder {
			r.log = append(r.log, fmt.Sprint("place ", p.Box.W))
		}
	}
	if r.hideGone {
		for _, v := range []uintptr{r.webHolder, r.imageHolder} {
			if !shown[v] {
				darwin.TestSetHidden(v, true)
			}
		}
	}
}

// onMain runs fn on the main thread.
func onMain(fn func()) { mygo.RunOnMain(fn) }

// placed waits for a condition on the placements.
func (r *nativeRig) placed(t *testing.T, what string, cond func() bool) {
	t.Helper()
	eventually(t, what, func() (ok bool) {
		onMain(func() { ok = cond() })
		return ok
	})
}

// logFrames has the frames of the test noted in r.log until it ends.
func (r *nativeRig) logFrames(t *testing.T) {
	metal.TestPresenting = func(layer, drawable uintptr) {
		if darwin.TestPresentsWithTransaction(layer) {
			r.log = append(r.log, "transact")
		} else {
			r.log = append(r.log, "present")
		}
	}
	t.Cleanup(func() { metal.TestPresenting = nil })
}

// frameLog returns what the frames did since it last was asked.
func (r *nativeRig) frameLog() []string {
	var l []string
	onMain(func() {
		l = r.log
		r.log = nil
	})
	return l
}

// waitLog waits for a frame to note want.
func (r *nativeRig) waitLog(t *testing.T, want string) []string {
	t.Helper()
	var l []string
	eventually(t, "a frame to note "+want, func() bool {
		l = append(l, r.frameLog()...)
		return slices.Contains(l, want)
	})
	return l
}

// TestNativeViewPlaced places a view of the application under the content
// and checks that it stays there, under the surface and over nothing else
// of the window, filling the content as the window resizes, that a window
// takes one, and that it leaves the window only as the window closes.
func TestNativeViewPlaced(t *testing.T) {
	r := newNativeRig(t, false)
	var order []uintptr
	onMain(func() { order = darwin.TestContentOrder(r.handle) })
	if i, j := slices.Index(order, r.root), slices.Index(order, r.surface); i < 0 || j < i {
		t.Fatalf("the content holds %v: want the native view %d under the surface %d", order, r.root, r.surface)
	}
	if err := r.w.NewNativeView(r.root, r.place); err == nil {
		t.Fatal("a window took a second native view")
	}
	r.w.SetSize(500, 350)
	r.placed(t, "the native view to fill the content", func() bool {
		_, _, w, h := darwin.TestViewFrame(r.root)
		_, _, cw, ch := darwin.TestViewFrame(r.surface)
		return w == 500 && w == cw && h == ch
	})
	onMain(func() {
		if n := darwin.TestLeftWindow(r.root); n != 0 {
			t.Errorf("the native view left its window %d times while it is open", n)
		}
	})
	r.w.Destroy()
	eventually(t, "the native view to leave the closed window", func() (left bool) {
		onMain(func() { left = darwin.TestLeftWindow(r.root) == 1 })
		return left
	})
}

// TestNativeViewResize resizes a window 200 times and checks that in each
// frame the application applies the box of each holder before the frame's
// present, and that the frame presents with the transaction.
func TestNativeViewResize(t *testing.T) {
	r := newNativeRig(t, false)
	r.logFrames(t)
	r.frameLog()
	for i := 1; i <= 200; i++ {
		width := 400 + i*2
		r.w.SetSize(width, 300)
		want := fmt.Sprint("place ", width/2)
		l := r.waitLog(t, want)
		for len(l) > 0 && l[len(l)-1] == want { // its present comes next
			l = append(l, r.waitLogAny(t)...)
		}
		at := slices.Index(l, want)
		if at+1 >= len(l) || l[at+1] != "transact" {
			t.Fatalf("step %d: the frames did %v: want %q and its frame presenting with the transaction", i, l, want)
		}
	}
}

// waitLogAny waits for a frame to note anything.
func (r *nativeRig) waitLogAny(t *testing.T) []string {
	t.Helper()
	var l []string
	eventually(t, "a frame", func() bool {
		l = r.frameLog()
		return len(l) > 0
	})
	return l
}

// TestNativeViewPresentsAlone checks that a frame that changes nothing of
// an element nor of the layer presents without the transaction, and one
// that changes a cover with it.
func TestNativeViewPresentsAlone(t *testing.T) {
	r := newNativeRig(t, false)
	r.logFrames(t)
	r.frameLog()
	r.color.Store(100)
	r.w.Update(func() {})
	if l := r.waitLogAny(t); slices.Contains(l, "transact") {
		t.Fatalf("a frame that changes a color did %v: want it presenting on its own", l)
	}
	r.popover.Store(true)
	r.w.Update(func() {})
	r.placed(t, "the cover", func() bool { return len(r.views) > 0 && len(r.views[0].Covers) == 1 })
	r.waitLog(t, "transact")
}

// TestNativeViewHoldFrames checks that a frame requested between
// HoldFrames and ReleaseFrames, or asked for by AppKit as the window
// resizes, draws after the release and not before, and that the layer
// keeps its size at the top left of the content meanwhile.
func TestNativeViewHoldFrames(t *testing.T) {
	r := newNativeRig(t, false)
	r.logFrames(t)
	r.frameLog()
	var drawnW, drawnH float64
	onMain(func() { _, _, drawnW, drawnH = darwin.TestViewFrame(r.surface) })
	r.w.HoldFrames()
	r.color.Store(50)
	r.w.Update(func() {})
	r.w.SetSize(600, 450)
	// No frame may come: the guard is the wait for one that would.
	time.Sleep(300 * time.Millisecond)
	if l := r.frameLog(); len(l) != 0 {
		t.Fatalf("while the window holds its frames, the frames did %v", l)
	}
	onMain(func() {
		x, y, w, h := darwin.TestMetalLayerFrame(r.handle)
		if x != 0 || y != 0 || w != drawnW || h != drawnH {
			t.Errorf("while held, the layer is at (%v, %v), %v by %v: want %v by %v at the top left", x, y, w, h, drawnW, drawnH)
		}
	})
	r.w.ReleaseFrames()
	l := r.waitLog(t, "place 300")
	presents := 0
	for _, e := range append(l, r.frameLog()...) {
		if e == "transact" || e == "present" {
			presents++
		}
	}
	if presents > 1 {
		t.Fatalf("the release drew %d frames: %v", presents, l)
	}
	r.placed(t, "the layer at the new size", func() bool {
		_, _, w, h := darwin.TestMetalLayerFrame(r.handle)
		_, _, cw, ch := darwin.TestViewFrame(r.surface)
		return w == 600 && w == cw && h == ch
	})
}

// TestNativeViewIOSurface checks that the IOSurface a holder shows is in
// use while presented, and released after.
func TestNativeViewIOSurface(t *testing.T) {
	r := newNativeRig(t, false)
	eventually(t, "the IOSurface in use", func() (used bool) {
		onMain(func() { used = darwin.TestIOSurfaceInUse(r.io) })
		return used
	})
	onMain(func() { darwin.TestClearContents(r.image) })
	eventually(t, "the IOSurface released", func() (used bool) {
		onMain(func() { used = darwin.TestIOSurfaceInUse(r.io) })
		return !used
	})
}

// TestNativeViewPointer checks that the view takes the pointer where an
// element shows and nothing covers it, that a popover over a hole takes
// the pointer and sets the cursor there, and that a press on the view
// reaches the content as a notice, which moves its focus.
func TestNativeViewPointer(t *testing.T) {
	r := newNativeRig(t, false)
	r.w.Focus()
	eventually(t, "the window focused", r.w.IsFocused)
	r.popover.Store(true)
	r.w.Update(func() {})
	r.placed(t, "the cover", func() bool { return len(r.views[0].Covers) == 1 })
	var hit, over uintptr
	var leaves, leavesOver bool
	onMain(func() {
		hit = darwin.TestHitTest(r.handle, 150, 200)
		over = darwin.TestHitTest(r.handle, 20, 40)
		leaves = darwin.TestLeavesCursor(r.handle, 150, 200)
		leavesOver = darwin.TestLeavesCursor(r.handle, 20, 40)
	})
	var inWeb bool
	onMain(func() { inWeb = darwin.TestIsInside(hit, r.web) })
	if !inWeb {
		t.Fatalf("the pointer over the hole hits %d: want the web view %d", hit, r.web)
	}
	if over != r.surface {
		t.Fatalf("the pointer over the popover hits %d: want the surface %d", over, r.surface)
	}
	if !leaves || leavesOver {
		t.Fatalf("the content leaves the cursor over the hole %v, over the popover %v", leaves, leavesOver)
	}
	onMain(func() { darwin.TestMoveMouse(r.handle, 20, 40) })
	eventually(t, "the popover's cursor", func() (set bool) {
		onMain(func() { set = darwin.TestContentCursor(r.handle) == int(ui.CursorCrosshair) })
		return set
	})
	before := r.clicks.Load()
	appClick(r.w, 150, 200)
	eventually(t, "the press to reach the web view", func() (in bool) {
		onMain(func() { in = darwin.TestIsInside(darwin.TestFirstResponder(r.handle), r.web) })
		return in
	})
	if r.clicks.Load() != before {
		t.Fatal("the content took a click on the view")
	}
	// The web view has the keyboard, so no element shows the focus: the
	// content's gets the focus the press moved once the keyboard is back.
	onMain(func() { darwin.TestGiveKeyboard(r.handle, r.surface) })
	eventually(t, "the notice of the press to focus the element", r.focused.Load)
}

// TestNativeViewNoHitTarget checks that the content takes the pointer over
// a hole whose holder finds no view there.
func TestNativeViewNoHitTarget(t *testing.T) {
	r := newNativeRig(t, true)
	var hit uintptr
	onMain(func() { hit = darwin.TestHitTest(r.handle, 150, 200) })
	if hit != r.surface {
		t.Fatalf("the pointer hits %d: want the surface %d", hit, r.surface)
	}
	appClick(r.w, 150, 200)
	eventually(t, "the click to reach the content", func() bool { return r.clicks.Load() == 1 })
}

// keyboardIn reports whether the keyboard is in view.
func (r *nativeRig) keyboardIn(view uintptr) (in bool) {
	onMain(func() { in = darwin.TestIsInside(darwin.TestFirstResponder(r.handle), view) })
	return in
}

// TestNativeViewKeyboard checks that Tab moves the keyboard into the web
// view and out of it, and that an element not drawn while its view holds
// the keyboard gives the keyboard to the content.
func TestNativeViewKeyboard(t *testing.T) {
	r := newNativeRig(t, false)
	r.w.Focus()
	eventually(t, "Tab into the web view", func() bool {
		pressTab(r.w, false)
		return r.keyboardIn(r.web)
	})
	pressTab(r.w, false)
	eventually(t, "Tab out of the web view", func() bool { return r.keyboardIn(r.surface) })
	eventually(t, "Tab into the web view again", func() bool {
		pressTab(r.w, false)
		return r.keyboardIn(r.web)
	})
	r.showWeb.Store(false)
	r.hideGone = false
	r.w.Update(func() {})
	eventually(t, "the keyboard to come back to the content", func() bool { return r.keyboardIn(r.surface) })
}

// TestNativeViewHiddenKeyboard checks that a holder that the application
// hides before the frame leaves the keyboard where AppKit moved it.
func TestNativeViewHiddenKeyboard(t *testing.T) {
	r := newNativeRig(t, false)
	r.w.Focus()
	eventually(t, "Tab into the web view", func() bool {
		pressTab(r.w, false)
		return r.keyboardIn(r.web)
	})
	var moved uintptr
	onMain(func() {
		darwin.TestSetHidden(r.webHolder, true)
		moved = darwin.TestFirstResponder(r.handle)
	})
	r.showWeb.Store(false)
	r.w.Update(func() {})
	r.placed(t, "the frame without the element", func() bool { return len(r.views) == 1 })
	var now uintptr
	onMain(func() { now = darwin.TestFirstResponder(r.handle) })
	if now != moved {
		t.Fatalf("the keyboard went from %d, where AppKit moved it, to %d", moved, now)
	}
}

// TestNativeViewAccessibility checks that assistive technology finds what
// is in a holder in its element, and only there.
func TestNativeViewAccessibility(t *testing.T) {
	r := newNativeRig(t, false)
	var extra uintptr
	onMain(func() { extra = darwin.TestAddAccessElement(r.webHolder) })
	accessibility(r.w)
	r.w.Update(func() {})
	var parent uintptr
	eventually(t, "the web view in its element", func() bool {
		accessibility(r.w)
		r.w.Update(func() {})
		onMain(func() { parent = darwin.TestAccessParent(r.web) })
		return parent != r.webHolder && parent != 0 && slices.Contains(tryChildren(parent), r.web)
	})
	onMain(func() {
		for _, obj := range []uintptr{r.web, extra} {
			if got := darwin.TestAccessParent(obj); got != parent {
				t.Errorf("the accessibility parent of %d is %d: want the element %d", obj, got, parent)
			}
			if holders := darwin.TestAccessHolders(r.handle, obj); !slices.Equal(holders, []uintptr{parent}) {
				t.Errorf("%d is an accessibility child of %v: want the element %d alone", obj, holders, parent)
			}
		}
		if kids := darwin.TestAccessChildren(r.root); len(kids) != 0 {
			t.Errorf("the native view reports the accessibility children %v", kids)
		}
	})
}

func tryChildren(obj uintptr) (kids []uintptr) {
	onMain(func() { kids = darwin.TestAccessChildren(obj) })
	return kids
}

// TestNativeViewDrop checks that a file dropped on the content reaches
// the content.
func TestNativeViewDrop(t *testing.T) {
	r := newNativeRig(t, false)
	path := filepath.Join(t.TempDir(), "dropped.txt")
	if err := os.WriteFile(path, []byte("dropped"), 0o644); err != nil {
		t.Fatal(err)
	}
	var files atomic.Int32
	r.w.OnFileDrop(func(e *mygo.FileDropEvent) { files.Add(int32(len(e.Paths))) })
	if over, dropped, _ := dropFiles(r.w, 200, 10, []string{path}); !over || !dropped {
		t.Fatalf("the drop on the content: over %v, dropped %v", over, dropped)
	}
	eventually(t, "the file to reach the content", func() bool { return files.Load() == 1 })
}
