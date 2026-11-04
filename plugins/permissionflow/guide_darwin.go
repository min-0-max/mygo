//go:build darwin

package permissionflow

import (
	"context"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/internal/darwin/mainthread"
)

// Guide's panel is an NSPanel that never takes the focus, so that System
// Settings stays the active app while the user drags. A goroutine follows
// System Settings through the window server (CGWindowListCopyWindowInfo),
// which needs no permission, 30 times a second, and moves the panel on the
// main thread when the window has moved, or hides it while another app is
// in front. The card of the app is an NSView of a class of ours, the
// source of the drag, whose item is a writer of ours putting the app on
// the pasteboard as Finder does.
//
// Go runs on the worker (internal/darwin/mainthread). What mygo.RunOnMain
// runs and the methods of these classes are its steps, whose messages run
// on the main thread one by one, and the objects that those return live
// until the step ends. What follows System Settings runs on a goroutine,
// so it asks in one step (mainthread.RunOnMain).

type nsPoint struct{ X, Y float64 }
type nsSize struct{ Width, Height float64 }
type nsRect struct {
	Origin nsPoint
	Size   nsSize
}

func (r rect) ns() nsRect { return nsRect{nsPoint{r.X, r.Y}, nsSize{r.W, r.H}} }

func fromNS(r nsRect) rect { return rect{r.Origin.X, r.Origin.Y, r.Size.Width, r.Size.Height} }

const (
	nsWindowStyleMaskBorderless         = 0
	nsWindowStyleMaskNonactivatingPanel = 1 << 7
	nsBackingStoreBuffered              = 2
	nsFloatingWindowLevel               = 3
	nsCollectionCanJoinAllSpaces        = 1 << 0
	nsCollectionFullScreenAuxiliary     = 1 << 8
	nsVisualEffectMaterialPopover       = 6
	nsVisualEffectBlendingBehindWindow  = 0
	nsVisualEffectStateActive           = 1
	nsBoxCustom                         = 4
	nsNoTitle                           = 0
	nsDragOperationCopy                 = 1
	nsDraggingFormationNone             = 1
	nsImageOnly                         = 1
	nsImageResizingModeStretch          = 1
	nsTextAlignmentCenter               = 2
	kCGWindowListOptionOnScreenOnly     = 1 << 0
	kCGWindowListExcludeDesktopElements = 1 << 4
	kCGEventSourceStateCombined         = 0
	kCGMouseButtonLeft                  = 0

	// The panel's layout, in points.
	panelPadding = 12
	panelRadius  = 16
	cardHeight   = 48
	iconSize     = 32
	closeSize    = 18
	dragIconSize = 56

	// trackInterval is how often the panel follows System Settings.
	trackInterval = time.Second / 30
	// settingsMisses is how many times in a row System Settings must be
	// missing for the panel to close: it is missing for a moment while it
	// opens or changes pages.
	settingsMisses = 12
	// settingsWait is how long System Settings may take to open.
	settingsWait = 10 * time.Second
)

var (
	classesOnce sync.Once
	classesErr  error

	// active is the flow whose panel shows, which the panel's classes
	// reach; one shows at a time. Main thread only, like the panel.
	active *flow
	// dragging is set while the app is dragged: the panel lets the mouse
	// through, and the tracker restores it once the button is up.
	dragging atomic.Bool

	flowMu   sync.Mutex
	lastFlow *flow // the flow running, which a new one stops
)

// A flow is one call of Guide.
type flow struct {
	p          Permission
	app, name  string
	draggable  bool
	stop       chan struct{} // closed when the user closes it, or another flow starts
	stopOnce   sync.Once
	panel      objc.ID
	views      panelViews
	labelH     float64
	shown      bool
	dragOrigin nsPoint
	dragBegan  bool
}

// panelViews are the views of the panel, which its content view keeps.
type panelViews struct {
	border, label, close, cardBox, icon, name, hint, hand, card objc.ID
}

func (f *flow) close() { f.stopOnce.Do(func() { close(f.stop) }) }

