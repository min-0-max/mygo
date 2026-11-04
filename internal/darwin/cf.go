//go:build darwin

package darwin

import (
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/egoist/mygo/internal/darwin/mainthread"
)

// The functions bound below run on the calling thread, as
// mainthread.CallingThreadFunctions lets them; those called through an
// address (…Fn) run on the main thread (mainthread.Forward), or by their
// receiver (mainthread.ByReceiver) for CFRelease.
var (
	cfRunLoopGetMain       func() uintptr
	cfRunLoopAddSource     func(rl, src, mode uintptr)
	kCFRunLoopCommonModes  uintptr
	cfErrorCopyDescription func(err id) uintptr
	cfDataGetLength        func(data uintptr) int
	cfDataGetBytePtr       func(data uintptr) unsafe.Pointer

	cfRunLoopSourceCreateFn, cfReleaseFn uintptr

	// LaunchServices.
	lsSetDefaultHandlerForURLSchemeFn, lsCopyDefaultHandlerForURLSchemeFn uintptr

	// Power.
	iopsCopyPowerSourcesInfoFn, iopsGetProvidingPowerSourceTypeFn uintptr
	cgEventSourceSecondsSinceLastType                             func(state int32, eventType uint32) float64

	// cgEventGetLocation is CGEventGetLocation: where an event happened,
	// in the display space of Core Graphics.
	cgEventGetLocation func(event uintptr) NSPoint

	cgDisplayIsBuiltin func(display uint32) bool
	cgDisplayRotation  func(display uint32) float64
	nsBeepFn           uintptr

	// Carbon hot keys.
	getApplicationEventTargetFn, registerEventHotKeyFn, unregisterEventHotKeyFn uintptr
	installEventHandlerFn, getEventParameterFn                                  uintptr
)

func cfRunLoopSourceCreate(alloc uintptr, order int, ctx unsafe.Pointer) uintptr {
	return mainthread.Forward[uintptr](cfRunLoopSourceCreateFn, alloc, order, ctx)
}

func cfRelease(obj uintptr) { mainthread.ByReceiver(cfReleaseFn, obj) }

// cfDataBytes copies the contents of a CFData on the calling thread, an
// empty slice for none.
func cfDataBytes(data uintptr) []byte {
	n := cfDataGetLength(data)
	if n == 0 {
		return []byte{}
	}
	return append([]byte(nil), unsafe.Slice((*byte)(cfDataGetBytePtr(data)), n)...)
}

func lsSetDefaultHandlerForURLScheme(scheme, bundleID id) int32 {
	return mainthread.Forward[int32](lsSetDefaultHandlerForURLSchemeFn, scheme, bundleID)
}

func lsCopyDefaultHandlerForURLScheme(scheme id) id {
	return mainthread.Forward[id](lsCopyDefaultHandlerForURLSchemeFn, scheme)
}

func iopsCopyPowerSourcesInfo() uintptr {
	return mainthread.Forward[uintptr](iopsCopyPowerSourcesInfoFn)
}

func iopsGetProvidingPowerSourceType(snapshot uintptr) id {
	return mainthread.Forward[id](iopsGetProvidingPowerSourceTypeFn, snapshot)
}

func nsBeep() { mainthread.Forward[struct{}](nsBeepFn) }

func getApplicationEventTarget() uintptr {
	return mainthread.Forward[uintptr](getApplicationEventTargetFn)
}

func registerEventHotKey(keyCode, modifiers uint32, hotKeyID eventHotKeyID, target uintptr, options uint32, outRef *uintptr) int32 {
	return mainthread.Forward[int32](registerEventHotKeyFn, keyCode, modifiers, hotKeyID, target, options, outRef)
}

func unregisterEventHotKey(ref uintptr) int32 {
	return mainthread.Forward[int32](unregisterEventHotKeyFn, ref)
}

