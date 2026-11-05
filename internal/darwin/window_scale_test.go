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

// scaleHandler records the scales the window reports.
type scaleHandler struct {
	platform.WindowHandler
	heard []float64
}

func (h *scaleHandler) ScaleChanged(scale float64) { h.heard = append(h.heard, scale) }

// windowScaleMain is the helper process of TestWindowScaleChange. It never
// shows the window and keeps out of the Dock. A test cannot change the scale
// of a display, so the scale that the window last reported is set to another
// one, as after a move from a display of that scale.
func windowScaleMain() int {
	load()
	b := New()
	if err := b.Init(notifyHandler{}, platform.AppOptions{ActivationPolicy: "prohibited"}); err != nil {
		fmt.Println(err)
		return 1
	}
	h := &scaleHandler{}
	opened, err := b.NewWindow(&platform.WindowOptions{Width: 200, Height: 100}, h)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	w := opened.(*window)
	withPool(func() {
		if send(w.delegate, "respondsToSelector:", uintptr(sel("windowDidChangeBackingProperties:")))&0xff == 0 {
			fmt.Println("the delegate does not hear of a change of the backing properties")
			return
		}
		deliver := func() { send(w.delegate, "windowDidChangeBackingProperties:", 0) }
		scale := msgFloat(w.win, sel("backingScaleFactor"))
		deliver()
		fmt.Printf("same scale: %v\n", h.heard)
		h.heard = nil
		w.backing = scale + 1
		deliver()
		deliver()
		if len(h.heard) == 1 && h.heard[0] == scale {
			fmt.Println("another scale: once, the current one")
		} else {
			fmt.Printf("another scale: %v, the window has %v\n", h.heard, scale)
		}
	})
	return 0
}

// AppKit sends windowDidChangeBackingProperties: for a change of the color
// space too, so only a change of the scale reaches the handler, once.
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
	for _, want := range []string{"same scale: []\n", "another scale: once, the current one\n"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("no %q in the output:\n%s", want, out)
		}
	}
}