func registerClasses() error {
	classesOnce.Do(func() {
		source := objc.GetProtocol("NSDraggingSource")
		writing := objc.GetProtocol("NSPasteboardWriting")
		var protocols []*objc.Protocol
		if source != nil {
			protocols = append(protocols, source)
		}
		_, classesErr = mainthread.RegisterClass("MyGoPermissionFlowDragView", objc.GetClass("NSView"), protocols, nil, []objc.MethodDef{
			{Cmd: sel("mouseDown:"), Fn: cardMouseDown},
			{Cmd: sel("mouseDragged:"), Fn: cardMouseDragged},
			{Cmd: sel("mouseUp:"), Fn: cardMouseUp},
			{Cmd: sel("acceptsFirstMouse:"), Fn: func(objc.ID, objc.SEL, objc.ID) bool { return true }},
			{Cmd: sel("mouseDownCanMoveWindow"), Fn: func(objc.ID, objc.SEL) bool { return false }},
			{Cmd: sel("resetCursorRects"), Fn: cardResetCursorRects},
			{Cmd: sel("draggingSession:sourceOperationMaskForDraggingContext:"), Fn: func(objc.ID, objc.SEL, objc.ID, int) uint { return nsDragOperationCopy }},
			{Cmd: sel("ignoreModifierKeysForDraggingSession:"), Fn: func(objc.ID, objc.SEL, objc.ID) bool { return true }},
		})
		if classesErr != nil {
			return
		}
		protocols = nil
		if writing != nil {
			protocols = append(protocols, writing)
		}
		_, classesErr = mainthread.RegisterClass("MyGoPermissionFlowAppWriter", objc.GetClass("NSObject"), protocols, nil, []objc.MethodDef{
			{Cmd: sel("writableTypesForPasteboard:"), Fn: writerTypes},
			{Cmd: sel("pasteboardPropertyListForType:"), Fn: writerPropertyList},
		})
		if classesErr != nil {
			return
		}
		_, classesErr = mainthread.RegisterClass("MyGoPermissionFlowTarget", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
			{Cmd: sel("close:"), Fn: func(objc.ID, objc.SEL, objc.ID) {
				if active != nil {
					active.close()
				}
			}},
		})
	})
	return classesErr
}

// The pasteboard types System Settings takes an app dragged from Finder
// with.
var appTypes = []string{
	"public.file-url",
	"public.url",
	"NSFilenamesPboardType",
	"com.apple.pasteboard.promised-file-url",
	"public.utf8-plain-text",
}

func writerTypes(_ objc.ID, _ objc.SEL, _ objc.ID) objc.ID {
	arr := send(class("NSMutableArray"), "array")
	for _, t := range appTypes {
		send(arr, "addObject:", nsString(t))
	}
	return arr
}

func writerPropertyList(_ objc.ID, _ objc.SEL, typ objc.ID) objc.ID {
	if active == nil {
		return 0
	}
	path := active.app
	switch goString(typ) {
	case "public.file-url", "public.url", "com.apple.pasteboard.promised-file-url":
		u := send(class("NSURL"), "fileURLWithPath:", nsString(path))
		return send(u, "absoluteString")
	case "NSFilenamesPboardType":
		return send(class("NSArray"), "arrayWithObject:", nsString(path))
	case "public.utf8-plain-text":
		return nsString(path)
	}
	return 0
}

func cardResetCursorRects(self objc.ID, _ objc.SEL) {
	bounds := sendRect(self, "bounds")
	send(self, "addCursorRect:cursor:", bounds, send(class("NSCursor"), "openHandCursor"))
}

func cardMouseDown(self objc.ID, _ objc.SEL, event objc.ID) {
	if active == nil {
		return
	}
	active.dragOrigin = mainthread.Send[nsPoint](event, sel("locationInWindow"))
	active.dragBegan = false
}

func cardMouseUp(objc.ID, objc.SEL, objc.ID) {
	if active != nil {
		active.dragBegan = false
	}
}

