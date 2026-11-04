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

	"github.com/egoist/mygo/internal/wait"
)

// otherThreadEnv makes the test binary the helper process of
// TestRunOnMainOfAnotherThreadRunsOnTheWorker.
const otherThreadEnv = "MYGO_MAINTHREAD_TEST_OTHER_THREAD"

// otherThreadMain is the helper process of
// TestRunOnMainOfAnotherThreadRunsOnTheWorker. On the main thread it runs
// the worker around the main run loop, while a goroutine calls RunOnMain
// with a function that forwards a call and a callback of Go, and then
// stops the run loop through another RunOnMain. It prints where the
// function, its call and the callback ran, and how many calls the first
// RunOnMain forwarded.
func otherThreadMain() int {
	const cfPath = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	fail := func(err error) int { fmt.Println(err); return 1 }
	cf, err := purego.Dlopen(cfPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return fail(err)
	}
	var (
		runLoopRun func()
		getMain    func() uintptr
	)
	purego.RegisterLibFunc(&runLoopRun, cf, "CFRunLoopRun")
	purego.RegisterLibFunc(&getMain, cf, "CFRunLoopGetMain")
	stop, err := purego.Dlsym(cf, "CFRunLoopStop")
	if err != nil {
		return fail(err)
	}
	pthreadSelf, err := purego.Dlsym(purego.RTLD_DEFAULT, "pthread_self")
	if err != nil {
		return fail(err)
	}
	self := func() uintptr { r, _, _ := purego.SyscallN(pthreadSelf); return r }
	var main, worker, ranOn, inner, callbackOn uintptr
	var counted bool
	var n uint64
	whoAmI := Callback(purego.NewCallback(func() uintptr {
		callbackOn = self()
		return callbackOn
	}))
	main = self()
	err = Run(func() error {
		worker = self()
		return nil
	}, func() {
		go func() {
			before := Forwarded()
			RunOnMain(func() {
				ranOn = self()
				counted = IsMainThread()
				inner = Forward[uintptr](pthreadSelf)
				Forward[uintptr](whoAmI)
			})
			n = Forwarded() - before
			RunOnMain(func() { Forward[struct{}](stop, getMain()) })
		}()
		runLoopRun()
	}, func() {})
	if err != nil {
		return fail(err)
	}
	fmt.Println("the function ran on the worker:", ranOn == worker && worker != main && counted)
	fmt.Println("its call ran on the main thread:", inner == main)
	fmt.Println("the callback ran on the worker:", callbackOn == worker)
	fmt.Println("forwarded calls:", n)
	return 0
}

// RunOnMain on a goroutine other than the worker runs its function on the
// worker as a step, which the main thread starts from its run loop: the
// native calls of the function run on the main thread, each one forwarded
// call, and a callback that reaches the main thread meanwhile runs on the
// worker as a nested step. With the call that starts the step, the
// RunOnMain of the helper forwards 3 calls.
func TestRunOnMainOfAnotherThreadRunsOnTheWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), otherThreadEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for _, w := range []string{
		"the function ran on the worker: true",
		"its call ran on the main thread: true",
		"the callback ran on the worker: true",
		"forwarded calls: 3",
	} {
		if !strings.Contains(string(out), w+"\n") {
			t.Errorf("no %q in the output:\n%s", w, out)
		}
	}
}
