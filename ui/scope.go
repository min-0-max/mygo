package ui

import (
	"cmp"
	"slices"
)

// Overlays scope the keyboard. A dialog (DialogBase) keeps the focus: Tab
// moves it among the dialog's elements alone, it moves into the dialog as
// the dialog opens, unless something in it took it, and the window's
// shortcuts built outside it wait while it shows, as what is behind it is
// inert, which assistive technology does not see either. The elements of
// a popover (PopoverBase) follow its anchor in the focus order. Escape
// closes the overlay on top, unless the focused element takes it, and an
// overlay closing with the focus in it gives the focus back to the element
// that had it as it opened.

// focusScope is where an element of the focus order is: in the dialog
// with that backdrop (0 for none), in the popover of that anchor (0 for
// none).
type focusScope struct{ modal, anchor, group uint64 }

// enterScope notes the scope of e and the elements inside it as the
// frame is committed, and returns the scope to restore after them.
func (rt *engine) enterScope(e *node) focusScope {
	saved := rt.commitScope
	if e.flags&flagModal != 0 && !rt.commitHidden {
		// A Hidden dialog scopes nothing.
		rt.commitScope = focusScope{modal: e.id}
		rt.modal, rt.modalLayer = e.id, 0
		for l := e; l.parent != nil; l = l.parent {
			if l.parent.id == overlayID {
				rt.modalLayer = l.id
			}
		}
	}
	if a := e.popover; a != nil {
		// A popover opened from a dialog is in the dialog.
		rt.commitScope = focusScope{modal: a.st.scope, anchor: a.id}
	}
	if e.focusGroup != 0 && rt.commitScope.group == 0 {
		// The outermost group: those inside it are part of it.
		rt.commitScope.group = e.id
		rt.groups[e.id] = groupInfo{
			orient: e.focusGroup, selects: e.groupSelects, rtl: e.rtl(),
			chooses: e.role == RoleRadioGroup || e.role == RoleTabList,
		}
	}
	e.st.scope = rt.commitScope.modal
	return saved
}

// arrangeFocus moves the elements of popovers after their anchors in the
// focus order, and the focus into the dialog on top when it is out of it.
func (rt *engine) arrangeFocus() {
	order, scopes := rt.focusOrder, rt.focusScopes
	if slices.ContainsFunc(scopes, func(sc focusScope) bool { return sc.anchor != 0 }) {
		rt.placePopovers(order, scopes)
	}
	if m := rt.modal; m != 0 && rt.scopeOf(rt.focused) != m {
		for i, sc := range rt.focusScopes {
			if sc.modal == m {
				rt.focused = rt.focusOrder[i]
				break
			}
		}
	}
}

// popoverOrder holds what placePopovers needs, which every frame with a
// popover mounted runs. Frames reuse it.
type popoverOrder struct {
	// links are the elements of popovers, by anchor, those of an anchor in
	// the order they came.
	links  []popoverLink
	placed []bool
	ids    []uint64
	scopes []focusScope
}

// popoverLink is element i of the focus order, of a popover on anchor.
type popoverLink struct {
	anchor uint64
	i      int
	// shown tells that the anchor is in the focus order.
	shown bool
}

// of returns the index of the first link on anchor, and whether it has one.
func (o *popoverOrder) of(anchor uint64) (int, bool) {
	return slices.BinarySearchFunc(o.links, anchor, func(l popoverLink, a uint64) int { return cmp.Compare(l.anchor, a) })
}

// add places element i of order and, after it, the popovers on it.
func (o *popoverOrder) add(i int, order []uint64, scopes []focusScope) {
	if o.placed[i] {
		return
	}
	o.placed[i] = true
	o.ids, o.scopes = append(o.ids, order[i]), append(o.scopes, scopes[i])
	at, ok := o.of(order[i])
	for ; ok && at < len(o.links) && o.links[at].anchor == order[i]; at++ {
		o.add(o.links[at].i, order, scopes)
	}
}

// placePopovers moves the elements of popovers after their anchors in the
// focus order.
func (rt *engine) placePopovers(order []uint64, scopes []focusScope) {
	o := &rt.popovers
	o.links = o.links[:0]
	for i, sc := range scopes {
		if sc.anchor != 0 {
			o.links = append(o.links, popoverLink{anchor: sc.anchor, i: i})
		}
	}
	slices.SortStableFunc(o.links, func(a, b popoverLink) int { return cmp.Compare(a.anchor, b.anchor) })
	moved := false
	for _, id := range order {
		at, ok := o.of(id)
		for ; ok && at < len(o.links) && o.links[at].anchor == id; at++ {
			o.links[at].shown, moved = true, true
		}
	}
	if !moved {
		return
	}
	o.placed = slices.Grow(o.placed[:0], len(order))[:len(order)]
	clear(o.placed)
	o.ids, o.scopes = o.ids[:0], o.scopes[:0]
	for i, sc := range scopes {
		if at, ok := o.of(sc.anchor); !ok || !o.links[at].shown {
			o.add(i, order, scopes)
		}
	}
	for i := range order {
		o.add(i, order, scopes) // popovers of anchors in no order
	}
	rt.focusOrder, rt.focusScopes, o.ids, o.scopes = o.ids, o.scopes, order, scopes
}