func cardMouseDragged(self objc.ID, _ objc.SEL, event objc.ID) {
	f := active
	if f == nil || f.dragBegan {
		return
	}
	at := mainthread.Send[nsPoint](event, sel("locationInWindow"))
	if math.Hypot(at.X-f.dragOrigin.X, at.Y-f.dragOrigin.Y) <= 4 {
		return
	}
	f.dragBegan = true
	writer := send(send(class("MyGoPermissionFlowAppWriter"), "alloc"), "init")
	item := send(send(class("NSDraggingItem"), "alloc"), "initWithPasteboardWriter:", writer)
	icon := send(send(class("NSWorkspace"), "sharedWorkspace"), "iconForFile:", nsString(f.app))
	send(icon, "setSize:", nsSize{dragIconSize, dragIconSize})
	p := mainthread.Send[nsPoint](self, sel("convertPoint:fromView:"), at, objc.ID(0))
	send(item, "setDraggingFrame:contents:", nsRect{nsPoint{p.X - dragIconSize/2, p.Y - dragIconSize/2}, nsSize{dragIconSize, dragIconSize}}, icon)
	session := send(self, "beginDraggingSessionWithItems:event:source:", send(class("NSArray"), "arrayWithObject:", item), event, self)
	send(session, "setAnimatesToStartingPositionsOnCancelOrFail:", true)
	send(session, "setDraggingFormation:", nsDraggingFormationNone)
	release(item)
	release(writer)
	// System Settings, under the pointer, takes the drop.
	dragging.Store(true)
	send(f.panel, "setIgnoresMouseEvents:", true)
	send(f.panel, "setAlphaValue:", 0.72)
}

// endDrag restores the panel after a drag. Main thread.
func (f *flow) endDrag() {
	dragging.Store(false)
	f.dragBegan = false
	if f.panel != 0 {
		send(f.panel, "setIgnoresMouseEvents:", false)
		send(f.panel, "setAlphaValue:", 1.0)
	}
}

func defaultApp() string {
	var path string
	mainthread.RunOnMain(func() {
		withPool(func() {
			path = goString(send(send(class("NSBundle"), "mainBundle"), "bundlePath"))
		})
	})
	if strings.HasSuffix(path, ".app") {
		return path
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return path
}

func guide(ctx context.Context, p Permission, opts GuideOptions) (Status, error) {
	if err := loadCore(); err != nil {
		return Unknown, err
	}
	if err := registerClasses(); err != nil {
		return Unknown, err
	}
	f := &flow{p: p, app: opts.App, name: opts.Name, draggable: macDraggable(p), stop: make(chan struct{})}
	if f.app == "" {
		f.app = defaultApp()
	}
	if f.name == "" {
		f.name = appName(f.app)
	}
	flowMu.Lock()
	if lastFlow != nil {
		lastFlow.close()
	}
	lastFlow = f
	flowMu.Unlock()
	defer func() {
		flowMu.Lock()
		if lastFlow == f {
			lastFlow = nil
		}
		flowMu.Unlock()
		mygo.RunOnMain(f.destroy)
	}()

	if err := openSettings(p); err != nil {
		return Unknown, err
	}
	var (
		t       = time.NewTicker(trackInterval)
		started = time.Now()
		seen    bool // System Settings was there
		misses  int  // times in a row it wasn't
		last    rect // where its window was
		front   bool // whether it was in front
		ticks   int
	)
	defer t.Stop()
	for {
		if ticks%int(pollInterval/trackInterval) == 0 {
			if st, err := check(p); err != nil || st == Granted {
				return st, err
			}
		}
		ticks++

		pid, frontmost := settingsProcess()
		switch {
		case pid != 0:
			seen, misses = true, 0
		case seen:
			if misses++; misses >= settingsMisses {
				return check(p)
			}
		case time.Since(started) > settingsWait:
			return check(p)
		}
		if dragging.Load() && !cgEventSourceButtonState(kCGEventSourceStateCombined, kCGMouseButtonLeft) {
			mygo.RunOnMain(f.endDrag)
		}
		if f.draggable && pid != 0 {
			// While a drag is on, System Settings is in front even when the
			// window server doesn't say so yet.
			show := frontmost || dragging.Load()
			if frame, ok := settingsFrame(pid); ok && (frame != last || show != front) {
				last, front = frame, show
				mygo.RunOnMain(func() { f.place(frame, show) })
			}
		}

		select {
		case <-ctx.Done():
			st, _ := check(p)
			return st, ctx.Err()
		case <-f.stop:
			return check(p)
		case <-t.C:
		}
	}
}

// settingsProcess returns the process of System Settings, 0 when it isn't
// running, and whether it is the app in front.
func settingsProcess() (pid int32, frontmost bool) {
	mainthread.RunOnMain(func() {
		withPool(func() {
			apps := send(class("NSRunningApplication"), "runningApplicationsWithBundleIdentifier:", nsString(settingsBundleID))
			if mainthread.Send[uint](apps, sel("count")) == 0 {
				return
			}
			app := send(apps, "firstObject")
			pid = int32(mainthread.Send[int](app, sel("processIdentifier")))
			front := send(send(class("NSWorkspace"), "sharedWorkspace"), "frontmostApplication")
			frontmost = front != 0 && int32(mainthread.Send[int](front, sel("processIdentifier"))) == pid
		})
	})
	return pid, frontmost
}

// settingsFrame returns the bounds of System Settings' window, in the
// window server's coordinates.
func settingsFrame(pid int32) (r rect, ok bool) {
	mainthread.RunOnMain(func() {
		withPool(func() {
			list := cgWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly|kCGWindowListExcludeDesktopElements, 0)
			if list == 0 {
				return
			}
			defer mainthread.ByReceiver(cfRelease, uintptr(list))
			var (
				kPID    = nsString("kCGWindowOwnerPID")
				kLayer  = nsString("kCGWindowLayer")
				kAlpha  = nsString("kCGWindowAlpha")
				kBounds = nsString("kCGWindowBounds")
				windows []settingsWindow
			)
			n := mainthread.Send[uint](list, sel("count"))
			for i := range n {
				w := send(list, "objectAtIndex:", i)
				if v := send(w, "objectForKey:", kPID); v == 0 || int32(mainthread.Send[int](v, sel("integerValue"))) != pid {
					continue
				}
				sw := settingsWindow{alpha: 1}
				if v := send(w, "objectForKey:", kLayer); v != 0 {
					sw.layer = mainthread.Send[int](v, sel("integerValue"))
				}
				if v := send(w, "objectForKey:", kAlpha); v != 0 {
					sw.alpha = mainthread.Send[float64](v, sel("doubleValue"))
				}
				b := send(w, "objectForKey:", kBounds)
				if b == 0 {
					continue
				}
				num := func(k string) float64 {
					v := send(b, "objectForKey:", nsString(k))
					if v == 0 {
						return 0
					}
					return mainthread.Send[float64](v, sel("doubleValue"))
				}
				sw.bounds = rect{num("X"), num("Y"), num("Width"), num("Height")}
				windows = append(windows, sw)
			}
			r, ok = mainSettingsWindow(windows)
		})
	})
	return r, ok
}

