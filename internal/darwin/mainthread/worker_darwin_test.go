//go:build darwin

package mainthread

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// onFakeMain runs fn on a goroutine locked to a thread of its own, which
// the worker takes for the main thread when fn starts it, and stops the
// worker when fn returns.
func onFakeMain(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := Start(); err != nil {
			t.Error(err)
			return
		}
		defer func() {
			Stop()
			link.mainThread = 0
		}()
		fn()
	}()
	<-done
}

func pthreadSelf(t *testing.T) uintptr { return dlsym(t, libSystem, "pthread_self") }

// The worker runs the steps of the main thread, and the calls that the
// worker and other threads forward run on the main thread.
func TestTheWorkerRunsStepsAndTheMainThreadRunsForwardedCalls(t *testing.T) {
	self := pthreadSelf(t)
	onFakeMain(t, func() {
		main := direct[uintptr](self)
		var worker, forwarded, queued uintptr
		var inStep, onMain bool
		RunOnWorker(func() {
			worker = direct[uintptr](self)
			inStep = IsMainThread()
			forwarded = Forward[uintptr](self)
			got := make(chan uintptr)
			go func() {
				onMain = IsMainThread()
				got <- Forward[uintptr](self)
			}()
			queued = <-got
		})
		if worker == main || worker == 0 {
			t.Errorf("the step ran on thread %#x, the main thread is %#x", worker, main)
		}
		if !inStep {
			t.Error("IsMainThread is false on the worker during a step")
		}
		if onMain {
			t.Error("IsMainThread is true on a goroutine other than the worker")
		}
		if forwarded != main {
			t.Errorf("the call that the worker forwarded ran on %#x, not on the main thread %#x", forwarded, main)
		}
		if queued != main {
			t.Errorf("the call that another goroutine forwarded ran on %#x, not on the main thread %#x", queued, main)
		}
	})
}

// A callback that reaches the main thread through a trampoline runs on the
// worker, as a nested step when the main thread runs a call that the
// worker forwarded, with its arguments and result as they came.
func TestATrampolineHandsCallbacksOfTheMainThreadToTheWorker(t *testing.T) {
	self := pthreadSelf(t)
	var calledOn uintptr
	var nested uintptr
	whoAmI := Callback(purego.NewCallback(func() uintptr {
		calledOn = direct[uintptr](self)
		nested = Forward[uintptr](self)
		return calledOn
	}))
	sum := Callback(purego.NewCallback(func(a, b, c, d, e, f, g, h, i, j, k, l uintptr) uintptr {
		return a + 2*b + 3*c + 4*d + 5*e + 6*f + 7*g + 8*h + 9*i + 10*j + 11*k + 12*l
	}))
	// purego callbacks return no floats.
	scale := Callback(purego.NewCallback(func(x float64, n uintptr, y float64) uintptr { return uintptr(x*float64(n) + 4*y) }))
	if direct[uintptr](whoAmI) != direct[uintptr](self) {
		t.Error("a trampoline off the main thread did not call its target on the calling thread")
	}
	onFakeMain(t, func() {
		main := direct[uintptr](self)
		var worker, answer, total uintptr
		var scaled uintptr
		RunOnWorker(func() {
			worker = direct[uintptr](self)
			answer = Forward[uintptr](whoAmI)
			total = Forward[uintptr](sum, uintptr(1), uintptr(1), uintptr(1), uintptr(1), uintptr(1), uintptr(1), uintptr(1), uintptr(1), uintptr(1), uintptr(2), uintptr(3), uintptr(4))
			scaled = Forward[uintptr](scale, 1.5, uintptr(4), 0.25)
		})
		if calledOn != worker || answer != worker {
			t.Errorf("the callback ran on %#x and answered %#x, the worker is %#x", calledOn, answer, worker)
		}
		if nested != main {
			t.Errorf("the call that the nested step forwarded ran on %#x, not on the main thread %#x", nested, main)
		}
		if total != 146 {
			t.Errorf("the callback of twelve arguments returned %d, want 146", total)
		}
		if scaled != 7 {
			t.Errorf("the callback of floats returned %v, want 7", scaled)
		}
	})
}

// The object that a method returns on the worker outlives the pool of
// the step and lives until the pool of the main thread drains.
func TestTheObjectThatAStepReturnsGoesToThePoolOfTheMainThread(t *testing.T) {
	objc := "/usr/lib/libobjc.A.dylib"
	poolPush, poolPop := dlsym(t, objc, "objc_autoreleasePoolPush"), dlsym(t, objc, "objc_autoreleasePoolPop")
	getClass, selRegister := dlsym(t, objc, "objc_getClass"), dlsym(t, objc, "sel_registerName")
	msgSend := dlsym(t, objc, "objc_msgSend")
	initWeak, loadWeak := dlsym(t, objc, "objc_initWeak"), dlsym(t, objc, "objc_loadWeakRetained")
	cstr := func(s string) *byte { b := append([]byte(s), 0); return &b[0] }
	sel := func(s string) uintptr { return direct[uintptr](selRegister, cstr(s)) }
	nsObject := direct[uintptr](getClass, cstr("NSObject"))
	var weak uintptr
	makeObject := Method(purego.NewCallback(func() uintptr {
		obj := direct[uintptr](msgSend, direct[uintptr](msgSend, nsObject, sel("alloc")), sel("init"))
		direct[uintptr](initWeak, &weak, obj)
		return direct[uintptr](msgSend, obj, sel("autorelease"))
	}), true)
	alive := func() bool {
		obj := direct[uintptr](loadWeak, &weak)
		if obj != 0 {
			direct[uintptr](msgSend, obj, sel("release"))
		}
		return obj != 0
	}
	onFakeMain(t, func() {
		pool := direct[uintptr](poolPush)
		var obj uintptr
		RunOnWorker(func() { obj = Forward[uintptr](makeObject) })
		if obj == 0 || !alive() {
			t.Error("the object that the step returned did not outlive the pool of the step")
		}
		direct[uintptr](poolPop, pool)
		if alive() {
			t.Error("the object that the step returned outlived the pool of the main thread")
		}
	})
}

