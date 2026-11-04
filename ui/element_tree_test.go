package ui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/internal/platform"
	"github.com/egoist/mygo/internal/surface"
)

// elementView is a view of keyed and unkeyed elements: a labeled group
// holding a button, a text input and a check box keyed before they are
// made, and a button without a key.
func elementView(name *string, agree *bool) func(c *Context) {
	return func(c *Context) {
		Column(c).Fill().Children(func() {
			Row(c).Label("Bar").Key("bar").Children(func() {
				Button(c, "Add").Key("add")
			})
			c.Key("name")
			TextInput(c, name)
			c.Key(7)
			Checkbox(c, agree, "Agree")
			Button(c, "Plain")
		})
	}
}

// findElement returns the element of the tree labeled label.
func findElement(t *testing.T, tree []ElementNode, label string) ElementNode {
	t.Helper()
	n, ok := findIn(tree, label)
	if !ok {
		t.Fatalf("no element %q in %+v", label, tree)
	}
	return n
}

func findIn(tree []ElementNode, label string) (ElementNode, bool) {
	for _, n := range tree {
		if n.Label == label {
			return n, true
		}
		if f, ok := findIn(n.Children, label); ok {
			return f, true
		}
	}
	return ElementNode{}, false
}

// TestElementTree checks that a Tester reads the elements of the last
// frame with their keys, roles, names, boxes, actions and children, in a
// build without the inspector too, and without describing the frames for
// assistive technology.
func TestElementTree(t *testing.T) {
	name, agree := "Ada", false
	tt := NewTester(elementView(&name, &agree), 400, 300)
	tree := tt.ElementTree()
	if len(tree) != 4 {
		t.Fatalf("%d elements at the top, want the group, the input, the check box and the plain button: %+v", len(tree), tree)
	}
	bar := tree[0]
	if bar.Key != "bar" || bar.Role != RoleGroup || bar.Label != "Bar" || len(bar.Children) != 1 {
		t.Fatalf("the group: %+v", bar)
	}
	add := bar.Children[0]
	if add.Key != "add" || add.Role != RoleButton || add.Label != "Add" || add.Actions&ActionPress == 0 {
		t.Fatalf("the button in the group: %+v", add)
	}
	text, ok := tt.Find("Add")
	if !ok {
		t.Fatal("the Tester finds no Add")
	}
	if !contains(add.Bounds, text) || !contains(bar.Bounds, add.Bounds) {
		t.Fatalf("Add's box %+v holds its text %+v, and the group's %+v holds it", add.Bounds, text, bar.Bounds)
	}
	for _, c := range []struct {
		n       ElementNode
		key     any
		role    Role
		actions Actions
	}{
		{tree[1], "name", RoleTextField, ActionSetValue},
		{tree[2], 7, RoleCheckBox, ActionPress},
		{tree[3], nil, RoleButton, ActionPress},
	} {
		if c.n.Key != c.key || c.n.Role != c.role || c.n.Actions&c.actions == 0 || c.n.Bounds.W <= 0 || c.n.Bounds.H <= 0 {
			t.Errorf("%q: key %v, role %v, actions %b, box %+v; want key %v, role %v, actions with %b", c.n.Label, c.n.Key, c.n.Role, c.n.Actions, c.n.Bounds, c.key, c.role, c.actions)
		}
	}
	if tt.rt.access || tt.h.access != nil {
		t.Fatalf("reading the tree described the frames for assistive technology (%v, %v)", tt.rt.access, tt.h.access)
	}

	// The boxes are in the window's DIPs.
	tt.SetZoom(2)
	zoomed := findElement(t, tt.ElementTree(), "Add")
	if zoomed.Bounds.W != 2*add.Bounds.W || zoomed.Bounds.H != 2*add.Bounds.H {
		t.Fatalf("zoomed by 2, Add's box is %+v, at 1 %+v", zoomed.Bounds, add.Bounds)
	}
}

func contains(outer, inner Rect) bool {
	return inner.X >= outer.X && inner.Y >= outer.Y && inner.X+inner.W <= outer.X+outer.W && inner.Y+inner.H <= outer.Y+outer.H
}