// place shows the panel under the window of System Settings at frame (in
// the window server's coordinates), or hides it. Main thread.
func (f *flow) place(frame rect, show bool) {
	if !show {
		if f.panel != 0 && f.shown {
			send(f.panel, "orderOut:", objc.ID(0))
			f.shown = false
		}
		return
	}
	withPool(func() {
		screens := send(class("NSScreen"), "screens")
		n := mainthread.Send[uint](screens, sel("count"))
		if n == 0 {
			return
		}
		mainHeight := sendRect(send(screens, "objectAtIndex:", uint(0)), "frame").Size.Height
		settings := flipRect(frame, mainHeight)
		var visible []rect
		for i := range n {
			visible = append(visible, fromNS(sendRect(send(screens, "objectAtIndex:", i), "visibleFrame")))
		}
		screen := screenOf(settings, visible)
		w := panelWidth(settings, screen)
		if f.panel == 0 {
			f.build(w)
		}
		f.layout(w)
		h := panelPadding + f.labelH + 8 + cardHeight + panelPadding
		target := panelFrame(settings, screen, h)
		send(f.panel, "setFrame:display:", target.ns(), true)
		send(f.panel, "invalidateShadow")
		if !f.shown {
			send(f.panel, "orderFrontRegardless")
			f.shown = true
		}
		active = f
	})
}

