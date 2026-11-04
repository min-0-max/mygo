//go:build darwin

package mainthread

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// linkState is what the main thread and the worker share; the assembly of
// each architecture reads its offsets from go_asm.h. The main thread and
// the worker hand records to each other through one slot each: a thread
// that hands a record to the other waits until it gets it back, and
// meanwhile runs only the records that the other hands it, so at most one
// record travels each way.
type linkState struct {
	mainThread   uintptr // the thread identity of the main thread
	workerThread uintptr // the thread identity of the worker
	running      uintptr // 1 while the worker serves steps
	stop         uintptr // set by the step that ends the worker
	depth        uintptr // the steps that the worker runs, nested
	mainSem      uintptr // the Mach semaphore that wakes the main thread
	workerSem    uintptr // the Mach semaphore that wakes the worker
	toMain       uintptr // the record handed to the main thread, | 1 when done
	toWorker     uintptr // the record handed to the worker, | 1 when done
	queue        uintptr // the records that other threads queue for the main thread

	// The C functions that the assembly calls, which the worker resolves
	// before the main thread or the worker reach them.
	semaphoreSignal, semaphoreWait uintptr
	poolPush, poolPop              uintptr
	retain, autorelease            uintptr
	// violation is the C callback that exchange calls when the other side
	// breaks the protocol, which panics with the state.
	violation uintptr
}

var link linkState

// trampolineRecord is the size of the record that a trampoline keeps on
// the stack of the main thread, which keeps the stack aligned to 16.
const trampolineRecord = (unsafe.Sizeof(record{}) + 15) &^ 15

// The C functions of the assembly of each architecture.
func exchange()
func drainQueue()
func trampolineTable()
func trampolineCommon()

// Their addresses, and the identity of the calling thread: a word of the
// thread's registers that no other thread shares, which the trampolines
// read as well.
func exchangeAddress() uintptr
func drainQueueAddress() uintptr
func trampolineAddress(i uintptr) uintptr
func threadPointer() uintptr

// The C functions of the host, in the assembly of each architecture.
func invokeBlock()
func runOnMain()
func invokeBlockAddress() uintptr
func runOnMainAddress() uintptr

// RunOnWorkerAddress returns the address of a C function that takes a
// block that takes no arguments and runs it on the worker as a step when
// the main thread calls it while the worker runs, and at once anywhere
// else. The main thread waits for the step and meanwhile runs the calls
// that the worker forwards to it.
func RunOnWorkerAddress() uintptr { return entry(invokeBlockAddress(), 0) }

// RunOnMainAddress returns the address of a C function that takes a block
// that takes no arguments and runs it on the main thread when the worker
// calls it during a step, and at once anywhere else.
func RunOnMainAddress() uintptr { return runOnMainAddress() }

//go:generate go run gen_trampolines.go

// trampolines is how many trampolines trampolineTable holds; targets and
// targetFlags hold the C function that each one calls and the record flags
// of its steps. A trampoline is made once for each target and flags
// (entry), and each target is a C callback or a method implementation
// that purego made, of which purego makes at most 2000 in a process and
// never frees them (the blocks of one Go function type share their invoke
// function and all blocks one dispose function), or an assembly function
// of this package. 2048 holds every callback that purego can make and the
// assembly functions, so the table runs out only after purego did.
const trampolines = 2048

var (
	targets     [trampolines]uintptr
	targetFlags [trampolines]uintptr
	tableMu     sync.Mutex
	tableUsed   int
	tableIndex  = map[[2]uintptr]uintptr{}
)

// native are the C functions that the worker calls from Go.
var native struct {
	once                sync.Once
	err                 error
	pthreadMainNP       func() int32
	semaphoreCreate     func(task uintptr, sem *uint32, policy, value int32) int32
	semaphoreWait       func(sem uint32) int32
	semaphoreSignal     func(sem uint32) int32
	machTaskSelf        uintptr
	runLoopSourceCreate func(alloc uintptr, order int, ctx unsafe.Pointer) uintptr
	runLoopSourceSignal func(src uintptr)
	runLoopWakeUp       func(rl uintptr)
	runLoopGetMain      func() uintptr
	runLoopAddSource    func(rl, src, mode uintptr)
	runLoopCommonModes  uintptr
	mainLoop, source    uintptr
	dispatchAsyncF      func(queue, context, work uintptr)
	mainQueue           uintptr
	sourceContext       [10]uintptr
	semaphores          []uint32
	semaphoresMu        sync.Mutex

	// The functions with which RegisterClass sets the implementations of
	// the methods of a class.
	classGetInstanceMethod  func(cls uintptr, sel objc.SEL) uintptr
	methodGetImplementation func(m uintptr) uintptr
	methodSetImplementation func(m, imp uintptr) uintptr
}