// TestActElement checks that an action on an element of the tree works as
// assistive technology's does, and that an element refuses an action it
// does not take.
func TestActElement(t *testing.T) {
	name, agree, added := "Ada", false, 0
	tt := NewTester(func(c *Context) {
		Column(c).Fill().Children(func() {
			if Button(c, "Add").Key("add").Clicked() {
				added++
			}
			c.Key("name")
			TextInput(c, &name)
			c.Key(7)
			Checkbox(c, &agree, "Agree")
		})
	}, 400, 300)
	act := func(label string, action Actions, text string) error {
		t.Helper()
		return tt.ActElement(findElement(t, tt.ElementTree(), label).ID, action, text)
	}
	if err := act("Add", ActionPress, ""); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if added != 1 {
		t.Fatalf("the press clicked %d times", added)
	}
	if err := act("Agree", ActionPress, ""); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !agree {
		t.Fatal("the press did not check the box")
	}
	tree := tt.ElementTree()
	var input ElementNode
	for _, n := range tree {
		if n.Key == "name" {
			input = n
		}
	}
	if err := tt.ActElement(input.ID, ActionSetValue, "Grace"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if name != "Grace" {
		t.Fatalf("the value is %q", name)
	}
	if err := act("Add", ActionSetValue, "x"); err == nil || err.Error() != `ui: the element takes no such action` {
		t.Fatalf("a button took a value: %v", err)
	}
	if err := tt.ActElement(1<<60, ActionPress, ""); err == nil || err.Error() != "ui: the last frame has no element 1152921504606846976" {
		t.Fatalf("an absent element: %v", err)
	}
	if err := act("Add", ActionPress|ActionFocus, ""); err == nil || err.Error() != `ui: the action must be one action` {
		t.Fatalf("two actions: %v", err)
	}
}

// TestElementTreeActions checks that each action of the element tree is
// the one assistive technology takes.
func TestElementTreeActions(t *testing.T) {
	for _, c := range []struct {
		ui Actions
		p  platform.AccessActions
	}{
		{ActionPress, platform.ActionPress},
		{ActionFocus, platform.ActionFocus},
		{ActionIncrement, platform.ActionIncrement},
		{ActionDecrement, platform.ActionDecrement},
		{ActionSetValue, platform.ActionSetValue},
		{ActionScrollIntoView, platform.ActionScrollIntoView},
		{ActionExpand, platform.ActionExpand},
	} {
		if uint8(c.ui) != uint8(c.p) {
			t.Errorf("action %b is %b to assistive technology", c.ui, c.p)
		}
	}
}

// TestContentElementTree checks that the content reads the element tree
// of the window it shows in once a frame finished, and tells why it reads
// none otherwise.
func TestContentElementTree(t *testing.T) {
	t.Setenv("MYGO_GPU", "0")
	name, agree := "Ada", false
	var v *Content
	var inside error
	view := elementView(&name, &agree)
	v = View(func(c *Context) {
		view(c)
		_, inside = v.ElementTree()
	})
	wantErr := func(what, want string) {
		t.Helper()
		if _, err := v.ElementTree(); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: error %v, want one saying %q", what, err, want)
		}
	}
	wantErr("before the content shows", "shows in no window")

	conn := &surface.Conn{Surface: &occludedSurface{}, Post: func(f func()) { f() }}
	v.AttachContent(conn)
	wantErr("before the first frame", "built no frame")

	conn.Event(platform.SurfaceEvent{Kind: platform.SurfaceFrame})
	if inside == nil || !strings.Contains(inside.Error(), "inside the view function") {
		t.Fatalf("inside the view function: error %v", inside)
	}
	tree, err := v.ElementTree()
	if err != nil {
		t.Fatal(err)
	}
	if add := findElement(t, tree, "Add"); add.Key != "add" || add.Role != RoleButton {
		t.Fatalf("Add: %+v", add)
	}

	other := &surface.Conn{Surface: &occludedSurface{}, Post: func(f func()) { f() }}
	v.AttachContent(other)
	wantErr("in two windows", "shows in 2 windows")
	other.Detach()
	if _, err := v.ElementTree(); err != nil {
		t.Fatalf("after the other window closed: %v", err)
	}
	conn.Detach()
	wantErr("after its window closed", "shows in no window")
}
