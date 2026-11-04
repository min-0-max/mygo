//go:build darwin

package darwin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/internal/wait"
)

// wakeEnv makes the test binary the helper process of
// TestStepReturnsWithTheWakeEvent.
const wakeEnv = "MYGO_DARWIN_TEST_WAKE"

// wakeApp records what the event fetch of the helper process of
// TestStepReturnsWithTheWakeEvent returned and what it sent.
var wakeApp struct {
	fetches, wakesFetched, wakesSent int
}

// isWake reports whether ev is the event that Wake posts.
func isWake(ev id) bool {
	return ev != 0 && sendInt(ev, "type") == nsEventTypeApplicationDefined && int16(sendInt(ev, "subtype")) == wakeSubtype
}

// wakeMain is the helper process of TestStepReturnsWithTheWakeEvent. On
// the main thread it runs the shared application, without a window, on
// the worker of the main thread: a step calls Step, and once the main
// thread waits for an event in it, another goroutine calls Wake. The
// application is NSApplication but for the date of its event fetch, which
// is wait.Guard from now, so that only an event ends the fetch before the
// guard, and it records what the fetch returned and what it sent. It
// prints how many fetches returned the wake event and how many sent it.
func wakeMain() int {
	const (
		cfPath               = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
		runLoopBeforeWaiting = 1 << 5 // kCFRunLoopBeforeWaiting
		activationProhibited = 2      // NSApplicationActivationPolicyProhibited
	)
	fail := func(err error) int { fmt.Println(err); return 1 }
	load()
	if !mainthread.IsMainThread() {
		return fail(fmt.Errorf("the helper process does not run on the main thread"))
	}
	cf, err := purego.Dlopen(cfPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return fail(err)
	}
	var (
		observerCreate func(alloc uintptr, activities uint, repeats bool, order int, callout, context uintptr) uintptr
		addObserver    func(rl, observer, mode uintptr)
		getMain        func() uintptr
	)
	purego.RegisterLibFunc(&observerCreate, cf, "CFRunLoopObserverCreate")
	purego.RegisterLibFunc(&addObserver, cf, "CFRunLoopAddObserver")
	purego.RegisterLibFunc(&getMain, cf, "CFRunLoopGetMain")
	defaultMode, err := purego.Dlsym(cf, "kCFRunLoopDefaultMode")
	if err != nil {
		return fail(err)
	}

	fetch := objc.RegisterName("nextEventMatchingMask:untilDate:inMode:dequeue:")
	sendEvent := objc.RegisterName("sendEvent:")
	cls, err := objc.RegisterClass("MyGoTestWakeApp", objc.GetClass("NSApplication"), nil, nil, []objc.MethodDef{
		{Cmd: fetch, Fn: func(self objc.ID, cmd objc.SEL, mask uint64, _ objc.ID, mode objc.ID, dequeue bool) objc.ID {
			guard := msgDateSince(class("NSDate"), sel("dateWithTimeIntervalSinceNow:"), wait.Guard.Seconds())
			ev := sendSuper(id(self), "MyGoTestWakeApp", cmd, uintptr(mask), uintptr(guard), uintptr(mode), boolArg(dequeue))
			wakeApp.fetches++
			if isWake(ev) {
				wakeApp.wakesFetched++
			}
			return objc.ID(ev)
		}},
		{Cmd: sendEvent, Fn: func(self objc.ID, cmd objc.SEL, ev objc.ID) {
			if isWake(id(ev)) {
				wakeApp.wakesSent++
			}
			sendSuper(id(self), "MyGoTestWakeApp", cmd, uintptr(ev))
		}},
	})
	if err != nil {
		return fail(err)
	}
	b := &Backend{app: send(id(cls), "sharedApplication")}
	send(b.app, "setActivationPolicy:", activationProhibited)

	// The main thread runs a run loop only in the event fetch of Step,
	// so the first time it is about to wait is in Step.
	waiting := make(chan struct{})
	var once sync.Once
	observer := observerCreate(0, runLoopBeforeWaiting, true, 0, purego.NewCallback(func(observer, activity, info uintptr) {
		once.Do(func() { close(waiting) })
	}), 0)
	addObserver(getMain(), observer, **(**uintptr)(unsafe.Pointer(&defaultMode))) // a CFStringRef constant

	err = mainthread.Run(func() error {
		woke := make(chan struct{})
		go func() {
			defer close(woke)
			<-waiting
			b.Wake()
		}()
		b.Step()
		<-woke
		return nil
	}, func() {}, func() {})
	if err != nil {
		return fail(err)
	}
	fmt.Println("fetches:", wakeApp.fetches)
	fmt.Println("fetches that returned the wake event:", wakeApp.wakesFetched)
	fmt.Println("wake events sent:", wakeApp.wakesSent)
	return 0
}

// Step, which await calls on the worker, returns with the event that Wake
// posts from another goroutine while the main thread waits for an event
// in it, and does not send that event. The application is the shared
// application, without a window, on the main thread of a helper process;
// the date of its event fetch is wait.Guard, so that Step ends with an
// event and not with its date.
func TestStepReturnsWithTheWakeEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), wakeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	for _, w := range []string{
		"fetches: 1",
		"fetches that returned the wake event: 1",
		"wake events sent: 0",
	} {
		if !strings.Contains(string(out), w+"\n") {
			t.Errorf("no %q in the output:\n%s", w, out)
		}
	}
}
