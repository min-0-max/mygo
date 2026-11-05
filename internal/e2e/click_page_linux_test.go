//go:build linux && (amd64 || arm64)

package e2e

import (
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/internal/linux"
)

// TestClickPageLeavesThePointer checks that a click of a page reaches the
// page as a click of a person does and that the pointer of the person stays
// where it is. The window takes no focus: the click is inside the process.
func TestClickPageLeavesThePointer(t *testing.T) {
	w := newWindow(t, mygo.WindowOptions{})
	w.Page().LoadHTML(`<!doctype html><body style="margin:0;height:100vh"
		onclick="window.clicked = [event.isTrusted, event.clientX, event.clientY].join()">`, "https://example.com/")
	eventually(t, "the page loaded", func() bool {
		ready, _ := mygo.EvalAs[string](w.Page(), "document.readyState")
		return ready == "complete"
	})
	var x, y int32
	mygo.RunOnMain(func() { x, y = linux.TestPointerPosition() })
	if !clickPage(w, 100, 50) {
		t.Fatal("the page was not clicked")
	}
	eventually(t, "the page heard a trusted click at its place", func() bool {
		clicked, _ := mygo.EvalAs[string](w.Page(), "window.clicked")
		return clicked == "true,100,50"
	})
	var afterX, afterY int32
	mygo.RunOnMain(func() { afterX, afterY = linux.TestPointerPosition() })
	if afterX != x || afterY != y {
		t.Errorf("the pointer moved from %d, %d to %d, %d", x, y, afterX, afterY)
	}
}
