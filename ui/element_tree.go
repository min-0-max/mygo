package ui

import (
	"errors"
	"fmt"
)

// ElementNode is an element of a frame as its element tree tells it
// (Content.ElementTree, Tester.ElementTree): what assistive technology
// reads of the element, with the key the app gave it. The tree holds the
// elements assistive technology sees, in every build, inspector or not.
type ElementNode struct {
	// Key is the key the app gave the element (Element.Key, Context.Key),
	// nil for none.
	Key any
	// Role is what the element is, never RoleAuto or RoleNone.
	Role Role
	// Label names the element as assistive technology reads it: its
	// Label, or the text inside it.
	Label string
	// Bounds is the element's visible box, in the window's DIPs.
	Bounds Rect
	// Actions are the actions the element takes.
	Actions Actions
	// Children are the elements inside it, in order.
	Children []ElementNode
}

// Actions are the actions an element of the element tree takes, those
// assistive technology performs on it.
type Actions uint8

const (
	// ActionPress presses the element, as a click does.
	ActionPress Actions = 1 << iota
	// ActionFocus gives the element the keyboard focus.
	ActionFocus
	// ActionIncrement and ActionDecrement step the value of a slider or
	// a stepper.
	ActionIncrement
	ActionDecrement
	// ActionSetValue sets the value of a text field.
	ActionSetValue
	// ActionScrollIntoView scrolls the containers around the element to
	// show it.
	ActionScrollIntoView
	// ActionExpand opens and closes an item of a tree with children.
	ActionExpand
)

// ElementTree returns the element tree of the last frame of the window
// the content shows in. It reads the frame as it is, building nothing,
// and leaves the frames that follow undescribed, unless assistive
// technology reads the window. Call it on the main thread, outside the
// view function, as from mygo.RunOnMain. It fails for content that shows
// in no window or in several, for a window that built no frame yet, and
// inside the view function, where the frame is being built.
func (v *Content) ElementTree() ([]ElementNode, error) {
	switch len(v.hosts) {
	case 0:
		return nil, errors.New("ui: the content shows in no window")
	case 1:
	default:
		return nil, fmt.Errorf("ui: the content shows in %d windows: give each window a Content of its own to read its element tree", len(v.hosts))
	}
	rt := v.hosts[0].rt
	switch {
	case rt.inFrame:
		return nil, errors.New("ui: ElementTree called inside the view function, while the frame builds")
	case rt.c.root == nil:
		return nil, errors.New("ui: the window built no frame yet")
	}
	return rt.elementTree(), nil
}

// ElementTree returns the element tree of the last frame (see
// Content.ElementTree).
func (t *Tester) ElementTree() []ElementNode { return t.rt.elementTree() }

// elementTree describes the last frame as assistive technology reads it,
// with the elements' keys.
func (rt *engine) elementTree() []ElementNode {
	tree := rt.windowAccess(rt.accessTree())
	keys := rt.elementKeys()
	// Nodes come in tree order, a parent before its children.
	children := make([][]int, len(tree.Nodes))
	var top []int
	for i, n := range tree.Nodes {
		if n.Parent >= 0 {
			children[n.Parent] = append(children[n.Parent], i)
		} else {
			top = append(top, i)
		}
	}
	var build func(at []int) []ElementNode
	build = func(at []int) []ElementNode {
		if len(at) == 0 {
			return nil
		}
		out := make([]ElementNode, len(at))
		for j, i := range at {
			n := tree.Nodes[i]
			out[j] = ElementNode{
				Key:      keys[n.ID],
				Role:     Role(n.Role) + RoleGroup,
				Label:    n.Label,
				Bounds:   Rect{X: float32(n.Bounds.X), Y: float32(n.Bounds.Y), W: float32(n.Bounds.W), H: float32(n.Bounds.H)},
				Actions:  Actions(n.Actions),
				Children: build(children[i]),
			}
		}
		return out
	}
	return build(top)
}

// elementKeys returns the keys the app gave the elements of the last
// frame, by element ID.
func (rt *engine) elementKeys() map[uint64]any {
	keys := map[uint64]any{}
	var walk func(e *node)
	walk = func(e *node) {
		for ; e != nil; e = e.next {
			if e.key != nil {
				keys[e.id] = e.key
			}
			walk(e.first)
		}
	}
	walk(rt.c.root)
	return keys
}
