//go:build darwin

package darwin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/egoist/mygo/internal/platform"
	"github.com/egoist/mygo/internal/wait"
)

// windowScaleEnv makes the test binary the helper process of
// TestWindowScaleChange.
const windowScaleEnv = "MYGO_DARWIN_TEST_WINDOW_SCALE"

// scaleHandler records the scales the window reports, and what the surface
// of the window answers as it reports one.
type scaleHandler struct {
	platform.WindowHandler
	heard []float64
	seen  []float64
	read  func() float64
}

func (h *scaleHandler) ScaleChanged(scale float64) {
	h.heard = append(h.heard, scale)
	if h.read != nil {
		h.seen = append(h.seen, h.read())
	}
}

func (h *scaleHandler) Closed() {}

// SurfaceEvent takes the events of the view, which AppKit calls as it goes
// into the window.
func (h *scaleHandler) SurfaceEvent(platform.SurfaceEvent) bool { return false }

// windowScaleMain is the helper process of TestWindowScaleChange. It never
// shows the window, so no frame comes, and keeps out of the Dock. A test
// cannot change the scale of a display, so the scale that the window last
// reported is set to another one, as after a move from a display of that
// scale.
func windowScaleMain() int {
	load()
	b := New()
	if err := b.Init(notifyHandler{}, platform.AppOptions{ActivationPolicy: "prohibited"}); err != nil {
		fmt.Println(err)
		return 1
	}
	for _, c := range []struct {
		name    string
		surface bool
	}{{"page", false}, {"surface", true}} {
		h := &scaleHandler{}
		opened, err := b.NewWindow(&platform.WindowOptions{Width: 200, Height: 100, Surface: c.surface}, h)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		w := opened.(*window)
		withPool(func() {
			target, selector := w.delegate, "windowDidChangeBackingProperties:"
			if c.surface {
				target, selector = w.surface.view, "viewDidChangeBackingProperties"
				h.read = w.surface.scale
			}
			send(target, "retain")
			deliver := func() { send(target, selector, 0) }
			scale := msgFloat(w.win, sel("backingScaleFactor"))
			// As AppKit calls it when the view goes into the window, and for a
			// change of the color space.
			deliver()
			fmt.Printf("%s same scale: %v\n", c.name, h.heard)
			w.backing = scale + 1
			if c.surface {
				w.surface.backing = scale + 1
			}
			deliver()
			deliver()
			switch {
			case len(h.heard) != 1 || h.heard[0] != scale:
				fmt.Printf("%s before the first frame: %v, the window has %v\n", c.name, h.heard, scale)
			case c.surface && h.seen[0] != scale:
				fmt.Printf("%s the surface answered %v as the window reported %v\n", c.name, h.seen[0], scale)
			default:
				fmt.Printf("%s before the first frame: once, the current one\n", c.name)
			}
			h.heard = nil
			w.Close()
			w.backing = scale + 1
			deliver()
			fmt.Printf("%s closed: %v\n", c.name, h.heard)
		})
	}
	return 0
}

// A change of the scale reaches the handler once, also before the first frame
// and after the surface reads the new scale. AppKit also calls for the same
// scale, as a view goes into a window or the color space changes, and a closed
// window hears nothing.
func TestWindowScaleChange(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), windowScaleEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for _, name := range []string{"page", "surface"} {
		for _, want := range []string{" same scale: []\n", " before the first frame: once, the current one\n", " closed: []\n"} {
			if !strings.Contains(string(out), name+want) {
				t.Errorf("no %q in the output:\n%s", name+want, out)
			}
		}
	}
}