func (f *flow) build(w float64) {
	panel := send(send(class("NSPanel"), "alloc"), "initWithContentRect:styleMask:backing:defer:", nsRect{Size: nsSize{w, 100}}, uint(nsWindowStyleMaskBorderless|nsWindowStyleMaskNonactivatingPanel), uint(nsBackingStoreBuffered), false)
	send(panel, "setLevel:", nsFloatingWindowLevel)
	send(panel, "setReleasedWhenClosed:", false)
	send(panel, "setOpaque:", false)
	send(panel, "setBackgroundColor:", send(class("NSColor"), "clearColor"))
	send(panel, "setHasShadow:", true)
	send(panel, "setHidesOnDeactivate:", false)
	send(panel, "setCollectionBehavior:", uint(nsCollectionCanJoinAllSpaces|nsCollectionFullScreenAuxiliary))
	send(panel, "setMovableByWindowBackground:", false)

	effect := send(send(class("NSVisualEffectView"), "alloc"), "initWithFrame:", nsRect{})
	send(effect, "setMaterial:", nsVisualEffectMaterialPopover)
	send(effect, "setBlendingMode:", nsVisualEffectBlendingBehindWindow)
	send(effect, "setState:", nsVisualEffectStateActive)
	// A layer's corner radius doesn't clip what the view blurs from behind
	// the window: a mask does, and the window's shadow follows it.
	mask := roundedMask(panelRadius)
	send(effect, "setMaskImage:", mask)
	release(mask)
	send(panel, "setContentView:", effect)
	release(effect)

	color := func(name string) objc.ID { return send(class("NSColor"), name) }
	v := &f.views
	v.border = newBox(color("clearColor"), panelRadius)
	send(effect, "addSubview:", v.border)
	release(v.border)

	label := send(class("NSTextField"), "wrappingLabelWithString:", nsString(""))
	text := send(send(class("NSMutableAttributedString"), "alloc"), "init")
	for _, r := range panelText(f.name, f.p.Title()) {
		font := send(class("NSFont"), "systemFontOfSize:", 13.0)
		if r.bold {
			font = send(class("NSFont"), "boldSystemFontOfSize:", 13.0)
		}
		attrs := send(class("NSMutableDictionary"), "dictionary")
		send(attrs, "setObject:forKey:", font, nsString("NSFont"))
		send(attrs, "setObject:forKey:", color("labelColor"), nsString("NSColor"))
		part := send(send(class("NSAttributedString"), "alloc"), "initWithString:attributes:", nsString(r.text), attrs)
		send(text, "appendAttributedString:", part)
		release(part)
	}
	send(label, "setAttributedStringValue:", text)
	release(text)
	send(effect, "addSubview:", label)
	v.label = label

	closeButton := send(send(class("NSButton"), "alloc"), "initWithFrame:", nsRect{})
	send(closeButton, "setBordered:", false)
	send(closeButton, "setImagePosition:", nsImageOnly)
	if img := symbol("xmark.circle.fill", "Close"); img != 0 {
		send(closeButton, "setImage:", img)
		send(closeButton, "setContentTintColor:", color("secondaryLabelColor"))
	} else {
		send(closeButton, "setTitle:", nsString("✕"))
	}
	target := send(send(class("MyGoPermissionFlowTarget"), "alloc"), "init") // kept, as small as it is
	send(closeButton, "setTarget:", target)
	send(closeButton, "setAction:", sel("close:"))
	send(effect, "addSubview:", closeButton)
	release(closeButton)
	v.close = closeButton

	// The card: a box, the app's icon and name, a hint, and over them the
	// view the drag starts from, which gets every click on the card.
	cardBox := newBox(send(color("controlBackgroundColor"), "colorWithAlphaComponent:", 0.65), 10)
	send(effect, "addSubview:", cardBox)
	release(cardBox)
	v.cardBox = cardBox

	icon := send(class("NSImageView"), "imageViewWithImage:", send(send(class("NSWorkspace"), "sharedWorkspace"), "iconForFile:", nsString(f.app)))
	send(effect, "addSubview:", icon)
	v.icon = icon

	name := send(class("NSTextField"), "labelWithString:", nsString(f.name))
	send(name, "setFont:", send(class("NSFont"), "systemFontOfSize:", 15.0))
	send(effect, "addSubview:", name)
	v.name = name

	hint := send(class("NSTextField"), "labelWithString:", nsString("Drag"))
	send(hint, "setFont:", send(class("NSFont"), "systemFontOfSize:", 9.0))
	send(hint, "setTextColor:", color("secondaryLabelColor"))
	send(hint, "setAlignment:", nsTextAlignmentCenter)
	send(effect, "addSubview:", hint)
	v.hint = hint

	if img := symbol("hand.draw", "Drag"); img != 0 {
		hand := send(class("NSImageView"), "imageViewWithImage:", img)
		send(hand, "setContentTintColor:", color("secondaryLabelColor"))
		send(effect, "addSubview:", hand)
		v.hand = hand
	}

	card := send(send(class("MyGoPermissionFlowDragView"), "alloc"), "initWithFrame:", nsRect{})
	send(effect, "addSubview:", card)
	release(card)
	v.card = card
	f.panel = panel
}

