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

// windowOcclusionEnv makes the test binary the helper process of
// TestWindowOcclusionChange.
const windowOcclusionEnv = "MYGO_DARWIN_TEST_WINDOW_OCCLUSION"

// occlusionHandler records what the window reports about its occlusion.
type occlusionHandler struct {
	platform.WindowHandler
	heard []bool
}

func (h *occlusionHandler) OcclusionChanged(occluded bool) { h.heard = append(h.heard, occluded) }

func (h *occlusionHandler) Closed() {}

// SurfaceEvent takes the events of the view, which AppKit calls as it goes
// into the window.
func (h *occlusionHandler) SurfaceEvent(platform.SurfaceEvent) bool { return false }

// windowOcclusionMain is the helper process of TestWindowOcclusionChange. It
// never shows the window, so none of the window shows, and keeps out of the
// Dock. The window server tells a window of a change of its occlusion through
// its delegate, which the helper calls as AppKit does.
func windowOcclusionMain() int {
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
		h := &occlusionHandler{}
		opened, err := b.NewWindow(&platform.WindowOptions{Width: 200, Height: 100, Surface: c.surface}, h)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		w := opened.(*window)
		withPool(func() {
			delegate := w.delegate
			send(delegate, "retain")
			deliver := func() { send(delegate, "windowDidChangeOcclusionState:", 0) }
			deliver()
			fmt.Printf("%s hidden: %v\n", c.name, h.heard)
			h.heard = nil
			w.Close()
			deliver()
			fmt.Printf("%s closed: %v\n", c.name, h.heard)
		})
	}
	return 0
}

// A change of the occlusion of a window reaches its handler, for a window of a
// page as for a window of a surface, and a closed window hears nothing.
func TestWindowOcclusionChange(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), windowOcclusionEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for _, name := range []string{"page", "surface"} {
		for _, want := range []string{" hidden: [true]\n", " closed: []\n"} {
			if !strings.Contains(string(out), name+want) {
				t.Errorf("no %q in the output:\n%s", name+want, out)
			}
		}
	}
}
