//go:build darwin

package darwin

import (
	"errors"
	"log"
	"slices"
	"sync"

	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/internal/platform"
)

// The native view of a window (platform.NativeViewSurface) is a view of
// the application in the window's content view, under the surface and the
// web views, above the material, filling the content: frames are
// transparent where the content shows a view inside it through a native
// view element. The application owns what is in it, so that nothing here
// overrides those views' methods: a press is seen by an event monitor,
// which is armed once a window has a native view and sees only presses,
// the cursor is left alone, and the elements of assistive technology adopt
// what is in the views by the setters of NSAccessibility.

// nativeHost is what a window does for its native view: what every app
// reaches goes through it, so that only NewNativeView, in the apps that
// call it, links nativeViews.
type nativeHost interface {
	// takes reports whether the native view takes the pointer at x, y in
	// the surface, p in the view around the surface.
	takes(x, y float64, p NSPoint) bool
	// moved follows the pointer of the mouse event ev.
	moved(ev id)
	// access returns what assistive technology finds in the element of
	// the node n.
	access(n *platform.AccessNode) []id
	// adopt has the element el the accessibility parent of kids, and of
	// none that it was of.
	adopt(el *accessElement, kids []id)
	// tabOut gives the content the keyboard back as Tab leaves view, and
	// reports whether view is in a key view.
	tabOut(view id, back bool) bool
	// close takes the native view out of a window that closes.
	close()
}

// nativeViews is the state of the native view elements of a surface.
type nativeViews struct {
	s    *surface
	view id
	// placed are the elements of the last frame.
	placed []platform.NativeViewPlacement
	// over tells that the pointer was last where the view takes it.
	over bool
	// logged are the views misnamed by elements, each logged once.
	logged map[uintptr]bool
}

// NewNativeView places view under the surface and the web views.
func (s *surface) NewNativeView(view uintptr) error {
	host := s.w
	if host.closed {
		return errDestroyed
	}
	if s.native != nil {
		return errors.New("mygo: the window has a native view")
	}
	v := id(view)
	if v == 0 || !sendBool(v, "isKindOfClass:", uintptr(class("NSView"))) {
		return errors.New("mygo: a native view is an NSView")
	}
	if send(v, "superview") != 0 {
		return errors.New("mygo: the native view is in a view")
	}
	s.native = &nativeViews{s: s, view: retain(v), logged: map[uintptr]bool{}}
	withPool(func() {
		msgSetRect(v, sel("setFrame:"), msgRect(host.view, sel("bounds")))
		send(v, "setAutoresizingMask:", nsViewWidthHeightSizable)
		// Composited with the surface, as the clip views of web views are.
		send(v, "setWantsLayer:", 1)
		// What is in it is found in the elements, not here.
		send(v, "setAccessibilityChildren:", uintptr(nsArray()))
		if host.effect != 0 {
			send(host.view, "addSubview:positioned:relativeTo:", uintptr(v), nsWindowAbove, uintptr(host.effect))
		} else {
			send(host.view, "addSubview:positioned:relativeTo:", uintptr(v), nsWindowBelow, 0)
		}
	})
	s.w.b.armPressMonitor()
	return nil
}

// nsWindowAbove is NSWindowAbove, for addSubview:positioned:relativeTo:.
const nsWindowAbove = 1

func (n *nativeViews) close() {
	if n.view == 0 {
		return
	}
	send(n.view, "removeFromSuperview")
	release(n.view)
	n.view, n.placed = 0, nil
}

// PlaceNativeViews notes where the frame shows its elements, calls apply,
// which has the application set the frames of their views, then takes the
// keyboard from the views of the elements the frame no longer shows. A
// view that left the keyboard as the application hid it leaves it where
// AppKit moved it.
func (s *surface) PlaceNativeViews(views []platform.NativeViewPlacement, apply func()) {
	n, _ := s.native.(*nativeViews)
	if n == nil || s.w.closed {
		return
	}
	last := n.placed
	n.placed = slices.DeleteFunc(slices.Clone(views), func(p platform.NativeViewPlacement) bool {
		return !s.named(last, p)
	})
	apply()
	for i := range last {
		if !slices.ContainsFunc(n.placed, func(p platform.NativeViewPlacement) bool { return p.View == last[i].View }) &&
			s.keyboardIn(id(last[i].View)) {
			send(s.w.win, "makeFirstResponder:", uintptr(s.view))
		}
	}
}

// named reports whether the element p names a view inside the native view
// and a key view inside that, once per view: last holds those known to.
func (s *surface) named(last []platform.NativeViewPlacement, p platform.NativeViewPlacement) bool {
	if slices.ContainsFunc(last, func(o platform.NativeViewPlacement) bool { return o.View == p.View && o.KeyView == p.KeyView }) {
		return true
	}
	n := s.native.(*nativeViews)
	view, key := id(p.View), id(p.KeyView)
	if view != 0 && key != 0 && view != n.view && sendBool(view, "isDescendantOf:", uintptr(n.view)) && sendBool(key, "isDescendantOf:", uintptr(view)) {
		return true
	}
	if !n.logged[p.View] {
		n.logged[p.View] = true
		log.Printf("mygo: a native view element names %#x and key view %#x, not a view inside the window's native view and a view inside that", p.View, p.KeyView)
	}
	return false
}

// keyboardIn reports whether the first responder is view or a view inside
// it.
func (s *surface) keyboardIn(view id) bool {
	r := send(s.w.win, "firstResponder")
	return r != 0 && sendBool(r, "isKindOfClass:", uintptr(class("NSView"))) && sendBool(r, "isDescendantOf:", uintptr(view))
}

