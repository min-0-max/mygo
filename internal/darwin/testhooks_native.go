//go:build darwin

package darwin

import (
	"sync"

	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/darwin/mainthread"
)

// The functions in this file make the views of an application for the GUI
// tests of native views in internal/e2e, and tell what became of them.
// They must run on the main thread.

var (
	testNativeOnce sync.Once
	// testLeftWindow counts the viewWillMoveToWindow: with nil of the
	// views of class MyGoTestHostView, by view.
	testLeftWindow = map[id]int{}
	// testNoHit are the views of that class that find no view in a hit
	// test.
	testNoHit = map[id]bool{}
)

// TestNewView makes a flipped view that notes what TestLeftWindow reads,
// and that finds no view in a hit test when noHit is set. Its caller owns
// it.
func TestNewView(noHit bool) uintptr {
	testNativeOnce.Do(func() {
		classDef("MyGoTestHostView", "NSView", nil, []objc.MethodDef{
			method("isFlipped", func(self id, _ objc.SEL) bool { return true }),
			method("hitTest:", func(self id, cmd objc.SEL, p NSPoint) id {
				if testNoHit[self] {
					return 0
				}
				return sendSuperPoint(self, "MyGoTestHostView", cmd, p)
			}),
			method("viewWillMoveToWindow:", func(self id, cmd objc.SEL, win id) {
				if win == 0 {
					testLeftWindow[self]++
				}
				sendSuper(self, "MyGoTestHostView", cmd, uintptr(win))
			}),
		})
	})
	v := msgInitRect(send(class("MyGoTestHostView"), "alloc"), sel("initWithFrame:"), NSRect{})
	testNoHit[v] = noHit
	return uintptr(v)
}

// TestLeftWindow returns how many times view of TestNewView was told it
// moves to no window.
func TestLeftWindow(view uintptr) int { return testLeftWindow[id(view)] }

// TestNewWebKitView makes a WKWebView of its own configuration, showing
// html, the way an application does, and returns it, owned by its caller.
func TestNewWebKitView(html string) uintptr {
	var web id
	withPool(func() {
		cfg := send(send(class("WKWebViewConfiguration"), "alloc"), "init")
		web = msgInitRectID(send(class("WKWebView"), "alloc"), sel("initWithFrame:configuration:"), NSRect{}, cfg)
		release(cfg)
		send(web, "loadHTMLString:baseURL:", uintptr(nsString(html)), 0)
	})
	return uintptr(web)
}

// TestWebKitLoaded reports whether a WKWebView finished loading.
func TestWebKitLoaded(web uintptr) bool { return !sendBool(id(web), "isLoading") }

// TestAddSubview puts child in parent, and TestSetFrame sets the frame of
// a view.
func TestAddSubview(parent, child uintptr) { send(id(parent), "addSubview:", child) }

func TestSetFrame(view uintptr, x, y, w, h float64) {
	msgSetRect(id(view), sel("setFrame:"), NSRect{Origin: NSPoint{x, y}, Size: NSSize{w, h}})
}

// TestViewFrame returns the frame of a view.
func TestViewFrame(view uintptr) (x, y, w, h float64) {
	r := msgRect(id(view), sel("frame"))
	return r.Origin.X, r.Origin.Y, r.Size.Width, r.Size.Height
}

// TestSetHidden hides or shows a view.
func TestSetHidden(view uintptr, hidden bool) {
	on := uintptr(0)
	if hidden {
		on = 1
	}
	send(id(view), "setHidden:", on)
}

// TestSetHiddenInTransaction commits only the child's visibility change;
// it does not invalidate or draw the window's native UI surface.
func TestSetHiddenInTransaction(view uintptr, hidden bool) {
	tx := class("CATransaction")
	send(tx, "begin")
	send(tx, "setDisableActions:", 1)
	TestSetHidden(view, hidden)
	send(tx, "commit")
}

// TestWindowBackingScale reports the real window's point-to-pixel scale.
func TestWindowBackingScale(handle uintptr) float64 {
	return msgFloat(theBackend.byNSWindow[id(handle)].win, sel("backingScaleFactor"))
}

// TestContentOrder returns the subviews of a window's content view, from
// the back: the surface's view as the view TestSurfaceView returns.
func TestContentOrder(handle uintptr) []uintptr {
	var order []uintptr
	withPool(func() {
		for _, v := range arrayItems(send(send(id(handle), "contentView"), "subviews")) {
			order = append(order, uintptr(v))
		}
	})
	return order
}

// TestSurfaceView returns the view of a window's surface.
func TestSurfaceView(handle uintptr) uintptr {
	return uintptr(theBackend.byNSWindow[id(handle)].surface.view)
}

// TestFirstResponder returns the first responder of a window.
func TestFirstResponder(handle uintptr) uintptr {
	return uintptr(send(id(handle), "firstResponder"))
}

// TestGiveKeyboard makes view the first responder of a window.
func TestGiveKeyboard(handle, view uintptr) {
	send(id(handle), "makeFirstResponder:", view)
}

// TestIsInside reports whether view is inside, or is, outer.
func TestIsInside(view, outer uintptr) bool {
	return view != 0 && sendBool(id(view), "isKindOfClass:", uintptr(class("NSView"))) && sendBool(id(view), "isDescendantOf:", outer)
}

// TestHitTest returns the view AppKit finds for the pointer at x, y of a
// window's content.
func TestHitTest(handle uintptr, x, y float64) uintptr {
	content := send(id(handle), "contentView")
	h := msgRect(content, sel("bounds")).Size.Height
	return uintptr(msgHitTest(content, sel("hitTest:"), NSPoint{x, h - y}))
}

