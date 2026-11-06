package ui

import "testing"

// The next key replaces the text that SelectAll selected, in an input that takes the focus in the same frame or that holds it.
func TestSelectAllOfAnInput(t *testing.T) {
	text := "term"
	first := true
	var selecting bool
	tt := coreNewTester(func(c *context) {
		in := coreTextInputBase(c, &text).Width(200)
		if first {
			in.Focus().SelectAll()
			first = false
		}
		if selecting {
			in.SelectAll()
			selecting = false
		}
	}, 300, 100)
	tt.Frame()
	tt.Type("x")
	tt.Frame()
	if text != "x" {
		t.Fatalf("a key after SelectAll made %q", text)
	}
	tt.Type("y")
	tt.Frame()
	if text != "xy" {
		t.Fatalf("the selection stayed: %q", text)
	}
	selecting = true
	tt.Frame()
	tt.Type("z")
	tt.Frame()
	if text != "z" {
		t.Fatalf("a key after SelectAll of a focused input made %q", text)
	}
}

// SelectAll of a field goes to the input inside it, and on any other element does nothing.
func TestSelectAllOfAFieldAndOfABox(t *testing.T) {
	text := "term"
	first := true
	tt := coreNewTester(func(c *context) {
		coreBox(c).SelectAll()
		f, _ := field(c, func() *node { return coreTextInputBase(c, &text).Width(200) })
		if first {
			f.Focus().SelectAll()
			first = false
		}
	}, 300, 100)
	tt.Frame()
	tt.Type("x")
	tt.Frame()
	if text != "x" {
		t.Fatalf("a key after SelectAll of the field made %q", text)
	}
}
