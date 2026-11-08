# Native view

`ui.NativeView` shows a view the application made in a window of native
UI: a terminal, a document view, an image region backed by an IOSurface,
a web view of another engine or one the application configures itself.
The application owns its views and keeps them in a view of its own, which
`Window.NewNativeView` places under what the window paints. Only macOS has
native views: elsewhere `NewNativeView` returns `errors.ErrUnsupported`.

```go
type workbench struct {
	win            *mygo.Window
	holder, editor uintptr
	menu           bool
}

func (app *workbench) view(c *ui.Context) {
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		sidebar(c)
		e := ui.NativeView(c, app.holder, app.editor).Grow(1)
		// Over the view: it shows, and it takes the pointer.
		ui.Popover(c, e, &app.menu, func() { ui.Text(c, "Over the view") })
	})
}

func main() {
	app := &workbench{}
	mygo.App.WhenReady(func() {
		app.win = mygo.NewWindow(mygo.WindowOptions{Title: "Workbench", Content: ui.View(app.view)})
		root := newRootView()
		app.holder, app.editor = newEditor(root)
		err := app.win.NewNativeView(root, func(views []mygo.NativePlacement) {
			for _, p := range views {
				setFrame(p.View, p.Box)
			}
		})
		if err != nil {
			log.Fatal(err)
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
```

## The native view of the window

`Window.NewNativeView` takes an `NSView` and a function that places the
views a frame shows. The view goes under the window's Metal surface,
fills the content of the window and resizes with it, and stays until the
window closes: the window retains it, and a window takes one. The
application adds its views to it and makes them in any way it likes.
Native views need a window showing `Content`.

`ui.NativeView` names a view inside the native view, and the view of it
that takes the keyboard, which is that view or one inside it; 0 is the
view itself. An element naming a view outside the native view, or a key
view outside the view it names, is not placed, and logs that once.

## Placing views

A native view element has no size of its own: as a [web view](web-view.md)
does, it stretches across its container and takes the room along it with
`Grow`, or a size. It paints a hole through the window's content over its
box, so what paints after it shows over the view, with its `Radius` cut
out of what the window paints.

In each frame that shows an element, and the one frame after the last that
did, the function runs on the main thread before the frame presents. It
hears of every element the frame shows, in `NativePlacement`: the view,
its box in the content of the window, the clip of the box, the radii of the
hole and the covers, the boxes of what paints over it and takes the
pointer. A frame that moves an element or changes its clip or covers
presents with the Core Animation transaction, as a frame that resizes the
window does, so that the view and what paints around it show the same
frame: nothing tears while a split divider drags. Other frames present
alone, as a window without views does.

MyGo sets nothing in the view: its frame, its clip and whether it shows
are the application's. A view that no frame shows is not in the list,
which is when the application hides it. An IOSurface that a view shows
stays with its layer's `contents`, which only the application sets.

## Holding frames

`Window.HoldFrames` stops the window drawing until
`Window.ReleaseFrames`, the frames AppKit asks for during a live resize
or a zoom among them. Meanwhile the Metal layer keeps the last frame at
its drawn size at the top left of the content, instead of scaling it to
the new bounds. What was asked for meanwhile draws as one frame after the
release. Holds do not nest: the first `ReleaseFrames` ends them.

## An open transaction

An application that opens a Core Animation transaction on the main thread
(`[CATransaction begin]`) and keeps it open across passes of the run loop,
as while it lays out native views, tells the window with
`Window.TransactionOpened`, and with `Window.TransactionCommitted` on the
main thread right after the commit. A frame that presents with the
transaction meanwhile keeps its drawable until the commit, so a frame that
would find no other drawable does not wait for one on the main thread,
which would keep the commit from coming until the system gives up after a
second: it is built at once and draws, once, after the commit. Without an
open transaction, frames take their drawables as before.

## Pointer and keyboard

The view takes the pointer where its element shows, nothing painted over
it takes the pointer, and the hit test of the native view finds a view
other than the native view itself. Anywhere else the pointer stays with
the content, so a transparent view that returns nothing from its hit test
leaves the element's own handlers working. Where the view takes the
pointer, the content leaves the cursor to it, and sets its own again as
the pointer leaves.

A press the view takes comes to the content as a press on a web view
does: it closes the popovers it is outside of (`PressedOutside`) and
moves the focus to the element. No click reaches the element.

The element is a Tab stop. Tab gives its key view the keyboard, and Tab
past the view goes on from the element. When a frame does not show an
element and, once the function placed the frame, the keyboard is still
inside its view, the keyboard goes to the content. If the application hid
the view and AppKit moved the keyboard, it stays where AppKit put it.

## Accessibility

Assistive technology finds what is in a view only in its element. The
element reports the nearest unignored accessibility descendants of the
view as its children, the key view among them, each reports the element
as its parent, and the native view reports no children. The element keeps
them whatever role it has, a button's too.
