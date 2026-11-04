//go:build darwin && arm64

package mainstack

import (
	"errors"
	"fmt"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// The native functions of the tests, which they call through purego.
var (
	libSystem, libObjC uintptr

	pthreadSelf, pthreadMainNP                                func() uintptr
	pthreadStackAddrNP, pthreadStackSizeNP                    func(thread uintptr) uintptr
	pthreadCreate                                             func(thread *uintptr, attr uintptr, start uintptr, arg uintptr) int32
	pthreadJoin                                               func(thread uintptr, result uintptr) int32
	pthreadSigmask                                            func(how int32, set *uint32, old *uint32) int32
	threadSuspend, threadGetState, threadResume, usleepSymbol uintptr

	objcInitWeak          func(location *uintptr, obj uintptr) uintptr
	objcLoadWeakRetained  func(location *uintptr) uintptr
	objcDestroyWeak       func(location *uintptr)
	objcSetAssociatedObj  func(obj, key, value uintptr, policy uintptr)
	pthreadMachThreadSelf func(thread uintptr) uint32
	dladdr                func(addr uintptr, info *dlInfo) int32
	msgSendAddress        uintptr
)

type point struct{ X, Y float64 }

// dlInfo is a Dl_info, which dladdr fills.
type dlInfo struct {
	image  *byte
	base   uintptr
	symbol *byte
	addr   uintptr
}

// cString copies a C string.
func cString(p *byte) string {
	var b []byte
	for ; *p != 0; p = (*byte)(unsafe.Add(unsafe.Pointer(p), 1)) {
		b = append(b, *p)
	}
	return string(b)
}

type size struct{ W, H float64 }

type rect struct {
	Origin point
	Size   size
}

func loadNative() error {
	var err error
	if libSystem, err = purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		return err
	}
	if libObjC, err = purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		return err
	}
	if _, err = purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		return err
	}
	purego.RegisterLibFunc(&pthreadSelf, libSystem, "pthread_self")
	purego.RegisterLibFunc(&pthreadMainNP, libSystem, "pthread_main_np")
	purego.RegisterLibFunc(&pthreadMachThreadSelf, libSystem, "pthread_mach_thread_np")
	purego.RegisterLibFunc(&pthreadStackAddrNP, libSystem, "pthread_get_stackaddr_np")
	purego.RegisterLibFunc(&pthreadStackSizeNP, libSystem, "pthread_get_stacksize_np")
	purego.RegisterLibFunc(&pthreadCreate, libSystem, "pthread_create")
	purego.RegisterLibFunc(&pthreadJoin, libSystem, "pthread_join")
	purego.RegisterLibFunc(&pthreadSigmask, libSystem, "pthread_sigmask")
	purego.RegisterLibFunc(&dladdr, libSystem, "dladdr")
	for _, s := range []struct {
		addr *uintptr
		name string
	}{
		{&threadSuspend, "thread_suspend"},
		{&threadGetState, "thread_get_state"},
		{&threadResume, "thread_resume"},
		{&usleepSymbol, "usleep"},
	} {
		if *s.addr, err = purego.Dlsym(libSystem, s.name); err != nil {
			return err
		}
	}
	purego.RegisterLibFunc(&objcInitWeak, libObjC, "objc_initWeak")
	purego.RegisterLibFunc(&objcLoadWeakRetained, libObjC, "objc_loadWeakRetained")
	purego.RegisterLibFunc(&objcDestroyWeak, libObjC, "objc_destroyWeak")
	purego.RegisterLibFunc(&objcSetAssociatedObj, libObjC, "objc_setAssociatedObject")
	msgSendAddress, err = purego.Dlsym(libObjC, "objc_msgSend")
	return err
}

// The sampler's state, a word array whose offsets sampler_darwin_arm64.s
// repeats. Go keeps a package-level array in place, so the sampler thread
// reads and writes it by its address.
const (
	wordPort = iota
	wordLow
	wordHigh
	wordStop
	wordPhase
	wordSuspend
	wordGetState
	wordResume
	wordUsleep
	wordFailures
	wordLastError
	wordCount
	wordSamples
)

const (
	samplerPhases = 16
	wordOutside   = wordSamples + samplerPhases
	wordFirst     = wordOutside + samplerPhases
	wordRegs      = wordFirst + samplerPhases
	// arm_thread_state64_t is 272 bytes.
	wordFirstPC  = wordRegs + 272/8
	samplerWords = wordFirstPC + samplerPhases
)

var (
	samplerState  [samplerWords]uint64
	samplerThread uintptr
	mainThread    uintptr
)

// captureMainThread records the calling thread, which must be the main
// thread, as the thread that the sampler samples, and its stack.
func captureMainThread() error {
	if pthreadMainNP() == 0 {
		return errors.New("the calling thread is not the main thread")
	}
	mainThread = pthreadSelf()
	high := pthreadStackAddrNP(mainThread)
	samplerState[wordPort] = uint64(pthreadMachThreadSelf(mainThread))
	samplerState[wordHigh] = uint64(high)
	samplerState[wordLow] = uint64(high - pthreadStackSizeNP(mainThread))
	return nil
}