// roundedMask returns an image of a rounded rectangle that stretches to
// any size, keeping its corners, for NSVisualEffectView's maskImage.
func roundedMask(radius float64) objc.ID {
	side := 2*radius + 1
	img := send(send(class("NSImage"), "alloc"), "initWithSize:", nsSize{side, side})
	send(img, "lockFocus")
	send(send(class("NSColor"), "blackColor"), "set")
	send(send(class("NSBezierPath"), "bezierPathWithRoundedRect:xRadius:yRadius:", nsRect{Size: nsSize{side, side}}, radius, radius), "fill")
	send(img, "unlockFocus")
	type nsEdgeInsets struct{ Top, Left, Bottom, Right float64 }
	send(img, "setCapInsets:", nsEdgeInsets{radius, radius, radius, radius})
	send(img, "setResizingMode:", nsImageResizingModeStretch)
	return img
}

// newBox returns a rounded box with the separator's border, filled.
func newBox(fill objc.ID, radius float64) objc.ID {
	box := send(send(class("NSBox"), "alloc"), "initWithFrame:", nsRect{})
	send(box, "setBoxType:", nsBoxCustom)
	send(box, "setTitlePosition:", nsNoTitle)
	send(box, "setBorderWidth:", 1.0)
	send(box, "setCornerRadius:", radius)
	send(box, "setBorderColor:", send(class("NSColor"), "separatorColor"))
	send(box, "setFillColor:", fill)
	return box
}

// symbol returns an SF Symbol, which macOS 11 and later have, or 0.
func symbol(name, description string) objc.ID {
	img := class("NSImage")
	if !mainthread.Send[bool](img, sel("respondsToSelector:"), sel("imageWithSystemSymbolName:accessibilityDescription:")) {
		return 0
	}
	return send(img, "imageWithSystemSymbolName:accessibilityDescription:", nsString(name), nsString(description))
}

// layout lays the panel out for the width, and measures its text.
func (f *flow) layout(w float64) {
	v := &f.views
	textW := w - 2*panelPadding - closeSize - 6
	send(v.label, "setPreferredMaxLayoutWidth:", textW)
	f.labelH = math.Ceil(mainthread.Send[nsSize](v.label, sel("fittingSize")).Height)
	h := panelPadding + f.labelH + 8 + cardHeight + panelPadding
	set := func(view objc.ID, r rect) {
		if view != 0 {
			send(view, "setFrame:", r.ns())
		}
	}
	card := rect{panelPadding, panelPadding, w - 2*panelPadding, cardHeight}
	set(v.border, rect{0, 0, w, h})
	set(v.label, rect{panelPadding, h - panelPadding - f.labelH, textW, f.labelH})
	set(v.close, rect{w - panelPadding - closeSize, h - panelPadding - closeSize + 2, closeSize, closeSize})
	set(v.cardBox, card)
	set(v.icon, rect{card.X + 8, card.Y + (cardHeight-iconSize)/2, iconSize, iconSize})
	set(v.name, rect{card.X + 8 + iconSize + 8, card.Y + (cardHeight-20)/2, card.W - iconSize - 70, 20})
	set(v.hand, rect{card.maxX() - 38, card.Y + 20, 24, 18})
	set(v.hint, rect{card.maxX() - 44, card.Y + 6, 36, 12})
	set(v.card, card)
	send(f.panel, "invalidateCursorRectsForView:", v.card)
}

// destroy closes the panel. Main thread.
func (f *flow) destroy() {
	if active == f {
		active = nil
		dragging.Store(false)
	}
	if f.panel == 0 {
		return
	}
	send(f.panel, "orderOut:", objc.ID(0))
	send(f.panel, "close")
	release(f.panel)
	f.panel, f.views = 0, panelViews{}
}