// TestLeavesCursor reports whether the content of a window leaves the
// cursor to what is under it at x, y, and TestContentCursor returns the
// cursor it sets.
func TestLeavesCursor(handle uintptr, x, y float64) bool {
	s := theBackend.byNSWindow[id(handle)].surface
	return s.leavesCursor(msgPointFromView(s.view, sel("convertPoint:toView:"), NSPoint{x, y}, 0))
}

func TestContentCursor(handle uintptr) int {
	return int(theBackend.byNSWindow[id(handle)].surface.cursor)
}

// TestMoveMouse tells the surface of a window that the mouse moved to
// x, y of its content, with no move of the pointer on the screen.
func TestMoveMouse(handle uintptr, x, y float64) {
	withPool(func() {
		win := id(handle)
		h := msgRect(send(win, "contentView"), sel("bounds")).Size.Height
		ev := msgMouseEvent(class("NSEvent"), sel("mouseEventWithType:location:modifierFlags:timestamp:windowNumber:context:eventNumber:clickCount:pressure:"),
			5, NSPoint{x, h - y}, 0, 0, sendInt(win, "windowNumber"), 0, 0, 0, 0) // NSEventTypeMouseMoved
		send(id(TestSurfaceView(handle)), "mouseMoved:", uintptr(ev))
	})
}

// TestPresentsWithTransaction reports whether a CAMetalLayer presents with
// the Core Animation transaction.
func TestPresentsWithTransaction(layer uintptr) bool {
	return sendBool(id(layer), "presentsWithTransaction")
}

// TestMetalLayerFrame returns the box of the Metal layer of a window from
// the top left of its content, in points.
func TestMetalLayerFrame(handle uintptr) (x, y, w, h float64) {
	s := theBackend.byNSWindow[id(handle)].surface
	l, super := s.metalLayer(), send(s.view, "layer")
	f, b := msgRect(l, sel("frame")), msgRect(super, sel("bounds"))
	y = f.Origin.Y
	if !sendBool(super, "isGeometryFlipped") {
		y = b.Size.Height - f.Origin.Y - f.Size.Height
	}
	return f.Origin.X, y, f.Size.Width, f.Size.Height
}

// TestAccessChildren returns the accessibility children of obj, and
// TestAccessParent its accessibility parent.
func TestAccessChildren(obj uintptr) []uintptr {
	var kids []uintptr
	withPool(func() {
		for _, k := range arrayItems(send(id(obj), "accessibilityChildren")) {
			kids = append(kids, uintptr(k))
		}
	})
	return kids
}

func TestAccessParent(obj uintptr) uintptr { return uintptr(send(id(obj), "accessibilityParent")) }

// TestAccessHolders returns what reports obj as an accessibility child
// among all that assistive technology finds going down from the window.
func TestAccessHolders(handle, obj uintptr) []uintptr {
	var holders []uintptr
	seen := map[id]bool{}
	var walk func(v id)
	walk = func(v id) {
		if seen[v] || !sendBool(v, "respondsToSelector:", uintptr(sel("accessibilityChildren"))) {
			return
		}
		seen[v] = true
		for _, k := range arrayItems(send(v, "accessibilityChildren")) {
			if uintptr(k) == obj {
				holders = append(holders, uintptr(v))
			}
			walk(k)
		}
	}
	withPool(func() { walk(id(handle)) })
	return holders
}

// TestAddAccessElement puts a new accessibility element in view, which
// holds it, and returns it.
func TestAddAccessElement(view uintptr) uintptr {
	var el id
	withPool(func() {
		el = send(class("NSAccessibilityElement"), "new")
		send(el, "setAccessibilityRole:", uintptr(nsString("AXButton")))
		send(el, "setAccessibilityParent:", view)
		send(id(view), "setAccessibilityChildren:", uintptr(nsArray(append(arrayItems(send(id(view), "accessibilityChildren")), el)...)))
	})
	return uintptr(el)
}

var (
	testIOSurfaceOnce   sync.Once
	testIOSurfaceCreate uintptr
	testIOSurfaceInUse  uintptr
)

// TestNewSurfaceView makes a layer-hosting view that shows a new
// IOSurface, as an image region does, owned by its caller, and returns
// the view and the surface.
func TestNewSurfaceView(width, height int) (view, surface uintptr) {
	testIOSurfaceOnce.Do(func() {
		lib := mustDlopen("/System/Library/Frameworks/IOSurface.framework/IOSurface")
		testIOSurfaceCreate = mustDlsym(lib, "IOSurfaceCreate")
		testIOSurfaceInUse = mustDlsym(lib, "IOSurfaceIsInUse")
	})
	withPool(func() {
		props := newDictionary()
		defer release(props)
		const bgra = 0x42475241 // 'BGRA'
		setObject(props, nsNumberInt(width), nsString("IOSurfaceWidth"))
		setObject(props, nsNumberInt(height), nsString("IOSurfaceHeight"))
		setObject(props, nsNumberInt(4), nsString("IOSurfaceBytesPerElement"))
		setObject(props, nsNumberInt(bgra), nsString("IOSurfacePixelFormat"))
		surface = mainthread.Forward[uintptr](testIOSurfaceCreate, props)
		v := msgInitRect(send(class("NSView"), "alloc"), sel("initWithFrame:"), NSRect{})
		send(v, "setWantsLayer:", 1)
		send(send(v, "layer"), "setContents:", surface)
		view = uintptr(v)
	})
	return view, surface
}

// TestIOSurfaceInUse reports whether the system uses an IOSurface.
func TestIOSurfaceInUse(surface uintptr) bool {
	return mainthread.Forward[bool](testIOSurfaceInUse, surface)
}

// TestClearContents takes the IOSurface away from the layer of a view.
func TestClearContents(view uintptr) { send(send(id(view), "layer"), "setContents:", 0) }
