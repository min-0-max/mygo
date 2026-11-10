package ui

// Classic scroll bars (Preferences.ClassicScrollbars) take room beside the
// content they scroll, as a web page's do in the system's web view: a
// scroll container whose content overflows it lays the content out again
// in the room the bar leaves, and draws the bar, a thumb on a track, in
// the room it took. WebKit does so with legacy scrollers: a box of
// overflow auto lays out without bars, and again with the bars of the
// directions that overflowed, keeping them even where the content then
// fits (RenderLayerScrollableArea's updateScrollbarsAfterLayout); a bar
// sits inside the box's border, outside its padding, and is as wide as
// AppKit's NSScroller at the regular control size, or the small one for
// scrollbar-width: thin (ThinScrollbars).

// scrollGutter is the room the classic scroll bars of a scroll container
// take inside its border: v across the vertical bar, h across the
// horizontal one, 0 where a bar does not show, as for every bar that
// overlays. border is the container's border, inside which they go.
type scrollGutter struct {
	v, h   float32
	border [4]float32
}

// classic reports whether the container has classic bars.
func (g scrollGutter) classic() bool { return g.v > 0 || g.h > 0 }

// Bits of barStyle.
const (
	barsHidden uint8 = 1 << iota
	barsThin
)

// barStyle returns whether the element's scroll bars are hidden
// (Scrollbars(false)) and thin (ThinScrollbars), as it says or the
// element it takes them from does (barsFrom), which classic bars lay out
// by.
func (e *node) barStyle() uint8 {
	if e.barsFrom != nil {
		e = e.barsFrom
	}
	var s uint8
	if e.hideScrollbars {
		s |= barsHidden
	}
	if e.thinScrollbars {
		s |= barsThin
	}
	return s
}

// classicBarWidth returns how wide the classic scroll bars of the
// element, a scroll container, are, 0 where they overlay its content or
// it hides them, which then take no room, as scrollbar-width: none.
func (e *node) classicBarWidth() float32 {
	p := e.c.rt.preferences()
	if !p.ClassicScrollbars {
		return 0
	}
	s := e.barStyle()
	switch {
	case s&barsHidden != 0:
		return 0
	case s&barsThin != 0:
		return max(p.ThinClassicScrollbarWidth, 0)
	}
	return max(p.ClassicScrollbarWidth, 0)
}

// classicBars returns the widths of the classic bars that scroll
// containers lay out by, regular and thin, both 0 where bars overlay.
func (rt *engine) classicBars() [2]float32 {
	p := rt.preferences()
	if !p.ClassicScrollbars {
		return [2]float32{}
	}
	return [2]float32{p.ClassicScrollbarWidth, p.ThinClassicScrollbarWidth}
}

// overflows reports whether content of a container of size view, padding
// and border included, overflows it with room taken by a bar across it.
func overflows(content, view, taken float32) bool { return content > view-taken+0.5 }

// containerBars returns where the scroll bars of a container go: its
// classic bars in its gutter when it has one, else those that overlay it
// (scrollBars).
func containerBars(box Rect, inset [4]float32, gut scrollGutter, w, h, x, y float32, flags uint32, width float32, rtl bool) scrollGeometry {
	if gut.classic() {
		return classicBars(box, gut, w, h, x, y, rtl)
	}
	return scrollBars(box, inset, w, h, x, y, flags, width, rtl)
}

// classicBars returns the classic scroll bars of a container box scrolled
// by (x, y) over content w×h, both border boxes, with its gutter gut: a
// track fills the room a bar takes, beside the view inside the border,
// and leaves the corner where both show empty; ScrollbarInsets moves
// none, as a web page's. A bar whose content fits, which it keeps, has a
// track and no thumb. Right to left, the vertical bar is on the left, and
// x counts from the right, as for overlay bars.
func classicBars(box Rect, gut scrollGutter, w, h, x, y float32, rtl bool) scrollGeometry {
	var g scrollGeometry
	b := gut.border
	in := Rect{box.X + b[3], box.Y + b[0], max(box.W-b[1]-b[3], 0), max(box.H-b[0]-b[2], 0)}
	// The view and the content, inside the border and beside the bars.
	viewW, viewH := max(in.W-gut.v, 0), max(in.H-gut.h, 0)
	contentW, contentH := w-b[1]-b[3]-gut.v, h-b[0]-b[2]-gut.h
	if gut.v > 0 {
		g.vertical = true
		g.vTrack = Rect{in.X + in.W - gut.v, in.Y, gut.v, viewH}
		if rtl {
			g.vTrack.X = in.X
		}
		if contentH > viewH+0.5 {
			t := scrollThumb(g.vTrack.Y, g.vTrack.H, viewH, contentH, y)
			across := classicThumb(gut.v)
			g.v = Rect{g.vTrack.X + (gut.v-across)/2, t.Y, across, t.H}
		}
	}
	if gut.h > 0 {
		g.horizontal = true
		g.hTrack = Rect{in.X, in.Y + in.H - gut.h, viewW, gut.h}
		if rtl {
			g.hTrack.X += gut.v
		}
		if contentW > viewW+0.5 {
			t := scrollThumb(g.hTrack.X, g.hTrack.W, viewW, contentW, x)
			across := classicThumb(gut.h)
			g.h = Rect{t.Y, g.hTrack.Y + (gut.h-across)/2, t.H, across}
			if rtl {
				// As far from the right of the track as the thumb would be
				// from its left.
				g.h.X = g.hTrack.X + g.hTrack.W - (t.Y - g.hTrack.X) - t.H
			}
		}
	}
	return g
}

// classicThumb returns how thick the thumb of a classic bar width DIPs
// wide is: half as thick, or 8 DIPs thinner where that is thicker, which
// leaves the track's room around it, as AppKit's knob does.
func classicThumb(width float32) float32 { return max(width-8, width/2) }

// classicScrollbars paints the classic bars g of e: their tracks, the
// corner between them, and their thumbs, darker while dragged.
func (p *Painter) classicScrollbars(e *node, g scrollGeometry, dragging, horizontal bool) {
	t := p.Theme()
	track := t.ScrollbarTrack
	if track == Transparent {
		track = t.Surface
	}
	line := t.Border
	thumb := t.Scrollbar
	if g.vertical {
		p.fill(g.vTrack, [4]float32{}, track, 0, Color{})
		// The line between the bar and the view.
		edge := Rect{g.vTrack.X, g.vTrack.Y, 1, g.vTrack.H}
		if e.rtl() {
			edge.X = g.vTrack.X + g.vTrack.W - 1
		}
		p.fill(edge, [4]float32{}, line, 0, Color{})
	}
	if g.horizontal {
		p.fill(g.hTrack, [4]float32{}, track, 0, Color{})
		p.fill(Rect{g.hTrack.X, g.hTrack.Y, g.hTrack.W, 1}, [4]float32{}, line, 0, Color{})
	}
	if g.vertical && g.horizontal {
		corner := Rect{g.vTrack.X, g.hTrack.Y, g.vTrack.W, g.hTrack.H}
		p.fill(corner, [4]float32{}, track, 0, Color{})
	}
	for _, bar := range [2]struct {
		r     Rect
		h, on bool
	}{{g.v, false, g.vertical}, {g.h, true, g.horizontal}} {
		if !bar.on || bar.r.W <= 0 || bar.r.H <= 0 {
			continue
		}
		c := thumb
		if dragging && bar.h == horizontal {
			c = c.Alpha(1.5)
		}
		r := min(bar.r.W, bar.r.H) / 2
		p.fill(bar.r, [4]float32{r, r, r, r}, c, 0, Color{})
	}
}