// scopeOf returns the dialog the element is in, 0 for none.
func (rt *engine) scopeOf(id uint64) uint64 {
	if s := rt.states[id]; s != nil {
		return s.scope
	}
	return 0
}

// overlayShortcut registers that overlay e handles mods+key while the
// focused element and those around it do not, the overlay made last (on
// top) first, and reports whether such a key was delivered to it.
func (rt *engine) overlayShortcut(e *node, mods Modifiers, key Key) bool {
	id := e.id
	rt.nextRegs = append(rt.nextRegs, shortcutReg{id: id, mods: mods, key: key, overlay: true, serial: e.serial})
	for i, d := range rt.delivered {
		if d.id == id && d.mods == mods && d.key == key {
			rt.delivered = append(rt.delivered[:i], rt.delivered[i+1:]...)
			rt.consumed, rt.shortcutRepeat = true, d.repeat
			return true
		}
	}
	return false
}

// openOverlay notes, as overlay id opens, the element that had the focus,
// to give it back as the overlay closes (restoreFocus).
func (rt *engine) openOverlay(back *node) {
	if back.st.born != rt.frame {
		return
	}
	if rt.openers == nil {
		rt.openers = map[uint64]uint64{}
	}
	if _, ok := rt.openers[back.id]; !ok {
		rt.openers[back.id] = rt.focused
	}
}

// restoreFocus gives the focus back to the element that had it as an
// overlay opened, once the overlay is gone with the focus that was in it.
func (rt *engine) restoreFocus() {
	for id, opener := range rt.openers {
		if rt.states[id] != nil {
			continue
		}
		delete(rt.openers, id)
		if rt.states[rt.focused] == nil && rt.states[opener] != nil {
			rt.focused = opener
		}
	}
}

// insideModal reports whether the elements being built are in the dialog
// on top of the last frame, or there is none: the window's shortcuts
// built outside it wait.
func (c *context) insideModal() bool {
	m := c.rt.modal
	if m == 0 {
		return true
	}
	for p := c.parent; p != nil; {
		if p.id == m {
			return true
		}
		if p.popover != nil {
			p = p.popover // a popover is where its anchor is
			continue
		}
		p = p.parent
	}
	return false
}

// Modal makes the element, built in an Overlay, a dialog's backdrop, as
// DialogBase's: while it shows, the focus moves into it, Tab goes round
// the elements inside it, the window's shortcuts built outside it wait,
// and assistive technology sees it and what shows above it alone, as what
// is behind it is inert. Build the dialog inside it.
func (e *node) Modal() *node {
	e.flags |= flagModal
	return e
}

// OverlayShortcut reports whether the key with exactly the modifiers mods
// was pressed for the element, an overlay, which gets the keys the focused
// element and those around it leave, wherever the focus is: the overlay
// built last first, so that Escape closes the one on top. Overlays close
// with it, as PopoverBase and DialogBase do:
//
//	if panel.OverlayShortcut(0, ui.KeyEscape) {
//		app.open = false
//	}
func (e *node) OverlayShortcut(mods Modifiers, key Key) bool {
	if !e.hasState() || e.c.inert {
		return false
	}
	return e.c.rt.overlayShortcut(e, mods, key)
}

// PressedOutside reports whether the pointer went down since the last
// frame outside the element: on neither it, nor what it holds, nor the
// element it is attached to (AttachTo), nor the overlays attached to
// elements inside it, as the popover of one of its buttons. A popover
// closes then, the press going on to what is under the pointer, as
// PopoverBase does. The last press decides: one on the button opening a
// popover after a press elsewhere, as on a web view's page, in the same
// frame, keeps it open. A press before the element showed, as the one
// opening it, is not outside it: an Overlay opened by a press stays.
func (e *node) PressedOutside() bool {
	if !e.hasState() || len(e.c.rt.downs) == 0 || e.st.born == e.c.rt.frame {
		return false
	}
	rt := e.c.rt
	if rt.pressedWithin(rt.downs[len(rt.downs)-1], e) {
		return false
	}
	rt.consumed = true
	return true
}

// pressedWithin reports whether element id of the last frame is e, inside it,
// the element e is attached to, or in an overlay attached to one of them.
func (rt *engine) pressedWithin(id uint64, e *node) bool {
	var anchor uint64
	if e.popover != nil {
		anchor = e.popover.id
	}
	s := rt.states[id]
	for range 1000 { // against cycles of anchors
		if s == nil {
			return false
		}
		if s.id == e.id || anchor != 0 && s.id == anchor {
			return true
		}
		next := s.parent
		if s.anchor != 0 {
			next = s.anchor // a popover is where its anchor is
		}
		if next == 0 {
			return false
		}
		s = rt.states[next]
	}
	return false
}