// The object that a call forwarded through ForwardRetained returns
// autoreleased outlives the pool of the main thread until the caller
// releases it.
func TestForwardRetainedTakesTheCallersReferenceOnTheMainThread(t *testing.T) {
	lib := "/usr/lib/libobjc.A.dylib"
	poolPush, poolPop := dlsym(t, lib, "objc_autoreleasePoolPush"), dlsym(t, lib, "objc_autoreleasePoolPop")
	initWeak, loadWeak, release := dlsym(t, lib, "objc_initWeak"), dlsym(t, lib, "objc_loadWeakRetained"), dlsym(t, lib, "objc_release")
	var weak uintptr
	alive := func() bool {
		obj := direct[uintptr](loadWeak, &weak)
		if obj != 0 {
			direct[uintptr](release, obj)
		}
		return obj != 0
	}
	onFakeMain(t, func() {
		pool := direct[uintptr](poolPush)
		var obj objc.ID
		RunOnWorker(func() {
			obj = SendRetained(objc.ID(objc.GetClass("NSMutableArray")), objc.RegisterName("array"))
		})
		direct[uintptr](initWeak, &weak, obj)
		direct[uintptr](poolPop, pool)
		if !alive() {
			t.Fatal("the object that ForwardRetained returned did not outlive the pool of the main thread")
		}
		direct[uintptr](release, obj)
		if alive() {
			t.Error("the object outlived the caller's release")
		}
	})
}

// A full trampoline table is a panic that names its size and the call
// that asked for one more, never a trampoline reused or a target called
// directly.
func TestAFullTrampolineTablePanicsWithTheCall(t *testing.T) {
	tableMu.Lock()
	used := tableUsed
	tableUsed = trampolines
	tableMu.Unlock()
	defer func() {
		tableMu.Lock()
		tableUsed = used
		tableMu.Unlock()
	}()
	var msg string
	func() {
		defer func() { msg, _ = recover().(string) }()
		Callback(0x10)
	}()
	for _, want := range []string{"all 2048 trampolines are taken", "TestAFullTrampolineTablePanicsWithTheCall", "worker_darwin_test.go"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the panic %q does not say %q", msg, want)
		}
	}
}

// A side that finds the slot of the other side full, or gets back a record
// that it did not hand over, panics with the state of the link, in a
// process of its own here.
func TestABrokenProtocolPanicsWithTheState(t *testing.T) {
	if kind := os.Getenv("MAINTHREAD_TEST_VIOLATION"); kind != "" {
		onFakeMain(t, func() {
			switch kind {
			case "full":
				link.toWorker = 0x1230
			case "wrong":
				RunOnWorker(func() {
					// The worker, once the step ended, finds a call
					// answered that it did not make; the main thread
					// touches the link no more.
					link.toWorker = 0x4561
				})
				select {}
			}
			RunOnWorker(func() {})
		})
		return
	}
	for kind, want := range map[string]string{
		"full":  "mainthread: protocol violation: the main thread handed",
		"wrong": "mainthread: protocol violation: the worker, which handed the main thread no record, got record 0x4560 back",
	} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestABrokenProtocolPanicsWithTheState$")
		cmd.Env = append(os.Environ(), "MAINTHREAD_TEST_VIOLATION="+kind)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("%s: the process went on after the protocol broke:\n%s", kind, out)
			continue
		}
		if !strings.Contains(string(out), want) || !strings.Contains(string(out), "link {") {
			t.Errorf("%s: the process ended without the state:\n%s", kind, out)
		}
	}
}

// Run starts the worker, runs start and finish on it as steps around the
// loop, which runs on the calling thread, and ends the worker; an error
// of start ends it before the loop.
func TestRunStartsAndStopsTheWorkerAroundTheLoop(t *testing.T) {
	self := pthreadSelf(t)
	type ran struct {
		thread uintptr
		main   bool
	}
	where := func() ran { return ran{direct[uintptr](self), IsMainThread()} }
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer func() { link.mainThread = 0 }()
		main := direct[uintptr](self)
		var start, loop, finish ran
		var runningInLoop bool
		err := Run(func() error { start = where(); return nil }, func() {
			loop = where()
			runningInLoop = link.running != 0
		}, func() { finish = where() })
		if err != nil {
			t.Error(err)
		}
		if start.thread == main || !start.main || finish.thread != start.thread || !finish.main {
			t.Errorf("start ran on %+v and finish on %+v, not as steps on the worker; the main thread is %#x", start, finish, main)
		}
		if loop.thread != main || !runningInLoop {
			t.Errorf("the loop ran on %#x (worker running %v), not on the main thread %#x with the worker", loop.thread, runningInLoop, main)
		}
		if link.running != 0 {
			t.Error("the worker runs on after Run returned")
		}

		failed := errors.New("start failed")
		var looped, finished bool
		err = Run(func() error { return failed }, func() { looped = true }, func() { finished = true })
		if err != failed || looped || finished || link.running != 0 {
			t.Errorf("after a failed start Run returned %v, ran the loop %v and finish %v, and left the worker running %v", err, looped, finished, link.running != 0)
		}
	}()
	<-done
}
