package ui

import (
	"errors"
	"fmt"

	"github.com/egoist/mygo/internal/platform"
)

// ElementNode is an element of a frame as its element tree tells it
// (Content.ElementTree, Tester.ElementTree): what assistive technology
// reads of the element, with the key the app gave it. The tree holds the
// elements assistive technology sees and those behind a dialog, in every
// build, inspector or not.
type ElementNode struct {
	// ID names the element in the frame for ActElement.
	ID uint64
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
	// Inert is set on an element behind a dialog (Element.Modal). It keeps
	// its Bounds, assistive technology does not see it, and ActElement
	// refuses it.
	Inert bool
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
	rt, err := v.frameEngine()
	if err != nil {
		return nil, err
	}
	return rt.elementTree(), nil
}

// ActElement performs one action on the element id of the last frame, as
// assistive technology does: ActionPress clicks it, and ActionSetValue
// replaces its text with text. What the action changes shows in the frame
// that follows. It fails for an element the frame does not hold, for an
// element behind a dialog and for an action the element does not take, and
// where ElementTree fails.
func (v *Content) ActElement(id uint64, action Actions, text string) error {
	rt, err := v.frameEngine()
	if err != nil {
		return err
	}
	return rt.actElement(id, action, text)
}

// frameEngine returns the engine of the one window the content shows in,
// when its last frame can be read.
func (v *Content) frameEngine() (*engine, error) {
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
		return nil, errors.New("ui: the element tree was asked for inside the view function, while the frame builds")
	case rt.c.root == nil:
		return nil, errors.New("ui: the window built no frame yet")
	}
	return rt, nil
}

// ElementTree returns the element tree of the last frame (see
// Content.ElementTree).
func (t *Tester) ElementTree() []ElementNode { return t.rt.elementTree() }

// ActElement performs an action on an element of the last frame (see
// Content.ActElement).
func (t *Tester) ActElement(id uint64, action Actions, text string) error {
	return t.rt.actElement(id, action, text)
}

// elementActions are the actions of assistive technology that the actions
// of the element tree name.
var elementActions = map[Actions]platform.AccessActionKind{
	ActionPress:          platform.AccessPress,
	ActionFocus:          platform.AccessFocus,
	ActionIncrement:      platform.AccessIncrement,
	ActionDecrement:      platform.AccessDecrement,
	ActionSetValue:       platform.AccessSetValue,
	ActionScrollIntoView: platform.AccessScrollIntoView,
	ActionExpand:         platform.AccessExpand,
}

// actElement gives the element an action of assistive technology, as the
// backend gives it those of the system's.
func (rt *engine) actElement(id uint64, action Actions, text string) error {
	kind, ok := elementActions[action]
	if !ok {
		return errors.New("ui: the action must be one action")
	}
	for _, n := range rt.windowAccess(rt.accessTree()).Nodes {
		if n.ID == id {
			if Actions(n.Actions)&action == 0 {
				return errors.New("ui: the element takes no such action")
			}
			rt.accessAction(platform.SurfaceEvent{Kind: platform.AccessAction, ID: id, Action: kind, Text: text})
			return nil
		}
	}
	if rt.dialogOnTop() {
		for _, n := range rt.describeFrame(false).Nodes {
			if n.ID == id {
				return errors.New("ui: the element is behind a dialog")
			}
		}
	}
	return fmt.Errorf("ui: the last frame has no element %d", id)
}

// elementTree describes the last frame as assistive technology reads it,
// with the elements' keys.
func (rt *engine) elementTree() []ElementNode {
	tree := rt.windowAccess(rt.describeFrame(false))
	// What assistive technology does not see of the whole frame is behind
	// the dialog on top.
	var seen map[uint64]bool
	if rt.dialogOnTop() {
		seen = map[uint64]bool{}
		for _, n := range rt.accessTree().Nodes {
			seen[n.ID] = true
		}
	}
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
				ID:       n.ID,
				Key:      keys[n.ID],
				Role:     Role(n.Role) + RoleGroup,
				Label:    n.Label,
				Bounds:   Rect{X: float32(n.Bounds.X), Y: float32(n.Bounds.Y), W: float32(n.Bounds.W), H: float32(n.Bounds.H)},
				Actions:  Actions(n.Actions),
				Inert:    seen != nil && !seen[n.ID],
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
