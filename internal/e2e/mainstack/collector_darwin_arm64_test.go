//go:build darwin && arm64

package mainstack

import (
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/internal/wait"
	"github.com/egoist/mygo/ui"
)

// The functions of JavaScriptCore that the collector child calls.
var (
	jsContextGroupCreate         func() uintptr
	jsGlobalContextCreateInGroup func(group, class uintptr) uintptr
	jsGlobalContextRelease       func(ctx uintptr)
	jsStringCreateWithUTF8String func(s *byte) uintptr
	jsStringRelease              func(s uintptr)
	jsEvaluateScript             func(ctx, script, this, source uintptr, line int32, exception *uintptr) uintptr
)

func loadJavaScriptCore() error {
	lib, err := purego.Dlopen("/System/Library/Frameworks/JavaScriptCore.framework/JavaScriptCore", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&jsContextGroupCreate, lib, "JSContextGroupCreate")
	purego.RegisterLibFunc(&jsGlobalContextCreateInGroup, lib, "JSGlobalContextCreateInGroup")
	purego.RegisterLibFunc(&jsGlobalContextRelease, lib, "JSGlobalContextRelease")
	purego.RegisterLibFunc(&jsStringCreateWithUTF8String, lib, "JSStringCreateWithUTF8CString")
	purego.RegisterLibFunc(&jsStringRelease, lib, "JSStringRelease")
	purego.RegisterLibFunc(&jsEvaluateScript, lib, "JSEvaluateScript")
	return nil
}

// evaluate runs script in ctx and reports whether it threw.
func evaluate(ctx uintptr, script string) bool {
	b := append([]byte(script), 0)
	s := jsStringCreateWithUTF8String(&b[0])
	defer jsStringRelease(s)
	var exception uintptr
	jsEvaluateScript(ctx, s, 0, 0, 1, &exception)
	return exception == 0
}

// collectorRuns is how many times the collector test runs its child, each
// of which must run to its end.
const collectorRuns = 3

// The collector of a JavaScriptCore VM scans the stack of each thread
// that holds the VM, the main thread among them, from the thread's stack
// pointer to the origin of its stack while it suspends the thread: the
// collector child evaluates a script on the main thread, which registers
// the main thread with the VM, and runs a mygo window that builds frames
// there while a thread of its own allocates in the same VM until the
// collector ran many times. A main thread that the collector suspends on a
// stack that the Go heap holds ends the process, as JavaScriptCore's
// allocator fails to copy a stack whose size wrapped around.
func TestTheCollectorScansTheMainThreadWhileTheAppRuns(t *testing.T) {
	var failed []string
	for run := 1; run <= collectorRuns; run++ {
		out, err := runChild(t, "collector")
		if err == nil && strings.Contains(out, "\nend\n") {
			continue
		}
		failed = append(failed, fmt.Sprintf("run %d: %v\n%s", run, err, firstLines(out, 12)))
	}
	if len(failed) > 0 {
		t.Fatalf("the collector child ended before its end in %d of %d runs:\n%s", len(failed), collectorRuns, strings.Join(failed, "\n"))
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// The allocations of the allocator thread: each evaluation makes 200,000
// objects, which the collector frees, and collectorRounds of them collect
// many times.
const (
	allocation      = `var a = []; for (var i = 0; i < 200000; i++) a.push({i: i, s: "x" + i}); a.length`
	collectorRounds = 300
)

// collectorChild runs the app of the collector test on the main thread.
func collectorChild() int {
	fail := func(err error) int {
		fmt.Println("error:", err)
		return 1
	}
	if err := loadJavaScriptCore(); err != nil {
		return fail(err)
	}
	// The main thread holds the VM: the evaluation registers it.
	group := jsContextGroupCreate()
	ctx := jsGlobalContextCreateInGroup(group, 0)
	if !evaluate(ctx, "1 + 1") {
		return fail(fmt.Errorf("the script threw on the main thread"))
	}
	var rounds atomic.Int32
	var frames atomic.Uint64
	allocated := make(chan error, 1)
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{Title: "Collector", Width: 400, Height: 300, Content: ui.View(func(c *ui.Context) {
			frames.Add(1)
			c.Invalidate()
			ui.Box(c).Fill()
		})})
		go func() {
			defer mygo.App.Quit()
			if !wait.Poll(func() bool { return frames.Load() > 0 }) {
				allocated <- fmt.Errorf("no frame came")
				return
			}
			go func() {
				runtime.LockOSThread()
				defer runtime.UnlockOSThread()
				other := jsGlobalContextCreateInGroup(group, 0)
				defer jsGlobalContextRelease(other)
				for range collectorRounds {
					if !evaluate(other, allocation) {
						allocated <- fmt.Errorf("the allocation threw")
						return
					}
					rounds.Add(1)
				}
				allocated <- nil
			}()
			wait.Poll(func() bool { return len(allocated) > 0 })
		}()
	})
	if err := mygo.App.Run(); err != nil {
		return fail(err)
	}
	if err := <-allocated; err != nil {
		return fail(err)
	}
	jsGlobalContextRelease(ctx)
	fmt.Printf("%d rounds of allocation, %d frames\n", rounds.Load(), frames.Load())
	fmt.Println("end")
	return 0
}
