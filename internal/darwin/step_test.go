//go:build darwin

package darwin

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/darwin/mainthread"
)

// stepApp is a fake application for Step: its event fetch records the
// thread that runs it and returns an event, and its sendEvent: calls a C
// callback of mygo, as AppKit calls the methods of mygo for an event.
var stepApp struct {
	once                 sync.Once
	app, event, callback uintptr
	pthreadSelf          uintptr
	fetchedOn, sentOn    uintptr
	callbackOn           uintptr
	fetchedCounted       bool
	untilFromNow         float64
}

func makeStepApp(t *testing.T) {
	s := &stepApp
	s.once.Do(func() {
		var err error
		if s.pthreadSelf, err = purego.Dlsym(purego.RTLD_DEFAULT, "pthread_self"); err != nil {
			t.Fatal(err)
		}
		self := func() uintptr { r, _, _ := purego.SyscallN(s.pthreadSelf); return r }
		s.callback = mainthread.Callback(purego.NewCallback(func() { s.callbackOn = self() }))
		event, err := objc.RegisterClass("MyGoTestStepEvent", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
			{Cmd: objc.RegisterName("type"), Fn: func(objc.ID, objc.SEL) uint { return 1 }},
		})
		if err != nil {
			t.Fatal(err)
		}
		app, err := objc.RegisterClass("MyGoTestStepApp", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
			{Cmd: objc.RegisterName("nextEventMatchingMask:untilDate:inMode:dequeue:"), Fn: func(_ objc.ID, _ objc.SEL, _ uint64, until, _ objc.ID, _ bool) objc.ID {
				s.untilFromNow = objc.Send[float64](until, objc.RegisterName("timeIntervalSinceNow"))
				s.fetchedOn = self()
				s.fetchedCounted = mainthread.IsMainThread()
				return objc.ID(s.event)
			}},
			{Cmd: objc.RegisterName("sendEvent:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				s.sentOn = self()
				purego.SyscallN(s.callback)
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		s.event = uintptr(objc.ID(event).Send(objc.RegisterName("new")))
		s.app = uintptr(objc.ID(app).Send(objc.RegisterName("new")))
	})
}

// The Step of await runs its Go on the worker, and the main thread runs
// only the native calls that it forwards, one forwarded call each: the
// push of an autorelease pool of the main thread, the date, the fetch of
// the event, its type, its send and the pop of the pool. A forwarded call
// that the main thread made itself would run at once, uncounted. A
// callback of mygo that the sent event reaches runs on the worker as a
// nested step. The application is a fake one, so no window is needed.
func TestStepRunsItsGoOnTheWorker(t *testing.T) {
	load()
	makeStepApp(t)
	s := &stepApp
	self := func() uintptr { r, _, _ := purego.SyscallN(s.pthreadSelf); return r }
	b := &Backend{app: id(s.app)}
	var n uint64
	var main, worker uintptr
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		main = self()
		err := mainthread.Run(func() (mainthread.Call, error) {
			worker = self()
			before := mainthread.Forwarded()
			b.Step()
			n = mainthread.Forwarded() - before
			return mainthread.Call{}, nil
		}, func() {})
		if err != nil {
			t.Error(err)
		}
	}()
	<-done
	if n != 6 {
		t.Errorf("one Step on the worker forwarded %d calls, want 6, one for each native call", n)
	}
	if s.fetchedOn != main || !s.fetchedCounted || s.sentOn != main {
		t.Errorf("Step fetched the event on %#x (IsMainThread %v) and sent it on %#x, not on the main thread %#x", s.fetchedOn, s.fetchedCounted, s.sentOn, main)
	}
	if s.callbackOn != worker {
		t.Errorf("the callback of the sent event ran on %#x, not on the worker %#x as a nested step", s.callbackOn, worker)
	}
	if year := 365 * 24 * time.Hour; s.untilFromNow < year.Seconds() {
		t.Errorf("Step fetched its event with a date %v s from now, want no date within a year: only an event or Wake ends it", s.untilFromNow)
	}
}
