//go:build darwin && arm64

package mainstack

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/internal/wait"
	"github.com/egoist/mygo/ui"
)

// jsSynchronousGarbageCollect is the C function of JavaScriptCore that
// runs a full collection of the VM of a context and returns once it ended.
var jsSynchronousGarbageCollect uintptr

// loadJavaScriptCore loads the native functions of the tests and
// JavaScriptCore.
func loadJavaScriptCore() error {
	if err := loadNative(); err != nil {
		return err
	}
	lib, err := purego.Dlopen("/System/Library/Frameworks/JavaScriptCore.framework/JavaScriptCore", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	jsSynchronousGarbageCollect, err = purego.Dlsym(lib, "JSSynchronousGarbageCollectForDebugging")
	return err
}

// nsString returns a string that the caller owns.
func nsString(s string) objc.ID {
	b := append([]byte(s), 0)
	return class("NSString").Send(sel("alloc")).Send(sel("initWithUTF8String:"), &b[0])
}

// evaluate runs script in ctx on the calling thread and returns its result
// as a number and whether it threw.
func evaluate(ctx objc.ID, script string) (float64, bool) {
	s := nsString(script)
	defer s.Send(sel("release"))
	v := ctx.Send(sel("evaluateScript:"), s)
	if ctx.Send(sel("exception")) != 0 {
		return 0, false
	}
	return objc.Send[float64](v, sel("toDouble")), true
}

// collectorRuns is how many times the collector test runs its child, each
// of which must run to its end.
const collectorRuns = 3

// The collector of a JavaScriptCore VM scans the stack of each thread
// that holds the VM, the main thread among them, from the thread's stack
// pointer to the origin of its stack while it suspends the thread: the
// collector child evaluates a script on the main thread, which registers
// the main thread with the VM, and runs a mygo window that builds frames
// there while a thread with no Go code allocates in the same VM until the
// collector ran many times. A thread of the VM that the collector suspends
// on a stack that the Go heap holds ends the process, as JavaScriptCore's
// allocator fails to copy a stack whose size wrapped around; the main
// thread is the only thread of the VM that runs Go code.
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

// The allocations of the collector child: each round makes 200,000
// objects, which the collector frees, and collectorRounds of them collect
// many times. Each round counts itself in rounds.
const (
	collectorRounds = 300
	allocation      = `var a = []; for (var i = 0; i < 200000; i++) a.push({i: i, s: "x" + i}); rounds++`
)

// collectorChild runs the app of the collector test on the main thread.
// The threads of the VM other than the main thread run no Go code: an
// NSThread of its own evaluates each round in the context of the main
// thread and ends, so that the main thread takes the VM between rounds,
// and a thread that pthread_create starts runs a full collection that
// ends the collections of the rounds before the app quits, so that none
// runs once App.Run returned and the main thread runs Go code again.
func collectorChild() int {
	fail := func(err error) int {
		fmt.Println("error:", err)
		return 1
	}
	if err := loadJavaScriptCore(); err != nil {
		return fail(err)
	}
	// The main thread holds the VM: the evaluation registers it.
	vm := class("JSVirtualMachine").Send(sel("alloc")).Send(sel("init"))
	ctx := class("JSContext").Send(sel("alloc")).Send(sel("initWithVirtualMachine:"), vm)
	if _, ok := evaluate(ctx, "var rounds = 0; 1 + 1"); !ok {
		return fail(fmt.Errorf("the script threw on the main thread"))
	}
	script := nsString(allocation)
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
			for round := range collectorRounds {
				allocator := class("NSThread").Send(sel("alloc")).Send(sel("initWithTarget:selector:object:"), ctx, sel("evaluateScript:"), script)
				allocator.Send(sel("start"))
				finished := wait.Poll(func() bool { return objc.Send[bool](allocator, sel("isFinished")) })
				allocator.Send(sel("release"))
				if !finished {
					allocated <- fmt.Errorf("round %d did not end (waited %v)", round, wait.Guard)
					return
				}
			}
			var collector uintptr
			if rc := pthreadCreate(&collector, 0, jsSynchronousGarbageCollect, uintptr(ctx.Send(sel("JSGlobalContextRef")))); rc != 0 {
				allocated <- fmt.Errorf("pthread_create: %d", rc)
				return
			}
			pthreadJoin(collector, 0)
			allocated <- nil
		}()
	})
	if err := mygo.App.Run(); err != nil {
		return fail(err)
	}
	if err := <-allocated; err != nil {
		return fail(err)
	}
	rounds, ok := evaluate(ctx, "rounds")
	if !ok || rounds != collectorRounds {
		return fail(fmt.Errorf("the allocations ran %v rounds (threw %v), not %d", rounds, !ok, collectorRounds))
	}
	script.Send(sel("release"))
	ctx.Send(sel("release"))
	vm.Send(sel("release"))
	fmt.Printf("%d rounds of allocation, %d frames\n", int(rounds), frames.Load())
	fmt.Println("end")
	return 0
}