// stackBounds returns the stack of the main thread.
func stackBounds() (low, high uintptr) {
	return uintptr(samplerState[wordLow]), uintptr(samplerState[wordHigh])
}

// startSampler starts the thread that samples the main thread while a
// phase is set.
func startSampler() error {
	setPhase(-1)
	samplerState[wordSuspend] = uint64(threadSuspend)
	samplerState[wordGetState] = uint64(threadGetState)
	samplerState[wordResume] = uint64(threadResume)
	samplerState[wordUsleep] = uint64(usleepSymbol)
	if r := pthreadCreate(&samplerThread, 0, samplerLoopAddress(), uintptr(unsafe.Pointer(&samplerState))); r != 0 {
		return fmt.Errorf("pthread_create: %w", syscall.Errno(r))
	}
	return nil
}

// setPhase has the sampler count its samples for phase, or take none for
// -1.
func setPhase(phase int) {
	atomic.StoreUint64(&samplerState[wordPhase], uint64(int64(phase)))
}

// phaseSamples returns how many samples the sampler took for phase so far.
func phaseSamples(phase int) uint64 {
	return atomic.LoadUint64(&samplerState[wordSamples+phase])
}

// sampleCount is what the sampler counted while one phase was set, with
// the stack pointer and the program counter of the first sample outside.
type sampleCount struct {
	samples, outside      uint64
	firstOutside, firstPC uintptr
}

// stopSampler ends the sampler thread and returns its counts, and the
// failures of its calls with the last error.
func stopSampler() (counts [samplerPhases]sampleCount, failures uint64, lastError int64, err error) {
	setPhase(-1)
	atomic.StoreUint64(&samplerState[wordStop], 1)
	if r := pthreadJoin(samplerThread, 0); r != 0 {
		return counts, 0, 0, fmt.Errorf("pthread_join: %w", syscall.Errno(r))
	}
	for i := range counts {
		counts[i] = sampleCount{
			samples:      atomic.LoadUint64(&samplerState[wordSamples+i]),
			outside:      atomic.LoadUint64(&samplerState[wordOutside+i]),
			firstOutside: uintptr(atomic.LoadUint64(&samplerState[wordFirst+i])),
			firstPC:      uintptr(atomic.LoadUint64(&samplerState[wordFirstPC+i])),
		}
	}
	return counts, atomic.LoadUint64(&samplerState[wordFailures]), int64(atomic.LoadUint64(&samplerState[wordLastError])), nil
}

// blockAsyncSignals blocks every signal on the main thread except the
// synchronous faults, which reach the thread that raises them whether it
// blocks them or not, as a host blocks them before the app runs.
func blockAsyncSignals() error {
	if pthreadMainNP() == 0 {
		return errors.New("block asynchronous signals: the calling thread is not the main thread")
	}
	set := ^uint32(0)
	for _, s := range []syscall.Signal{syscall.SIGILL, syscall.SIGTRAP, syscall.SIGABRT, syscall.SIGEMT, syscall.SIGFPE, syscall.SIGBUS, syscall.SIGSEGV, syscall.SIGSYS} {
		set &^= 1 << (uint(s) - 1)
	}
	const sigBlock = 1
	if r := pthreadSigmask(sigBlock, &set, nil); r != 0 {
		return fmt.Errorf("block asynchronous signals: pthread_sigmask: %w", syscall.Errno(r))
	}
	return nil
}

// Objective-C helpers of the tests.

func sel(name string) objc.SEL { return objc.RegisterName(name) }

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

func isKind(obj objc.ID, className string) bool {
	return obj != 0 && objc.Send[bool](obj, sel("isKindOfClass:"), objc.GetClass(className))
}

// findView returns the first view in the tree of v that is of class
// className. In a step: the arrays run their messages on the calling
// thread.
func findView(v objc.ID, className string) objc.ID {
	if isKind(v, className) {
		return v
	}
	subviews := mainSend[objc.ID](v, sel("subviews"))
	for i := range objc.Send[int](subviews, sel("count")) {
		if found := findView(subviews.Send(sel("objectAtIndex:"), i), className); found != 0 {
			return found
		}
	}
	return 0
}

// findLayer returns the first layer in the tree of l that is of class
// className. In a step.
func findLayer(l objc.ID, className string) objc.ID {
	if isKind(l, className) {
		return l
	}
	sublayers := mainSend[objc.ID](l, sel("sublayers"))
	if sublayers == 0 {
		return 0
	}
	for i := range objc.Send[int](sublayers, sel("count")) {
		if found := findLayer(sublayers.Send(sel("objectAtIndex:"), i), className); found != 0 {
			return found
		}
	}
	return 0
}
