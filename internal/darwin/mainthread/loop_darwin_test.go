//go:build darwin

package mainthread

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo/internal/wait"
)

// loopEnv makes the test binary the helper process of
// TestOtherThreadsForwardWhileTheMainThreadRunsALoop.
const loopEnv = "MYGO_MAINTHREAD_TEST_LOOP"

// loopMain is the helper process of
// TestOtherThreadsForwardWhileTheMainThreadRunsALoop. On the main thread
// it starts the worker, and a step runs, through RunOnMain, the main run
// loop in a mode of its common modes with a timer of its own, as a modal
// panel does, for up to wait.Guard, while another goroutine forwards a
// call and then stops the run loop; the step waits for the goroutine. It
// prints where that call ran and whether the run loop ended by that stop,
// which the goroutine makes only once the call returned.
func loopMain() int {
	const (
		cfPath         = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
		utf8           = uint32(0x08000100)
		runLoopStopped = 2 // kCFRunLoopRunStopped
	)
	fail := func(err error) int { fmt.Println(err); return 1 }
	cf, err := purego.Dlopen(cfPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return fail(err)
	}
	var (
		runInMode     func(mode uintptr, seconds float64, returnAfterSourceHandled bool) int32
		addCommonMode func(rl, mode uintptr)
		getMain       func() uintptr
		stringCreate  func(alloc uintptr, s string, encoding uint32) uintptr
		now           func() float64
		timerCreate   func(alloc uintptr, fireDate, interval float64, flags uint, order int, callout, context uintptr) uintptr
		addTimer      func(rl, timer, mode uintptr)
		stop          func(rl uintptr)
	)
	purego.RegisterLibFunc(&runInMode, cf, "CFRunLoopRunInMode")
	purego.RegisterLibFunc(&addCommonMode, cf, "CFRunLoopAddCommonMode")
	purego.RegisterLibFunc(&getMain, cf, "CFRunLoopGetMain")
	purego.RegisterLibFunc(&stringCreate, cf, "CFStringCreateWithCString")
	purego.RegisterLibFunc(&now, cf, "CFAbsoluteTimeGetCurrent")
	purego.RegisterLibFunc(&timerCreate, cf, "CFRunLoopTimerCreate")
	purego.RegisterLibFunc(&addTimer, cf, "CFRunLoopAddTimer")
	purego.RegisterLibFunc(&stop, cf, "CFRunLoopStop")
	pthreadSelf, err := purego.Dlsym(purego.RTLD_DEFAULT, "pthread_self")
	if err != nil {
		return fail(err)
	}
	self := func() uintptr { r, _, _ := purego.SyscallN(pthreadSelf); return r }
	if err := loadNative(); err != nil {
		return fail(err)
	}
	if native.pthreadMainNP() == 0 {
		return fail(fmt.Errorf("the helper process does not run on the main thread"))
	}
	mode := stringCreate(0, "github.com/egoist/mygo/internal/darwin/mainthread.test", utf8)
	addCommonMode(getMain(), mode)
	// A timer that does not fire while the run loop runs keeps it running
	// in the mode until its time ends.
	timer := timerCreate(0, now()+3600, 0, 0, 0, purego.NewCallback(func(timer, info uintptr) {}), 0)
	addTimer(getMain(), timer, mode)

	if err := Start(); err != nil {
		return fail(err)
	}
	main := self()
	var (
		ranOn  uintptr
		result int32
	)
	forwarded := make(chan struct{})
	RunOnWorker(func() {
		looping := make(chan struct{})
		go func() {
			defer close(forwarded)
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			<-looping
			ranOn = Forward[uintptr](pthreadSelf)
			stop(getMain())
		}()
		RunOnMain(func() {
			close(looping)
			result = runInMode(mode, wait.Guard.Seconds(), false)
		})
		<-forwarded
	})
	Stop()
	fmt.Println("the forwarded call ran on the main thread:", ranOn == main)
	fmt.Println("the run loop ended by the stop that follows the call:", result == runLoopStopped)
	return 0
}

// A call that another goroutine forwards while a step runs the main run
// loop on the main thread in a mode of its common modes, as a modal panel
// does, runs on the main thread and returns while that run loop runs.
func TestOtherThreadsForwardWhileTheMainThreadRunsALoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), loopEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for _, w := range []string{
		"the forwarded call ran on the main thread: true",
		"the run loop ended by the stop that follows the call: true",
	} {
		if !strings.Contains(string(out), w+"\n") {
			t.Errorf("no %q in the output:\n%s", w, out)
		}
	}
}
