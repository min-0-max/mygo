package ui

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/internal/platform"
	"github.com/egoist/mygo/internal/scene"
	"github.com/egoist/mygo/internal/surface"
)

// nativePlaced returns where the last frame of tt shows the native view
// element naming view.
func nativePlaced(tt *Tester, view uintptr) (placedWebView, bool) {
	return placed(tt, nativeElement{view, view + 1})
}

// nativeTester shows the element of nativePlaced under a toolbar, while
// *show, with a box over it while *menu.
func nativeTester(show, menu *bool) *Tester {
	return coreNewTester(func(c *context) {
		coreColumn(c).Fill().AlignItems(Stretch).Children(func() {
			coreText(c, "Toolbar").Height(40)
			if *show {
				coreNativeView(c, 7, 8).Grow(1).Radius(12).Padding(10).Children(func() {
					if *menu {
						coreBox(c).Absolute().Left(20).Top(20).Size(50, 30).Background(RGB(0, 0, 0))
					}
				})
			}
		})
	}, 300, 240)
}

// TestNativeViewElement checks that a native view element paints a hole
// over its content box, tells its box, clip and the covers of what paints
// over it, and is not placed by a frame that does not build it.
func TestNativeViewElement(t *testing.T) {
	show, menu := true, false
	tt := nativeTester(&show, &menu)
	p, ok := nativePlaced(tt, 7)
	if !ok {
		t.Fatal("the element is not placed")
	}
	want := platform.RectF{X: 10, Y: 50, W: 280, H: 180}
	if p.frame != want || p.clip != want {
		t.Fatalf("placed at %+v clipped by %+v, want %+v", p.frame, p.clip, want)
	}
	if len(p.covers) != 0 {
		t.Fatalf("covers %+v with nothing over it", p.covers)
	}
	holes := slices.DeleteFunc(slices.Clone(tt.rt.scene.Ops), func(op scene.Op) bool { return op.Kind != scene.OpHole })
	if len(holes) != 1 || holes[0].Rect != (scene.Rect{X: 10, Y: 50, W: 280, H: 180}) {
		t.Fatalf("holes %+v: want one over the box", holes)
	}
	menu = true
	tt.rt.changed()
	tt.Frame()
	p, _ = nativePlaced(tt, 7)
	if covers := []platform.RectF{{X: 20, Y: 60, W: 50, H: 30}}; !slices.Equal(p.covers, covers) {
		t.Fatalf("covers %+v, want %+v", p.covers, covers)
	}
	show = false
	tt.rt.changed()
	tt.Frame()
	if _, ok := nativePlaced(tt, 7); ok {
		t.Fatal("placed by a frame that does not build it")
	}
}

// TestNativeViewKey checks that the key view of an element is its view
// unless it names another.
func TestNativeViewKey(t *testing.T) {
	tt := coreNewTester(func(c *context) {
		coreNativeView(c, 7, 0).Height(10)
	}, 100, 100)
	if _, ok := placed(tt, nativeElement{7, 7}); !ok {
		t.Fatal("the key view of an element naming none is not its view")
	}
}

// TestNativeViewTab checks that Tab enters the key view of an element and
// that Tab out of it moves the focus on.
func TestNativeViewTab(t *testing.T) {
	var before, after string
	tt := coreNewTester(func(c *context) {
		coreBox(c).Fill().Children(func() {
			coreTextInput(c, &before).Label("Before")
			coreNativeView(c, 7, 8).Height(100)
			coreTextInput(c, &after).Label("After")
		})
	}, 200, 200)
	tt.Key(0, KeyTab)
	tt.Key(0, KeyTab)
	if tt.h.tabbedInto != (nativeElement{7, 8}) || tt.h.tabbedBack {
		t.Fatalf("Tab gave %v the keyboard (back %v): want the element", tt.h.tabbedInto, tt.h.tabbedBack)
	}
	tt.rt.webViewTabOut(nativeElement{7, 8}, false)
	tt.Frame()
	if !tt.Focused("After") {
		t.Fatal("Tab out of the key view did not go on from the element")
	}
}