// loadNative resolves the C functions of the worker.
func loadNative() error {
	native.once.Do(func() { native.err = resolve() })
	return native.err
}

func resolve() error {
	libSystem, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	libObjC, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	sym := func(lib uintptr, name string) uintptr {
		p, e := purego.Dlsym(lib, name)
		if e != nil && err == nil {
			err = e
		}
		return p
	}
	purego.RegisterLibFunc(&native.pthreadMainNP, libSystem, "pthread_main_np")
	purego.RegisterLibFunc(&native.semaphoreCreate, libSystem, "semaphore_create")
	purego.RegisterLibFunc(&native.semaphoreWait, libSystem, "semaphore_wait")
	purego.RegisterLibFunc(&native.semaphoreSignal, libSystem, "semaphore_signal")
	if p := sym(libSystem, "mach_task_self_"); p != 0 {
		native.machTaskSelf = uintptr(uint32(*word(p)))
	}
	purego.RegisterLibFunc(&native.runLoopSourceCreate, cf, "CFRunLoopSourceCreate")
	purego.RegisterLibFunc(&native.runLoopSourceSignal, cf, "CFRunLoopSourceSignal")
	purego.RegisterLibFunc(&native.runLoopWakeUp, cf, "CFRunLoopWakeUp")
	purego.RegisterLibFunc(&native.runLoopGetMain, cf, "CFRunLoopGetMain")
	purego.RegisterLibFunc(&native.runLoopAddSource, cf, "CFRunLoopAddSource")
	purego.RegisterLibFunc(&native.classGetInstanceMethod, libObjC, "class_getInstanceMethod")
	purego.RegisterLibFunc(&native.methodGetImplementation, libObjC, "method_getImplementation")
	purego.RegisterLibFunc(&native.methodSetImplementation, libObjC, "method_setImplementation")
	purego.RegisterLibFunc(&native.dispatchAsyncF, libSystem, "dispatch_async_f")
	native.mainQueue = sym(libSystem, "_dispatch_main_q")
	if p := sym(cf, "kCFRunLoopCommonModes"); p != 0 {
		native.runLoopCommonModes = *word(p)
	}
	link.poolPush = sym(libObjC, "objc_autoreleasePoolPush")
	link.poolPop = sym(libObjC, "objc_autoreleasePoolPop")
	link.retain = sym(libObjC, "objc_retain")
	link.autorelease = sym(libObjC, "objc_autorelease")
	link.semaphoreSignal = sym(libSystem, "semaphore_signal")
	link.semaphoreWait = sym(libSystem, "semaphore_wait")
	link.violation = purego.NewCallback(violation)
	if err != nil {
		return err
	}
	return loadDecisions(cf)
}

// violation panics with the state of a broken protocol: kind 1 when the
// other side handed back done a record (got) other than the one that this
// side handed it (want), kind 2 when the slot of the other side still held
// a record (got) when this side handed it one (want).
func violation(kind, onMain, want, got uintptr) {
	side, other := "worker", "main thread"
	if onMain != 0 {
		side, other = other, side
	}
	what := fmt.Sprintf("the %s handed record %#x to the %s and got record %#x back", side, want, other, got)
	if want == 0 {
		what = fmt.Sprintf("the %s, which handed the %s no record, got record %#x back", side, other, got)
	}
	if kind == 2 {
		what = fmt.Sprintf("the %s handed %#x to the %s, whose slot still held %#x", side, want, other, got)
	}
	panic(fmt.Sprintf("mainthread: protocol violation: %s; link %+v", what, link))
}

func newSemaphore() (uint32, error) {
	var s uint32
	if r := native.semaphoreCreate(native.machTaskSelf, &s, 0, 0); r != 0 {
		return 0, fmt.Errorf("mainthread: semaphore_create: kern_return_t %d", r)
	}
	return s, nil
}

