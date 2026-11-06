//go:build darwin

package mainthread

import (
	"testing"

	"github.com/ebitengine/purego"
)

type cgPoint struct{ X, Y float64 }
type cgSize struct{ W, H float64 }
type cgRect struct {
	Origin cgPoint
	Size   cgSize
}
type cfRange struct{ Location, Length int }
type cgAffineTransform struct{ A, B, C, D, Tx, Ty float64 }

func dlsym(t *testing.T, path, name string) uintptr {
	t.Helper()
	lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := purego.Dlsym(lib, name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const (
	libSystem      = "/usr/lib/libSystem.B.dylib"
	coreGraphics   = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	coreFoundation = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
)

// A record carries each kind of argument and result to the function it
// calls as the procedure call standard places them.
func TestARecordCallsAsTheCallingConventionPlacesValues(t *testing.T) {
	if got := direct[float64](dlsym(t, libSystem, "fmax"), 2.5, 7.25); got != 7.25 {
		t.Errorf("fmax(2.5, 7.25) = %v", got)
	}
	if got := direct[float32](dlsym(t, libSystem, "fmaxf"), float32(1.5), float32(-3)); got != 1.5 {
		t.Errorf("fmaxf(1.5, -3) = %v", got)
	}
	if got := direct[int64](dlsym(t, libSystem, "labs"), int64(-42)); got != 42 {
		t.Errorf("labs(-42) = %v", got)
	}
	u := direct[cgRect](dlsym(t, coreGraphics, "CGRectUnion"), cgRect{cgPoint{0, 0}, cgSize{10, 10}}, cgRect{cgPoint{5, 5}, cgSize{20, 30}})
	if want := (cgRect{cgPoint{0, 0}, cgSize{25, 35}}); u != want {
		t.Errorf("CGRectUnion = %+v, want %+v", u, want)
	}
	if got := direct[cfRange](dlsym(t, libSystem, "ldiv"), int64(17), int64(5)); got != (cfRange{3, 2}) {
		t.Errorf("ldiv(17, 5) = %+v", got)
	}
	rot := direct[cgAffineTransform](dlsym(t, coreGraphics, "CGAffineTransformMakeTranslation"), 3.0, 4.0)
	if rot != (cgAffineTransform{1, 0, 0, 1, 3, 4}) {
		t.Errorf("CGAffineTransformMakeTranslation(3, 4) = %+v", rot)
	}
	if got := direct[bool](dlsym(t, coreGraphics, "CGRectIsEmpty"), cgRect{}); !got {
		t.Error("CGRectIsEmpty(CGRectZero) = false")
	}
	r := direct[cgRect](dlsym(t, coreGraphics, "CGRectInset"), cgRect{cgPoint{0, 0}, cgSize{10, 10}}, 1.0, 2.0)
	if want := (cgRect{cgPoint{1, 2}, cgSize{8, 6}}); r != want {
		t.Errorf("CGRectInset = %+v, want %+v", r, want)
	}
	if got := direct[float64](dlsym(t, libSystem, "ldexp"), 1.5, int32(3)); got != 12 {
		t.Errorf("ldexp(1.5, 3) = %v", got)
	}
}

// Arguments past the registers go on the stack.
func TestARecordCarriesStackArguments(t *testing.T) {
	sum := purego.NewCallback(func(a, b, c, d, e, f, g, h, i, j, k, l uintptr) uintptr {
		return a + 2*b + 3*c + 4*d + 5*e + 6*f + 7*g + 8*h + 9*i + 10*j + 11*k + 12*l
	})
	got := direct[uintptr](sum, uintptr(1), uintptr(1), uintptr(1), uintptr(1), uintptr(1), uintptr(1), uintptr(1), uintptr(1), uintptr(1), uintptr(2), uintptr(3), uintptr(4))
	if want := uintptr(36 + 9 + 20 + 33 + 48); got != want {
		t.Errorf("sum = %d, want %d", got, want)
	}
}
