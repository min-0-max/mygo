//go:build darwin

package darwin

import "testing"

// TestSystemSymbol reads the symbols of a search field in the sizes that its images have at their natural size, and refuses a
// name that the system does not have.
func TestSystemSymbol(t *testing.T) {
	load()
	th := theme{b: &Backend{app: send(class("NSApplication"), "sharedApplication")}}
	for _, name := range []string{"magnifyingglass", "xmark.circle.fill"} {
		mask, ok := th.SystemSymbol(name, 13, 2)
		if !ok {
			t.Fatalf("no symbol %s", name)
		}
		b := mask.Bounds()
		if b.Dx() < 26 || b.Dx() > 36 || b.Dy() < 26 || b.Dy() > 36 {
			t.Fatalf("%s is %v", name, b)
		}
		painted, clear := 0, 0
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				if _, _, _, a := mask.At(x, y).RGBA(); a > 0xc000 {
					painted++
				} else if a == 0 {
					clear++
				}
			}
		}
		if painted == 0 || clear == 0 {
			t.Fatalf("%s has %d painted and %d clear pixels", name, painted, clear)
		}
	}
	if _, ok := th.SystemSymbol("no.such.symbol", 13, 2); ok {
		t.Fatal("a symbol that the system does not have")
	}
}