// semaphore returns a Mach semaphore of the pool, which the caller puts
// back with putSemaphore.
func semaphore() uint32 {
	native.semaphoresMu.Lock()
	if n := len(native.semaphores); n > 0 {
		s := native.semaphores[n-1]
		native.semaphores = native.semaphores[:n-1]
		native.semaphoresMu.Unlock()
		return s
	}
	native.semaphoresMu.Unlock()
	s, err := newSemaphore()
	if err != nil {
		panic(err)
	}
	return s
}

func putSemaphore(s uint32) {
	native.semaphoresMu.Lock()
	native.semaphores = append(native.semaphores, s)
	native.semaphoresMu.Unlock()
}

// Start starts the worker. The thread that calls it counts as the main
// thread until Stop: the main thread, before the main run loop runs. From
// then on the trampolines that run on the main thread hand their callbacks
// to the worker as steps, and the calls that Forward forwards run on the
// main thread.
func Start() error {
	if err := loadNative(); err != nil {
		return err
	}
	if atomic.LoadUintptr(&link.running) != 0 {
		return errors.New("mainthread: the worker runs already")
	}
	link.mainThread = threadPointer()
	mainSem, err := newSemaphore()
	if err != nil {
		return err
	}
	workerSem, err := newSemaphore()
	if err != nil {
		return err
	}
	link.mainSem, link.workerSem = uintptr(mainSem), uintptr(workerSem)
	link.stop, link.depth, link.toMain, link.toWorker = 0, 0, 0, 0
	if native.source == 0 {
		native.mainLoop = native.runLoopGetMain()
		native.sourceContext[9] = drainQueueAddress() // perform
		native.source = native.runLoopSourceCreate(0, 0, unsafe.Pointer(&native.sourceContext))
		native.runLoopAddSource(native.mainLoop, native.source, native.runLoopCommonModes)
	}
	ready := make(chan struct{})
	go serve(ready)
	<-ready
	return nil
}

// serve is the worker: a goroutine locked to a thread of its own that
// waits for steps in exchange until the step of Stop ends it.
func serve(ready chan<- struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	link.workerThread = threadPointer()
	atomic.StoreUintptr(&link.running, 1)
	close(ready)
	purego.SyscallN(exchangeAddress(), 0)
	atomic.StoreUintptr(&link.running, 0)
	link.workerThread = 0
	close(stopped)
}

var stopped = make(chan struct{})

// Stop ends the worker, on the main thread, and returns once it ended.
func Stop() {
	if atomic.LoadUintptr(&link.running) == 0 {
		return
	}
	RunOnWorker(func() { link.stop = 1 })
	<-stopped
	stopped = make(chan struct{})
}

// Run runs the Go code of an application around its event loop: it
// starts the worker (Start), runs start on it as a step, runs loop on the
// calling thread, the main thread, runs finish on the worker as another
// step and ends the worker (Stop) before it returns. An error of start
// ends Run before loop and finish.
func Run(start func() error, loop func(), finish func()) error {
	if err := Start(); err != nil {
		return err
	}
	defer Stop()
	var err error
	RunOnWorker(func() { err = start() })
	if err != nil {
		return err
	}
	loop()
	RunOnWorker(finish)
	return nil
}

// mainFuncs holds the functions that RunOnMain runs for threads other than
// the worker and the main thread, by the number that the trampoline of
// their callback takes.
var mainFuncs struct {
	sync.Mutex
	next  uintptr
	m     map[uintptr]func()
	entry uintptr
}

// RunOnMain runs fn on the worker and returns when it returned. The native
// calls that fn forwards run on the main thread, one forwarded call each,
// and a callback that reaches the main thread meanwhile runs on the worker
// as a nested step; no Go code runs on the main thread. On the worker
// during a step it runs fn at once, as part of that step, and on the main
// thread as a step of its own (RunOnWorker). On any other thread the main
// thread starts the step from its run loop, as it runs a callback, and
// runs the calls of fn inside that one callout, so that what they
// autorelease there stays until fn returned. A step that waits for that
// thread without running the main run loop waits for good. While the
// worker does not run, it runs fn at once.
func RunOnMain(fn func()) {
	tp := threadPointer()
	switch {
	case atomic.LoadUintptr(&link.running) == 0 || tp == link.workerThread:
		fn()
	case tp == link.mainThread:
		RunOnWorker(fn)
	default:
		runFromOtherThread(fn)
	}
}

