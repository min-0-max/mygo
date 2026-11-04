//go:build darwin

package mainthread

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/wait"
)

// appKitEnv makes the test binary the helper process of
// TestTheDecisionsLeaveAppKitFontsWorking.
const appKitEnv = "MYGO_MAINTHREAD_TEST_APPKIT"

// appKitMain is the helper process of
// TestTheDecisionsLeaveAppKitFontsWorking. Before AppKit is loaded, it
// loads what the package calls through and decides a CFRelease through
// ByReceiver, as a first forwarded call of a program does before its text
// engine loads AppKit; then it loads AppKit and asks NSFont for the
// system font.
func appKitMain() int {
	const (
		cfPath     = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
		appKitPath = "/System/Library/Frameworks/AppKit.framework/AppKit"
		rtldNoLoad = 0x10 // RTLD_NOLOAD: open only a library that is loaded
	)
	fail := func(err error) int { fmt.Println(err); return 1 }
	if _, err := purego.Dlopen(appKitPath, purego.RTLD_NOW|rtldNoLoad); err == nil {
		return fail(fmt.Errorf("AppKit is loaded before the helper loads it"))
	}
	if err := loadNative(); err != nil {
		return fail(err)
	}
	cf, err := purego.Dlopen(cfPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return fail(err)
	}
	var stringCreate func(alloc uintptr, s string, encoding uint32) uintptr
	purego.RegisterLibFunc(&stringCreate, cf, "CFStringCreateWithCString")
	before := Forwarded()
	ByReceiver(decisions.cfRelease, stringCreate(0, "mygo", 0x08000100))
	fmt.Println("the CFRelease of a CFString ran on the calling thread:", Forwarded() == before)
	if _, err := purego.Dlopen(appKitPath, purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		return fail(err)
	}
	font := objc.ID(objc.GetClass("NSFont")).Send(objc.RegisterName("systemFontOfSize:weight:"), 13.0, 0.0)
	fmt.Println("NSFont returned the system font:", font != 0)
	return 0
}

// What the package loads and decides before a program loads AppKit leaves
// the fonts of AppKit working once it does.
func TestTheDecisionsLeaveAppKitFontsWorking(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), appKitEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for _, w := range []string{
		"the CFRelease of a CFString ran on the calling thread: true",
		"NSFont returned the system font: true",
	} {
		if !strings.Contains(string(out), w+"\n") {
			t.Errorf("no %q in the output:\n%s", w, out)
		}
	}
}
