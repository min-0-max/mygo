package mygo

import (
	"errors"
	"fmt"

	"github.com/egoist/mygo/internal/platform"
)

// RectF is a rectangle in DIPs of a window's content, from its top-left
// corner.
type RectF = platform.RectF

// NativePlacement is where a frame shows a native view element
// (ui.NativeView), which the function given to Window.NewNativeView hears
// of.
type NativePlacement struct {
	// View is the view the element names.
	View uintptr
	// Box is the box of the element as it shows, in the content of the
	// window, and Clip the part of it that shows.
	Box, Clip RectF
	// Radii are those of the corners of the hole the element paints, from
	// the top left clockwise.
	Radii [4]float64
	// Covers are the boxes, within Clip, of what the content paints over
	// the element and takes the pointer.
	Covers []RectF
}

// errNativeView is returned by NewNativeView without a place function.
var errNativeView = errors.New("mygo: a native view needs a function that places its views")

// NewNativeView places an NSView that the application made, the native
// view of the window, under what the window's Content paints, filling the
// content of the window, until the window closes. The window retains it,
// and a window takes one. Inside it, the application makes the views that
// a frame shows with ui.NativeView:
//
//	err := win.NewNativeView(root, func(views []mygo.NativePlacement) {
//		for _, p := range views {
//			setFrame(p.View, p.Box)
//		}
//	})
//
//	// In the view:
//	ui.NativeView(c, holder, webView).Grow(1)
//
// place runs on the main thread in each frame that shows an element, and
// in the frame after the last that did, with every element the frame
// shows, before the frame presents: what it sets in the views shows with
// the frame, in the same Core Animation transaction when the frame moves
// an element, which is when the frame presents with the transaction. A
// view the frame does not show is not in the list, and the application
// hides it; the keyboard leaves it once place returns. views is valid
// until place returns.
//
// Only macOS has native views: elsewhere it returns
// errors.ErrUnsupported.
func (w *Window) NewNativeView(view uintptr, place func(views []NativePlacement)) error {
	if place == nil {
		return errNativeView
	}
	if !w.content {
		return errNoContent
	}
	err := errDestroyed
	onMain(func() {
		if w.native == nil || w.conn == nil {
			return
		}
		s, ok := w.conn.Surface.(platform.NativeViewSurface)
		if !ok {
			err = fmt.Errorf("mygo: native views: %w", errors.ErrUnsupported)
			return
		}
		if err = s.NewNativeView(view); err != nil {
			return
		}
		w.conn.PlaceNativeViews = func(views []platform.NativeViewPlacement) {
			s.PlaceNativeViews(views, func() { place(nativePlacements(views)) })
		}
		w.conn.TabIntoNativeView = s.TabIntoNativeView
		// A frame that waits for its element now places it.
		w.conn.Invalidate()
	})
	return err
}

// nativePlacements returns views as the application takes them.
func nativePlacements(views []platform.NativeViewPlacement) []NativePlacement {
	list := make([]NativePlacement, len(views))
	for i := range views {
		v := &views[i]
		list[i] = NativePlacement{View: v.View, Box: v.Frame, Clip: v.Clip, Radii: v.Radii, Covers: v.Covers}
	}
	return list
}

// HoldFrames keeps the window from drawing until ReleaseFrames, the frames
// that the system asks for, as during a live resize or a zoom, among them.
// While it holds them the window keeps its last frame at the size it was
// drawn, at the top left of the content, rather than scale it to the
// window. A frame requested or asked for meanwhile draws, as one, after
// the release; a notice waiting for a frame (ui's AfterPresent) hears that
// the window holds its frames. It does nothing to a window that shows no
// Content.
func (w *Window) HoldFrames() { w.holdFrames(true) }

// ReleaseFrames ends what HoldFrames began.
func (w *Window) ReleaseFrames() { w.holdFrames(false) }

func (w *Window) holdFrames(hold bool) {
	onMain(func() {
		if w.native != nil && w.conn != nil && w.conn.HoldFrames != nil {
			w.conn.HoldFrames(hold)
		}
	})
}