// runFromOtherThread runs fn as RunOnMain does on a thread other than the
// worker and the main thread: it hands the main queue of libdispatch, which
// the main run loop runs, the trampoline of a callback that runs fn, which
// the main thread hands the worker as a step.
func runFromOtherThread(fn func()) {
	mainFuncs.Lock()
	if mainFuncs.entry == 0 {
		mainFuncs.m = map[uintptr]func(){}
		mainFuncs.entry = entry(purego.NewCallback(func(n uintptr) {
			mainFuncs.Lock()
			f := mainFuncs.m[n]
			delete(mainFuncs.m, n)
			mainFuncs.Unlock()
			f()
		}), 0)
	}
	mainFuncs.next++
	n := mainFuncs.next
	done := make(chan struct{})
	mainFuncs.m[n] = func() {
		defer close(done)
		fn()
	}
	mainFuncs.Unlock()
	forwarded.Add(1)
	queued.Add(1)
	native.dispatchAsyncF(native.mainQueue, n, mainFuncs.entry)
	<-done
}

// step is the C function of the steps that RunOnWorker starts.
var (
	stepFn       func()
	stepOnce     sync.Once
	stepCallback uintptr
)

// RunOnWorker runs fn on the worker as a step and returns when it
// returned. The main goroutine calls it on the main thread, which waits on
// its own stack and meanwhile runs the calls that are forwarded to it.
// Called anywhere else, or while the worker does not run, it runs fn at
// once.
func RunOnWorker(fn func()) {
	if atomic.LoadUintptr(&link.running) == 0 || threadPointer() != link.mainThread {
		fn()
		return
	}
	stepOnce.Do(func() {
		stepCallback = purego.NewCallback(func() uintptr {
			f := stepFn
			stepFn = nil
			f()
			return 0
		})
	})
	stepFn = fn
	rec := &record{fn: stepCallback, flags: recordStep}
	purego.SyscallN(exchangeAddress(), uintptr(unsafe.Pointer(rec)))
	runtime.KeepAlive(rec)
}

// IsMainThread reports whether the calling thread counts as the main
// thread: the thread that started the worker, and the worker while it runs
// a step; while the worker does not run, the thread for which
// pthread_main_np answers true.
func IsMainThread() bool {
	if atomic.LoadUintptr(&link.running) == 0 {
		if err := loadNative(); err != nil {
			panic(err)
		}
		return native.pthreadMainNP() != 0
	}
	tp := threadPointer()
	return tp == link.mainThread || (tp == link.workerThread && link.depth > 0)
}

// Stepping reports whether the worker runs a step. On the main thread it
// tells whether the main thread runs inside that step, where it drains no
// autorelease pool of its own until the step returned.
func Stepping() bool {
	return atomic.LoadUintptr(&link.running) != 0 && atomic.LoadUintptr(&link.depth) > 0
}

// Forward runs the native call of fn with args on the main thread and
// returns its result of type R: at once on the main thread, through the
// main thread that waits for the worker's step on the worker, and through
// the main run loop on any other thread. While the worker does not run,
// it runs the call on the calling thread.
func Forward[R any](fn uintptr, args ...any) R {
	rec, keep, out := prepare(fn, reflect.TypeFor[R](), args)
	forward(rec)
	runtime.KeepAlive(keep)
	return result[R](rec, out)
}

// ForwardRetained runs the native call of fn with args on the main thread
// as Forward does, and takes the caller's reference to the object that it
// returns there, inside the forwarded call: an object that the call
// returns autoreleased is otherwise held only by the autorelease pool of
// the main thread, whose drain the caller does not wait for. The caller
// releases the object.
func ForwardRetained[R any](fn uintptr, args ...any) R {
	rec, keep, out := prepare(fn, reflect.TypeFor[R](), args)
	rec.flags |= recordRetainsResult
	forward(rec)
	runtime.KeepAlive(keep)
	return result[R](rec, out)
}

// Send sends the message sel with args to recv on the main thread through
// Forward, and returns its result of type R.
func Send[R any](recv objc.ID, sel objc.SEL, args ...any) R {
	return Forward[R](msgSend(), append([]any{recv, sel}, args...)...)
}

// SendRetained sends the message sel with args to recv on the main thread
// through ForwardRetained, and returns the object that it returns, which
// the caller releases.
func SendRetained(recv objc.ID, sel objc.SEL, args ...any) objc.ID {
	return ForwardRetained[objc.ID](msgSend(), append([]any{recv, sel}, args...)...)
}