// TestNativeViewCursor checks that the content keeps its cursor over an
// element, which the platform leaves out where the view takes the
// pointer, and can set it where the view takes none.
func TestNativeViewCursor(t *testing.T) {
	tt := coreNewTester(func(c *context) {
		coreNativeView(c, 7, 8).Fill().Cursor(CursorCrosshair)
	}, 100, 100)
	tt.Move(50, 50)
	tt.Frame()
	if tt.h.cursor != CursorCrosshair {
		t.Fatalf("cursor %v over a native view element: want the element's", tt.h.cursor)
	}
}

// orderGPU records, in order with what the test adds, the frames it
// presents, with whether each presents with the transaction, and tells
// what comes of them as the test says.
type orderGPU struct {
	log      *[]string
	transact []bool
	held     []bool
	came     platform.Presentation
}

func (g *orderGPU) Render(*scene.Scene) error { return nil }
func (g *orderGPU) Release()                  {}
func (g *orderGPU) RenderNotice(s *scene.Scene, done func(platform.Presentation, error), _ func()) error {
	*g.log = append(*g.log, "present")
	g.transact = append(g.transact, s.Transact)
	done(g.came, nil)
	return nil
}
func (g *orderGPU) HoldFrames(hold bool) { g.held = append(g.held, hold) }

// nativeHost is a window host drawing with an orderGPU, with a surface
// that counts the frames it is asked for, showing nativeTester's view.
type nativeHost struct {
	*windowHost
	s     *occludedSurface
	g     *orderGPU
	log   []string
	shown bool
	menu  bool
	place int
}

func newNativeHost(t *testing.T) *nativeHost {
	t.Setenv("MYGO_GPU", "")
	n := &nativeHost{s: &occludedSurface{}, shown: true}
	n.g = &orderGPU{log: &n.log}
	newGPU = func(platform.SurfaceNative) (gpuRenderer, error) { return n.g, nil }
	t.Cleanup(func() { newGPU = newGPURenderer })
	n.windowHost = &windowHost{conn: &surface.Conn{Surface: n.s, Post: func(f func()) { f() }}}
	n.conn.PlaceNativeViews = func([]platform.NativeViewPlacement) {
		n.place++
		n.log = append(n.log, "place")
	}
	n.conn.HoldFrames = n.holdFrames
	n.rt = newRuntime(func(c *context) {
		if n.shown {
			coreNativeView(c, 7, 8).Fill().Children(func() {
				if n.menu {
					coreBox(c).Size(20, 20).Background(RGB(0, 0, 0))
				}
			})
		}
	}, n.windowHost)
	return n
}

func (n *nativeHost) frame() { n.event(platform.SurfaceEvent{Kind: platform.SurfaceFrame}) }

// lastTransact returns whether the last frame presented with the
// transaction, and forgets what came before.
func (n *nativeHost) lastTransact(t *testing.T) bool {
	t.Helper()
	if len(n.g.transact) == 0 {
		t.Fatal("no frame presented")
	}
	return n.g.transact[len(n.g.transact)-1]
}

// TestNativeViewPlacedBeforePresent checks that the application hears
// where a frame shows an element before the frame presents, in each frame
// that draws it and in the frame after the last that did.
func TestNativeViewPlacedBeforePresent(t *testing.T) {
	n := newNativeHost(t)
	n.frame()
	n.rt.changed()
	n.frame()
	if want := []string{"place", "present", "place", "present"}; !slices.Equal(n.log, want) {
		t.Fatalf("frames ran %v, want %v", n.log, want)
	}
	n.shown = false
	n.rt.changed()
	n.frame()
	n.rt.changed()
	n.frame()
	if n.place != 3 {
		t.Fatalf("the application heard of %d frames, want 3: two that draw the element and the first without it", n.place)
	}
}

