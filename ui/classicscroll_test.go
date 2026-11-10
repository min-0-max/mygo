package ui

import "testing"

// classic are the preferences of a desktop with classic scroll bars, as
// wide as AppKit's legacy scrollers at the regular and the small control
// size.
var classic = Preferences{ClassicScrollbars: true, ClassicScrollbarWidth: 17, ThinClassicScrollbarWidth: 13}

// scrollFixture is a scroll container 200×100 at the top left of the
// window holding a box as high as tall, and wide as the container lets
// it be.
type scrollFixture struct {
	tall      float32
	thin      bool
	scroll    *state
	content   *state
	direction Direction
}

func (f *scrollFixture) view(c *context) {
	s := coreScroll(c).Direction(f.direction).Size(200, 100).ThinScrollbars(f.thin)
	f.scroll = s.st
	s.Children(func() {
		f.content = coreBox(c).Height(f.tall).st
	})
}

func newScrollFixture(t *testing.T, f *scrollFixture, p Preferences) *Tester {
	t.Helper()
	tt := coreNewTesterWith(f.view, 300, 200, TesterOptions{Preferences: p})
	t.Cleanup(tt.rt.close)
	return tt
}

// bars returns where the fixture's scroll bars are.
func (f *scrollFixture) bars() scrollGeometry {
	s := f.scroll
	return containerBars(Rect{s.x, s.y, s.w, s.h}, s.barInset, s.gutter, float32(s.contentW), float32(s.contentH), float32(s.scrollX), float32(s.scrollY), s.flags, s.barWidth, s.rtl)
}

// Overlay scroll bars take no room: the content is as wide as the
// container, overflowing it or not.
func TestClassicScrollbarsOverlayTakesNoRoom(t *testing.T) {
	f := &scrollFixture{tall: 300}
	newScrollFixture(t, f, Preferences{})
	if f.content.w != 200 {
		t.Fatalf("content overflowing a container with overlay bars is %g wide, want 200", f.content.w)
	}
	if f.scroll.gutter.classic() {
		t.Fatalf("overlay bars took room: %+v", f.scroll.gutter)
	}
	if f.scroll.contentW != 200 || f.scroll.contentH != 300 {
		t.Fatalf("content is %gx%g, want 200x300", f.scroll.contentW, f.scroll.contentH)
	}
}

// A classic bar takes its width from the content that overflows, which is
// laid out again beside it, and shows there, a track the height of the
// container, with the pointer away.
func TestClassicScrollbarsNarrowOverflowingContent(t *testing.T) {
	f := &scrollFixture{tall: 300}
	newScrollFixture(t, f, classic)
	if f.content.w != 183 {
		t.Fatalf("content overflowing a container with classic bars is %g wide, want 200-17", f.content.w)
	}
	if got := f.scroll.gutter; got.v != 17 || got.h != 0 {
		t.Fatalf("gutter is %+v, want a vertical bar 17 wide", got)
	}
	// It scrolls as far as before: the content, past the view.
	if f.scroll.contentH != 300 || f.scroll.contentW != 200 {
		t.Fatalf("content is %gx%g, want 200x300", f.scroll.contentW, f.scroll.contentH)
	}
	if !f.scroll.barsShown {
		t.Fatal("a classic bar does not show with the pointer away")
	}
	g := f.bars()
	if !g.vertical || g.horizontal || g.vTrack != (Rect{183, 0, 17, 100}) {
		t.Fatalf("classic bars at %+v, want a vertical track at 183,0 17x100", g)
	}
	if g.v.H <= 0 || g.v.X < g.vTrack.X || g.v.X+g.v.W > g.vTrack.X+g.vTrack.W {
		t.Fatalf("thumb %+v is not on its track %+v", g.v, g.vTrack)
	}
}

// ThinScrollbars takes the small width, as scrollbar-width: thin does.
func TestClassicScrollbarsThinTakeSmallWidth(t *testing.T) {
	f := &scrollFixture{tall: 300, thin: true}
	newScrollFixture(t, f, classic)
	if f.content.w != 187 {
		t.Fatalf("content beside a thin classic bar is %g wide, want 200-13", f.content.w)
	}
	if g := f.bars(); g.vTrack != (Rect{187, 0, 13, 100}) {
		t.Fatalf("thin track at %+v, want 187,0 13x100", g.vTrack)
	}
}

// Content that fits has no bar, which takes no room.
func TestClassicScrollbarsFittingContentKeepsWidth(t *testing.T) {
	f := &scrollFixture{tall: 50}
	newScrollFixture(t, f, classic)
	if f.content.w != 200 || f.scroll.gutter.classic() {
		t.Fatalf("content that fits is %g wide with gutter %+v, want 200 and none", f.content.w, f.scroll.gutter)
	}
	if g := f.bars(); g.vertical || g.horizontal {
		t.Fatalf("content that fits shows bars %+v", g)
	}
}