// msgSend returns the address of objc_msgSend.
func msgSend() uintptr {
	if err := loadNative(); err != nil {
		panic(err)
	}
	return decisions.msgSend
}

// forwarded counts the calls that the worker and other threads forwarded
// to the main thread.
var forwarded atomic.Uint64

// Forwarded returns how many calls the worker and other threads forwarded
// to the main thread since the process started: a call that runs at once,
// on the main thread or while the worker does not run, is not counted.
func Forwarded() uint64 { return forwarded.Load() }

// queued counts what threads other than the worker and the main thread
// handed the main thread.
var queued atomic.Uint64

// Queued returns how many of the calls that Forwarded counts threads other
// than the worker and the main thread handed the main thread: their
// forwarded calls, and the step of each RunOnMain, whose own calls the
// worker forwards.
func Queued() uint64 { return queued.Load() }

// forward runs rec on the main thread.
func forward(rec *record) {
	tp := threadPointer()
	switch {
	case atomic.LoadUintptr(&link.running) == 0 || tp == link.mainThread:
		run(rec)
	case tp == link.workerThread && link.depth > 0:
		forwarded.Add(1)
		rec = checkedOnMain(rec)
		purego.SyscallN(exchangeAddress(), uintptr(unsafe.Pointer(rec)))
	default:
		forwarded.Add(1)
		queued.Add(1)
		rec = checkedOnMain(rec)
		s := semaphore()
		rec.sem = uintptr(s)
		p := uintptr(unsafe.Pointer(rec))
		for {
			head := atomic.LoadUintptr(&link.queue)
			rec.next = head
			if atomic.CompareAndSwapUintptr(&link.queue, head, p) {
				break
			}
		}
		native.runLoopSourceSignal(native.source)
		native.runLoopWakeUp(native.mainLoop)
		native.semaphoreSignal(uint32(link.mainSem))
		for native.semaphoreWait(s) != 0 {
		}
		putSemaphore(s)
	}
	runtime.KeepAlive(rec)
}

// entry returns the trampoline that calls target with flags, the same for
// each call with the same target and flags.
func entry(target, flags uintptr) uintptr {
	if target == 0 {
		return 0
	}
	tableMu.Lock()
	defer tableMu.Unlock()
	key := [2]uintptr{target, flags}
	if i, ok := tableIndex[key]; ok {
		return trampolineAddress(i)
	}
	if tableUsed == trampolines {
		panic(fmt.Sprintf("mainthread: all %d trampolines are taken; %s asked for one for %#x", trampolines, registeringCall(), target))
	}
	i := uintptr(tableUsed)
	tableUsed++
	targets[i] = target
	targetFlags[i] = flags | recordStep
	tableIndex[key] = i
	return trampolineAddress(i)
}

// registeringCall names the call that asked for a trampoline: the first
// call on the stack from a file that is not a source file of this package
// (a test of the package among them), with the function of this package
// that it called.
func registeringCall() string {
	pcs := make([]uintptr, 32)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Dir(self)
	called := "an unknown function"
	for {
		f, more := frames.Next()
		if filepath.Dir(f.File) != dir || strings.HasSuffix(f.File, "_test.go") {
			return fmt.Sprintf("%s at %s:%d, calling %s,", f.Function, f.File, f.Line, called)
		}
		called = f.Function
		if !more {
			return "an unknown call, calling " + called + ","
		}
	}
}

// isTrampoline reports whether fn is a trampoline of the table.
func isTrampoline(fn uintptr) bool {
	base := trampolineAddress(0)
	return fn >= base && fn < trampolineAddress(trampolines)
}

// Callback returns the trampoline for a C callback that purego made, to
// pass in its place: called on the main thread while the worker runs, it
// runs the callback on the worker as a step.
func Callback(fn uintptr) uintptr { return entry(fn, 0) }

// Method returns the trampoline for the implementation imp of an
// Objective-C method that purego made, to set in its place with
// method_setImplementation. returnsObject says that the method returns an
// object, which the step hands to the autorelease pool of the main thread.
func Method(imp uintptr, returnsObject bool) uintptr {
	if returnsObject {
		return entry(imp, recordReturnsObject)
	}
	return entry(imp, 0)
}
