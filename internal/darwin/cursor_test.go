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

// TestCursors gives every cursor an NSCursor: AppKit's own, a private
// one, or the nearest public one where macOS lacks it.
func TestCursors(t *testing.T) {
	load()
	withPool(func() {
		arrow := send(class("NSCursor"), "arrowCursor")
		// Without a window server, as over SSH, AppKit gives no arrow or
		// I-beam, which it draws from the system's cursors: the others
		// must still come, and none may raise (_moveCursor did).
		server := arrow != 0
		if !server {
			t.Log("no window server: the arrow and the I-beam are nil")
		}
		for c := platform.CursorDefault; c <= platform.CursorNoDrop; c++ {
			cur := nsCursor(c)
			if cur == 0 && (server || c != platform.CursorDefault && c != platform.CursorText) {
				t.Errorf("cursor %d: no NSCursor", c)
			}
			if c != platform.CursorDefault && cur == arrow {
				t.Logf("cursor %d: the arrow (macOS has none of its own)", c)
			}
		}
		for _, c := range []platform.Cursor{platform.CursorNoDrop, platform.CursorCell, platform.CursorAllScroll} {
			if nsCursor(c) == arrow {
				t.Errorf("cursor %d is the arrow, though its fallback is public", c)
			}
		}
	})
}

// cursorRectEnv makes the test binary the helper process of
// TestPageCursorYieldsToContent.
const cursorRectEnv = "MYGO_DARWIN_TEST_CURSOR_RECT"

// cursorRectMain is the helper process of TestPageCursorYieldsToContent.
// It never shows the window and keeps out of the Dock. The pointer is over
// what the content draws, which sets the pointing hand; AppKit then builds
// the window's cursor rects, as it does as the window becomes key, and
// sends cursorUpdate: to the view under the pointer, as its tracking does
// for a move. A page's cursor then comes, the answer of the web process
// to an earlier move: WebKit's PageClientImpl::setCursor (PageClientImplMac.mm)
// sets it unless AppKit has a cursor rect cursor ([NSApp _cursorRectCursor]),
// and checks only that the window, not the web view, is under the pointer.
func cursorRectMain() int {
	load()
	b := New()
	if err := b.Init(notifyHandler{}, platform.AppOptions{ActivationPolicy: "prohibited"}); err != nil {
		fmt.Println(err)
		return 1
	}
	opened, err := b.NewWindow(&platform.WindowOptions{Width: 200, Height: 100, Surface: true}, &scaleHandler{})
	if err != nil {
		fmt.Println(err)
		return 1
	}
	w := opened.(*window)
	withPool(func() {
		if !respondsTo(b.app, "_cursorRectCursor") {
			fmt.Println("NSApp has no _cursorRectCursor, which WebKit reads")
			return
		}
		s := w.surface
		s.inside = true
		s.SetCursor(platform.CursorPointer)
		send(w.win, "resetCursorRects")
		ev := msgMouseEvent(class("NSEvent"), sel("mouseEventWithType:location:modifierFlags:timestamp:windowNumber:context:eventNumber:clickCount:pressure:"),
			5, NSPoint{X: 50, Y: 50}, 0, 0, sendInt(w.win, "windowNumber"), 0, 0, 0, 0) // NSEventTypeMouseMoved
		send(s.view, "cursorUpdate:", uintptr(ev))
		cls := class("NSCursor")
		if send(b.app, "_cursorRectCursor") == 0 {
			send(send(cls, "arrowCursor"), "set")
		}
		switch current := send(cls, "currentCursor"); current {
		case send(cls, "pointingHandCursor"):
			fmt.Println("cursor: the content's")
		case send(cls, "arrowCursor"):
			fmt.Println("cursor: the page's arrow")
		default:
			fmt.Printf("cursor: another, %#x\n", current)
		}
	})
	return 0
}

// TestPageCursorYieldsToContent checks that a page's cursor that comes
// after the content set its own, while the pointer is over the content,
// leaves the content's: the surface declares its cursor as a cursor rect,
// which WebKit yields to.
func TestPageCursorYieldsToContent(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), cursorRectEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if !strings.Contains(string(out), "cursor: the content's\n") {
		t.Errorf("the page's cursor replaced the content's:\n%s", out)
	}
}