// Changing the scroller style lays every container out again, though
// nothing in the view changed.
func TestClassicScrollbarsFollowStyleChange(t *testing.T) {
	f := &scrollFixture{tall: 300}
	tt := newScrollFixture(t, f, Preferences{})
	if f.content.w != 200 {
		t.Fatalf("overlay: content is %g wide, want 200", f.content.w)
	}
	tt.SetPreferences(classic)
	if f.content.w != 183 {
		t.Fatalf("after classic bars came: content is %g wide, want 183", f.content.w)
	}
	tt.SetPreferences(Preferences{ClassicScrollbars: true, ClassicScrollbarWidth: 15, ThinClassicScrollbarWidth: 11})
	if f.content.w != 185 {
		t.Fatalf("after classic bars widened: content is %g wide, want 185", f.content.w)
	}
	tt.SetPreferences(Preferences{})
	if f.content.w != 200 || f.scroll.gutter.classic() {
		t.Fatalf("after overlay bars came back: content is %g wide with gutter %+v", f.content.w, f.scroll.gutter)
	}
}

// Right to left, the vertical bar is on the left, and the content right
// of it.
func TestClassicScrollbarsRightToLeft(t *testing.T) {
	f := &scrollFixture{tall: 300, direction: RightToLeft}
	newScrollFixture(t, f, classic)
	if f.content.w != 183 || f.content.x != 17 {
		t.Fatalf("content right to left is at %g, %g wide, want at 17, 183 wide", f.content.x, f.content.w)
	}
	if g := f.bars(); g.vTrack != (Rect{0, 0, 17, 100}) {
		t.Fatalf("track right to left at %+v, want 0,0 17x100", g.vTrack)
	}
}

// Content overflowing both ways takes both bars, and the corner between
// them; content just narrow enough to fit beside no bar overflows beside
// the vertical one, and takes the horizontal one with it.
func TestClassicScrollbarsBoth(t *testing.T) {
	var s *state
	tt := coreNewTesterWith(func(c *context) {
		e := coreScrollBoth(c).Size(200, 100)
		s = e.st
		e.Children(func() { coreBox(c).Size(190, 300) })
	}, 300, 200, TesterOptions{Preferences: classic})
	defer tt.rt.close()
	if s.gutter.v != 17 || s.gutter.h != 17 {
		t.Fatalf("gutter is %+v, want both bars 17 wide", s.gutter)
	}
	// As far as the content goes past the view beside the bars.
	if s.contentW != 190+17 || s.contentH != 300+17 {
		t.Fatalf("content is %gx%g, want 207x317", s.contentW, s.contentH)
	}
	g := containerBars(Rect{s.x, s.y, s.w, s.h}, s.barInset, s.gutter, float32(s.contentW), float32(s.contentH), 0, 0, s.flags, s.barWidth, s.rtl)
	if g.vTrack != (Rect{183, 0, 17, 83}) || g.hTrack != (Rect{0, 83, 183, 17}) {
		t.Fatalf("tracks at %+v and %+v, want 183,0 17x83 and 0,83 183x17", g.vTrack, g.hTrack)
	}
}

// A classic thumb takes presses and drags as an overlay one does, with
// the pointer coming from away, and its track pages.
func TestClassicScrollbarsDragAndPage(t *testing.T) {
	f := &scrollFixture{tall: 1000}
	tt := newScrollFixture(t, f, classic)
	tt.Move(250, 150)
	tt.Frame()
	g := f.bars()
	x, y := g.v.X+g.v.W/2, g.v.Y+g.v.H/2
	tt.Press(x, y)
	tt.Move(x, y+20)
	tt.Release(x, y+20)
	if f.scroll.scrollY <= 0 {
		t.Fatal("dragging a classic thumb did not scroll")
	}
	from := f.scroll.scrollY
	g = f.bars()
	tt.ClickAt(g.vTrack.X+g.vTrack.W/2, g.vTrack.Y+g.vTrack.H-3)
	if f.scroll.scrollY <= from {
		t.Fatalf("a press on the track below the thumb did not page down from %g: %g", from, f.scroll.scrollY)
	}
}

// A List's rows lay out beside a classic bar where they overflow it, and
// as wide as it is where they fit.
func TestClassicScrollbarsList(t *testing.T) {
	var s ListState
	var list, row *state
	n := 100
	tt := coreNewTesterWith(func(c *context) {
		l := coreList(c, &s, n, func(i int) {
			r := coreTextf(c, "Row %d", i).Height(30)
			if i == 0 {
				row = r.st
			}
		}).Size(200, 100)
		list = l.st
	}, 300, 200, TesterOptions{Preferences: classic})
	defer tt.rt.close()
	if row.w != 183 || list.gutter.v != 17 {
		t.Fatalf("a row of a list overflowing is %g wide with gutter %+v, want 183 and 17", row.w, list.gutter)
	}
	n = 2
	tt.Frame()
	tt.Frame()
	if row.w != 200 || list.gutter.classic() {
		t.Fatalf("a row of a list that fits is %g wide with gutter %+v, want 200 and none", row.w, list.gutter)
	}
}