func installEventHandler(target, handler uintptr, numTypes uint32, list *eventTypeSpec, userData uintptr, outRef *uintptr) int32 {
	return mainthread.Forward[int32](installEventHandlerFn, target, handler, numTypes, list, userData, outRef)
}

func getEventParameter(event uintptr, name, typ uint32, outType uintptr, size uintptr, outSize uintptr, data unsafe.Pointer) int32 {
	return mainthread.Forward[int32](getEventParameterFn, event, name, typ, outType, size, outSize, data)
}

// Called by address on every Signal, like the hot functions of objc.go.
var cfRunLoopSourceSignalFn, cfRunLoopWakeUpFn uintptr

type eventHotKeyID struct {
	Signature uint32
	ID        uint32
}

type eventTypeSpec struct {
	Class uint32
	Kind  uint32
}

func loadCF() {
	purego.RegisterLibFunc(&cfRunLoopGetMain, libCF, "CFRunLoopGetMain")
	cfRunLoopSourceCreateFn = mustDlsym(libCF, "CFRunLoopSourceCreate")
	purego.RegisterLibFunc(&cfRunLoopAddSource, libCF, "CFRunLoopAddSource")
	purego.RegisterLibFunc(&cfErrorCopyDescription, libCF, "CFErrorCopyDescription")
	purego.RegisterLibFunc(&cfDataGetLength, libCF, "CFDataGetLength")
	purego.RegisterLibFunc(&cfDataGetBytePtr, libCF, "CFDataGetBytePtr")
	cfRunLoopSourceSignalFn = mustDlsym(libCF, "CFRunLoopSourceSignal")
	cfRunLoopWakeUpFn = mustDlsym(libCF, "CFRunLoopWakeUp")
	cfReleaseFn = mustDlsym(libCF, "CFRelease")
	p, err := purego.Dlsym(libCF, "kCFRunLoopCommonModes")
	if err != nil {
		panic(err)
	}
	kCFRunLoopCommonModes = **(**uintptr)(unsafe.Pointer(&p))

	lsSetDefaultHandlerForURLSchemeFn = mustDlsym(libCoreServices, "LSSetDefaultHandlerForURLScheme")
	lsCopyDefaultHandlerForURLSchemeFn = mustDlsym(libCoreServices, "LSCopyDefaultHandlerForURLScheme")

	if iokit, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err == nil {
		iopsCopyPowerSourcesInfoFn = mustDlsym(iokit, "IOPSCopyPowerSourcesInfo")
		iopsGetProvidingPowerSourceTypeFn = mustDlsym(iokit, "IOPSGetProvidingPowerSourceType")
	}
	purego.RegisterLibFunc(&cgEventSourceSecondsSinceLastType, libCG, "CGEventSourceSecondsSinceLastEventType")

	purego.RegisterLibFunc(&cgEventGetLocation, libCG, "CGEventGetLocation")
	purego.RegisterLibFunc(&cgDisplayIsBuiltin, libCG, "CGDisplayIsBuiltin")
	purego.RegisterLibFunc(&cgDisplayRotation, libCG, "CGDisplayRotation")
	nsBeepFn = mustDlsym(libAppKit, "NSBeep")

	getApplicationEventTargetFn = mustDlsym(libCarbon, "GetApplicationEventTarget")
	registerEventHotKeyFn = mustDlsym(libCarbon, "RegisterEventHotKey")
	unregisterEventHotKeyFn = mustDlsym(libCarbon, "UnregisterEventHotKey")
	installEventHandlerFn = mustDlsym(libCarbon, "InstallEventHandler")
	getEventParameterFn = mustDlsym(libCarbon, "GetEventParameter")
}

// constString reads an exported NSString constant such as
// NSPasteboardTypeString.
func constString(lib uintptr, name string) id {
	p, err := purego.Dlsym(lib, name)
	if err != nil || p == 0 {
		return 0
	}
	return **(**id)(unsafe.Pointer(&p))
}