// TabIntoNativeView gives key the keyboard as the window's key view loop
// does: key follows the surface in the loop, or precedes it.
func (s *surface) TabIntoNativeView(key uintptr, back bool) {
	if s.w.closed || s.native == nil {
		return
	}
	withPool(func() {
		if back {
			send(id(key), "setNextKeyView:", uintptr(s.view))
			send(s.w.win, "selectKeyViewPrecedingView:", uintptr(s.view))
		} else {
			send(s.view, "setNextKeyView:", key)
			send(s.w.win, "selectKeyViewFollowingView:", uintptr(s.view))
		}
	})
}

// tabOut gives the content the keyboard back as Tab leaves the key view of
// an element that holds view, and reports whether one does. WebKit moves
// the keyboard out of a page past its ends through the window's key view
// loop, as other views do.
func (n *nativeViews) tabOut(view id, back bool) bool {
	s := n.s
	if view == 0 || view == s.view {
		return false
	}
	for i := range n.placed {
		p := &n.placed[i]
		if !sendBool(view, "isDescendantOf:", p.KeyView) {
			continue
		}
		send(s.w.win, "makeFirstResponder:", uintptr(s.view))
		ev := platform.SurfaceEvent{Kind: platform.WebViewTabOut, Key: platform.KeyTab, View: p.View}
		if back {
			ev.Mods = platform.ModShift
		}
		s.send(ev)
		return true
	}
	return false
}

// takes finds that an element shows at x, y, nothing covers it, and the
// hit test of the native view finds a view inside it.
func (n *nativeViews) takes(x, y float64, p NSPoint) bool {
	for i := range n.placed {
		if n.placed[i].At(x, y) {
			hit := msgHitTest(n.view, sel("hitTest:"), p)
			return hit != 0 && hit != n.view
		}
	}
	return false
}

// msgHitTest sends hitTest:, which takes an NSPoint, on the main thread
// (mainthread.Forward).
func msgHitTest(obj id, sel objc.SEL, p NSPoint) id {
	return mainthread.Forward[id](msgSendAddr, obj, sel, p)
}

// under reports whether the native view takes the pointer of the mouse
// event ev.
func (n *nativeViews) under(ev id) bool {
	s := n.s
	x, y := s.location(ev)
	return n.takes(x, y, msgPointFromView(send(s.view, "superview"), sel("convertPoint:fromView:"), msgPoint(ev, sel("locationInWindow")), 0))
}

// moved sets the content's cursor again as the pointer leaves what the
// native view takes: the view set its own meanwhile, and the content,
// whose cursor may be the same, tells nothing.
func (n *nativeViews) moved(ev id) {
	over := n.under(ev)
	if over != n.over {
		n.over = over
		if !over {
			n.s.applyCursor()
		}
	}
}

var pressMonitor sync.Once

// armPressMonitor has the presses that the application dispatches seen,
// for those that go to native views: nothing of Go runs for other events.
func (b *Backend) armPressMonitor() {
	pressMonitor.Do(func() {
		const mask = 1<<1 | 1<<3 | 1<<25 // left, right and other mouse down
		block := newBlock(func(_ objc.Block, ev id) id {
			if w := b.byNSWindow[send(ev, "window")]; w != nil && !w.closed && w.surface != nil {
				if n, ok := w.surface.native.(*nativeViews); ok {
					n.pressed(ev)
				}
			}
			return ev
		})
		// The monitor lasts as long as the application, and the block with it.
		retain(send(class("NSEvent"), "addLocalMonitorForEventsMatchingMask:handler:", mask, uintptr(block)))
	})
}

// pressed tells the content of a press that goes to a view of the native
// view: it takes no event of the press itself.
func (n *nativeViews) pressed(ev id) {
	if !n.under(ev) {
		return
	}
	s := n.s
	button := 0
	switch typ := sendInt(ev, "type"); typ {
	case 3: // NSEventTypeRightMouseDown
		button = 1
	case 25: // NSEventTypeOtherMouseDown
		button = 2
	}
	x, y := s.location(ev)
	s.send(platform.SurfaceEvent{Kind: platform.WebViewPress, X: x, Y: y, Button: button, Mods: eventMods(ev)})
}

// access returns, in a pool, the nearest unignored descendants of the view
// of node, and its key view; nothing for a node of no element the last
// frame shows.
func (n *nativeViews) access(node *platform.AccessNode) []id {
	if node.View == 0 || !slices.ContainsFunc(n.placed, func(p platform.NativeViewPlacement) bool { return p.View == node.View }) {
		return nil
	}
	view, key := id(node.View), id(node.KeyView)
	if view == key {
		return []id{key}
	}
	kids := arrayItems(mainthread.Forward[id](unignoredChildren, send(view, "accessibilityChildren")))
	if !slices.Contains(kids, key) {
		kids = append(kids, key)
	}
	return kids
}

// adopt holds kids, which the application may free once an element no
// longer shows them.
func (n *nativeViews) adopt(el *accessElement, kids []id) {
	for _, k := range el.adopted {
		if !slices.Contains(kids, k) {
			send(k, "setAccessibilityParent:", 0)
		}
		release(k)
	}
	el.adopted = el.adopted[:0]
	for _, k := range kids {
		el.adopted = append(el.adopted, retain(k))
		send(k, "setAccessibilityParent:", uintptr(el.obj))
	}
}