// TestFrameTransactsOnChange checks that a frame presents with the
// transaction only when it changes a placement, until one that does
// presents.
func TestFrameTransactsOnChange(t *testing.T) {
	n := newNativeHost(t)
	n.frame()
	if !n.lastTransact(t) {
		t.Fatal("the frame placing the element first presented on its own")
	}
	n.rt.changed()
	n.frame()
	if n.lastTransact(t) {
		t.Fatal("a frame that changes no placement presented with the transaction")
	}
	n.menu = true
	n.rt.changed()
	n.frame()
	if !n.lastTransact(t) {
		t.Fatal("a frame that changes a cover presented on its own")
	}
	n.g.came = platform.PresentNoDrawable
	n.menu = false
	n.rt.changed()
	n.frame()
	n.g.came = platform.Presented
	n.rt.changed()
	n.frame()
	if !n.lastTransact(t) {
		t.Fatal("the frame after one that found no drawable left the change on its own")
	}
	n.rt.changed()
	n.frame()
	if n.lastTransact(t) {
		t.Fatal("the change presented twice with the transaction")
	}
}

// TestBuiltFramePlacesNothing checks that a frame built for something
// other than presenting, as for a capture, tells nothing, and that the
// next frame tells it.
func TestBuiltFramePlacesNothing(t *testing.T) {
	n := newNativeHost(t)
	n.frame()
	n.shown = false
	n.place = 0
	n.rt.changed()
	n.capture()
	if n.place != 0 {
		t.Fatal("a capture placed the elements")
	}
	n.frame()
	if n.place != 1 || !n.lastTransact(t) {
		t.Fatalf("the frame after a capture told %d placements, with the transaction %v", n.place, n.lastTransact(t))
	}
}

// TestHoldFrames checks that no frame draws while the window holds its
// frames, that what was asked for meanwhile draws as one frame after the
// release, and that a notice hears of the hold.
func TestHoldFrames(t *testing.T) {
	n := newNativeHost(t)
	n.frame()
	var waiting presentations
	n.afterPresent(waiting.add)
	n.holdFrames(true)
	waiting.want(t, "waiting as the frames are held", platform.PresentHeld)
	var asked presentations
	n.afterPresent(asked.add)
	asked.want(t, "asked for while held", platform.PresentHeld)
	drawn := len(n.g.transact)
	n.rt.changed()
	n.frame()
	n.frame()
	if len(n.g.transact) != drawn {
		t.Fatal("a frame drew while the window holds its frames")
	}
	requested := n.s.requested
	n.holdFrames(false)
	if n.s.requested != requested+1 {
		t.Fatalf("the release asked for %d frames, want 1", n.s.requested-requested)
	}
	n.frame()
	if len(n.g.transact) != drawn+1 {
		t.Fatalf("%d frames after the release, want 1", len(n.g.transact)-drawn)
	}
	if !slices.Equal(n.g.held, []bool{true, false}) {
		t.Fatalf("the renderer heard %v", n.g.held)
	}
	n.holdFrames(true)
	requested = n.s.requested
	n.holdFrames(false)
	if n.s.requested != requested {
		t.Fatal("a release asked for a frame that nothing asked for")
	}
}

// TestNativeViewAccessNode checks that assistive technology finds what the
// named view holds in a node inside the element, whatever role the element
// takes, a button's that has no children among them.
func TestNativeViewAccessNode(t *testing.T) {
	tt := coreNewTester(func(c *context) {
		e := coreNativeView(c, 7, 8).Size(100, 100).Label("Editor")
		e.Clicked()
	}, 300, 240)
	tt.send(platform.SurfaceEvent{Kind: platform.AccessibilityOn})
	tree := tt.h.access
	if tree == nil {
		t.Fatal("no tree after AccessibilityOn")
	}
	for _, n := range tree.Nodes {
		if n.View == 7 {
			if n.KeyView != 8 || tree.Nodes[n.Parent].Label != "Editor" {
				t.Errorf("the node of the view: %+v, inside %+v", n, tree.Nodes[n.Parent])
			}
			return
		}
	}
	t.Errorf("no node of the view in %+v", tree.Nodes)
}
