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
	"time"

	"github.com/ebitengine/purego"
)

// loopEnv makes the test binary the helper process of
// TestOtherThreadsForwardWhileTheMainThreadRunsALoop.
const loopEnv = "MYGO_MAINTHREAD_TEST_LOOP"

func init() {
	if os.Getenv(loopEnv) == "1" {
		// The main goroutine stays on the main thread, whose run loop is
		// the main run loop.
		runtime.LockOSThread()
	}
}

func TestMain(m *testing.M) {
	if os.Getenv(loopEnv) == "1" {
		os.Exit(loopMain())
	}
	os.Exit(m.Run())
}

// loopMain is the helper process of
// TestOtherThreadsForwardWhileTheMainThreadRunsALoop. On the main thread
// it starts the worker, and a step runs, through RunOnMain, the main run
// loop in a mode of its common modes with a timer of its own, as a modal
// panel does, while another goroutine forwards a call; the step waits for
// the goroutine. It prints where that call ran, whether it returned
// before the run loop ended and whether the run loop ran until its time
// ended.
func loopMain() int {
	const (
		cfPath          = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
		utf8            = uint32(0x08000100)
		runLoopTimedOut = 3 // kCFRunLoopRunTimedOut
		loopSeconds     = 1.0
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
	)
	purego.RegisterLibFunc(&runInMode, cf, "CFRunLoopRunInMode")
	purego.RegisterLibFunc(&addCommonMode, cf, "CFRunLoopAddCommonMode")
	purego.RegisterLibFunc(&getMain, cf, "CFRunLoopGetMain")
	purego.RegisterLibFunc(&stringCreate, cf, "CFStringCreateWithCString")
	purego.RegisterLibFunc(&now, cf, "CFAbsoluteTimeGetCurrent")
	purego.RegisterLibFunc(&timerCreate, cf, "CFRunLoopTimerCreate")
	purego.RegisterLibFunc(&addTimer, cf, "CFRunLoopAddTimer")
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
		ranOn             uintptr
		returned, loopEnd time.Time
		result            int32
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
			returned = time.Now()
		}()
		RunOnMain(func() {
			close(looping)
			result = runInMode(mode, loopSeconds, false)
			loopEnd = time.Now()
		})
		<-forwarded
	})
	Stop()
	fmt.Println("the forwarded call ran on the main thread:", ranOn == main)
	fmt.Println("it returned before the run loop ended:", returned.Before(loopEnd))
	fmt.Println("the run loop ran until its time ended:", result == runLoopTimedOut)
	return 0
}

// A call that another goroutine forwards while a step runs the main run
// loop on the main thread in a mode of its common modes, as a modal panel
// does, runs on the main thread and returns before that run loop ends.
func TestOtherThreadsForwardWhileTheMainThreadRunsALoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), loopEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for _, w := range []string{
		"the forwarded call ran on the main thread: true",
		"it returned before the run loop ended: true",
		"the run loop ran until its time ended: true",
	} {
		if !strings.Contains(string(out), w+"\n") {
			t.Errorf("no %q in the output:\n%s", w, out)
		}
	}
}
