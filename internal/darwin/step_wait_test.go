//go:build darwin

package darwin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/internal/platform"
	"github.com/egoist/mygo/internal/wait"
)

// stepWaitEnv makes the test binary the helper process of
// TestWhatEndsTheWaitOfAStep.
const stepWaitEnv = "MYGO_DARWIN_TEST_STEP_WAIT"

// stepWaitHandler is the application handler of the helper process: its
// Dispatch is the one that the run loop source of Signal calls.
type stepWaitHandler struct {
	platform.AppHandler
	dispatch func()
}

func (h stepWaitHandler) Dispatch() { h.dispatch() }

// stepWaitMain is the helper process of TestWhatEndsTheWaitOfAStep. It runs
// the shared application, without a window, on the worker of the main
// thread, as a step: each Step of it waits with the date that Step gives
// its event fetch, while another goroutine asks for what a frame, a posted
// function or a timer needs.
func stepWaitMain() int {
	const (
		cfPath               = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
		activationProhibited = 2 // NSApplicationActivationPolicyProhibited
	)
	fail := func(err error) int { fmt.Println(err); return 1 }
	load()
	cf, err := purego.Dlopen(cfPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return fail(err)
	}
	var (
		absolute    func() float64
		timerCreate func(alloc uintptr, fire, interval float64, flags uint, order int, callout, context uintptr) uintptr
		addTimer    func(rl, timer, mode uintptr)
	)
	purego.RegisterLibFunc(&absolute, cf, "CFAbsoluteTimeGetCurrent")
	purego.RegisterLibFunc(&timerCreate, cf, "CFRunLoopTimerCreate")
	purego.RegisterLibFunc(&addTimer, cf, "CFRunLoopAddTimer")
	commonModes, err := purego.Dlsym(cf, "kCFRunLoopCommonModes")
	if err != nil {
		return fail(err)
	}

	b := &Backend{app: send(class("NSApplication"), "sharedApplication")}
	send(b.app, "setActivationPolicy:", activationProhibited)
	theBackend = b
	dispatched := func() {}
	b.h = stepWaitHandler{dispatch: func() { dispatched() }}
	b.mainLoop = cfRunLoopGetMain()
	var ctx [10]uintptr // CFRunLoopSourceContext; perform is the last field
	ctx[9] = dispatchCallback
	b.source = cfRunLoopSourceCreate(0, 0, unsafe.Pointer(&ctx[0]))
	cfRunLoopAddSource(b.mainLoop, b.source, kCFRunLoopCommonModes)

	soon := func(fn func()) { time.AfterFunc(100*time.Millisecond, fn) }
	var timerFired bool
	timerCallback := mainthread.Callback(purego.NewCallback(func(timer, info uintptr) uintptr {
		timerFired = true
		b.Wake()
		return 0
	}))
	err = mainthread.Run(func() (mainthread.Call, error) {
		// Nothing to do: only Wake ends the wait.
		began := time.Now()
		time.AfterFunc(1200*time.Millisecond, b.Wake)
		b.Step()
		fmt.Println("the wait with nothing to do lasted until the wake:", time.Since(began) >= time.Second)

		// A function that a goroutine queues, as App.Dispatch does, runs in the wait.
		var ran bool
		dispatched = func() { ran = true; b.Wake() }
		soon(b.Signal)
		b.Step()
		fmt.Println("a signal ran the queue in the wait:", ran)
		dispatched = func() {}

		// A frame that drawSoon asks for runs on the same queue of the main thread.
		var drawn bool
		soon(func() { b.post(func() { drawn = true; b.Wake() }) })
		b.Step()
		fmt.Println("a posted frame ran in the wait:", drawn)

		// A timer added while the main thread waits fires in the wait.
		soon(func() {
			addTimer(b.mainLoop, timerCreate(0, absolute()+0.1, 0, 0, 0, timerCallback, 0), **(**uintptr)(unsafe.Pointer(&commonModes)))
		})
		b.Step()
		fmt.Println("a timer added in the wait fired in it:", timerFired)
		return mainthread.Call{}, nil
	}, func() {})
	if err != nil {
		return fail(err)
	}
	return 0
}

// Step waits with no date: what the main thread has to do ends or runs in
// the wait by itself, so an application that waits in Step wakes for nothing.
// The application is the shared application, without a window, on the main
// thread of a helper process; a date that a Step would wait for fails the
// first line, and a wake that one of these lost ends the process at the limit.
func TestWhatEndsTheWaitOfAStep(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), stepWaitEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for _, w := range []string{
		"the wait with nothing to do lasted until the wake: true",
		"a signal ran the queue in the wait: true",
		"a posted frame ran in the wait: true",
		"a timer added in the wait fired in it: true",
	} {
		if !strings.Contains(string(out), w+"\n") {
			t.Errorf("no %q in the output:\n%s", w, out)
		}
	}
}
