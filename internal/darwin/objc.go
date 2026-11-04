//go:build darwin

// Package darwin implements the macOS backend (AppKit + WKWebView) in pure
// Go: Objective-C is driven through the runtime with purego, no cgo.
package darwin

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/darwin/mainthread"
)

type id = objc.ID

// NSPoint, NSSize and NSRect mirror the CoreGraphics structs (64-bit).
type NSPoint struct{ X, Y float64 }
type NSSize struct{ Width, Height float64 }
type NSRect struct {
	Origin NSPoint
	Size   NSSize
}

// caFrameRateRange mirrors Core Animation's CAFrameRateRange.
type caFrameRateRange struct{ Minimum, Maximum, Preferred float32 }

var (
	msgSendAddr      uintptr
	msgSendSuperAddr uintptr

	// msgSendStretAddr is the entry point of the messages that return a
	// struct larger than 16 bytes, which is another one on amd64.
	msgSendStretAddr uintptr

	// _Block_copy and _Block_release, which go through
	// mainthread.ByReceiver.
	blockCopyFn, blockReleaseFn uintptr

	// Functions on hot paths, called by address: RegisterFunc's wrappers
	// use reflection and allocate on every call.
	poolPushFn, poolPopFn uintptr
)

// The messages that take or return floats or structs, which run on the
// main thread (mainthread.Forward).
func msgRect(obj id, sel objc.SEL) NSRect {
	return mainthread.Forward[NSRect](msgSendStretAddr, obj, sel)
}
func msgSetRect(obj id, sel objc.SEL, r NSRect) {
	mainthread.Forward[struct{}](msgSendAddr, obj, sel, r)
}
func msgSetRectBool(obj id, sel objc.SEL, r NSRect, b bool) {
	mainthread.Forward[struct{}](msgSendAddr, obj, sel, r, b)
}
func msgRectForRect(obj id, sel objc.SEL, r NSRect, style uint) NSRect {
	return mainthread.Forward[NSRect](msgSendStretAddr, obj, sel, r, style)
}
func msgRectToRect(obj id, sel objc.SEL, r NSRect) NSRect {
	return mainthread.Forward[NSRect](msgSendStretAddr, obj, sel, r)
}
func msgFloat(obj id, sel objc.SEL) float64 {
	return mainthread.Forward[float64](msgSendAddr, obj, sel)
}
func msgSetFloat(obj id, sel objc.SEL, v float64) {
	mainthread.Forward[struct{}](msgSendAddr, obj, sel, v)
}
func msgSize(obj id, sel objc.SEL) NSSize { return mainthread.Forward[NSSize](msgSendAddr, obj, sel) }
func msgSetSize(obj id, sel objc.SEL, s NSSize) {
	mainthread.Forward[struct{}](msgSendAddr, obj, sel, s)
}
func msgSuperSetSize(sup uintptr, sel objc.SEL, s NSSize) {
	mainthread.Forward[struct{}](msgSendSuperAddr, sup, sel, s)
}
func msgSuperPoint(sup uintptr, sel objc.SEL, p NSPoint) id {
	return mainthread.Forward[id](msgSendSuperAddr, sup, sel, p)
}
func msgPoint(obj id, sel objc.SEL) NSPoint {
	return mainthread.Forward[NSPoint](msgSendAddr, obj, sel)
}
func msgSetPoint(obj id, sel objc.SEL, p NSPoint) {
	mainthread.Forward[struct{}](msgSendAddr, obj, sel, p)
}
func msgInitRect(obj id, sel objc.SEL, r NSRect) id {
	return mainthread.Forward[id](msgSendAddr, obj, sel, r)
}
func msgInitRectID(obj id, sel objc.SEL, r NSRect, a id) id {
	return mainthread.Forward[id](msgSendAddr, obj, sel, r, a)
}
func msgRectIDUint(obj id, sel objc.SEL, r NSRect, a id, n uint) {
	mainthread.Forward[struct{}](msgSendAddr, obj, sel, r, a, n)
}
func msgInitIDPoint(obj id, sel objc.SEL, a id, p NSPoint) id {
	return mainthread.Forward[id](msgSendAddr, obj, sel, a, p)
}
func msgInitWindow(obj id, sel objc.SEL, r NSRect, style uint, backing uint, deferFlag bool) id {
	return mainthread.Forward[id](msgSendAddr, obj, sel, r, style, backing, deferFlag)
}
func msgColor(cls id, sel objc.SEL, r, g, b, a float64) id {
	return mainthread.Forward[id](msgSendAddr, cls, sel, r, g, b, a)
}
func msgOtherEvent(cls id, sel objc.SEL, typ uint, loc NSPoint, flags uint, ts float64, wn int, ctx id, subtype int16, d1, d2 int) id {
	return mainthread.Forward[id](msgSendAddr, cls, sel, typ, loc, flags, ts, wn, ctx, subtype, d1, d2)
}
func msgPopUpMenu(menu id, sel objc.SEL, item id, loc NSPoint, view id) bool {
	return mainthread.Forward[bool](msgSendAddr, menu, sel, item, loc, view)
}
func msgNextEvent(app id, sel objc.SEL, mask uint64, until id, mode id, dequeue bool) id {
	return mainthread.Forward[id](msgSendAddr, app, sel, mask, until, mode, dequeue)
}
func msgPointFromView(obj id, sel objc.SEL, p NSPoint, view id) NSPoint {
	return mainthread.Forward[NSPoint](msgSendAddr, obj, sel, p, view)
}
func msgDateSince(cls id, sel objc.SEL, seconds float64) id {
	return mainthread.Forward[id](msgSendAddr, cls, sel, seconds)
}
func msgFloatID(obj id, sel objc.SEL, v float64) id {
	return mainthread.Forward[id](msgSendAddr, obj, sel, v)
}
func msgFloat2ID(obj id, sel objc.SEL, a, b float64) id {
	return mainthread.Forward[id](msgSendAddr, obj, sel, a, b)
}
func msgFontFamily(obj id, sel objc.SEL, family id, traits uint, weight int, size float64) id {
	return mainthread.Forward[id](msgSendAddr, obj, sel, family, traits, weight, size)
}
func msgMouseEvent(cls id, sel objc.SEL, typ uint, loc NSPoint, flags uint, ts float64, wn int, ctx id, eventNumber int, clickCount int, pressure float32) id {
	return mainthread.Forward[id](msgSendAddr, cls, sel, typ, loc, flags, ts, wn, ctx, eventNumber, clickCount, pressure)
}
func msgKeyEvent(cls id, sel objc.SEL, typ uint, loc NSPoint, flags uint, ts float64, wn int, ctx id, chars, charsIgnoring id, repeat bool, keyCode uint16) id {
	return mainthread.Forward[id](msgSendAddr, cls, sel, typ, loc, flags, ts, wn, ctx, chars, charsIgnoring, repeat, keyCode)
}
func msgDrawImage(obj id, sel objc.SEL, dst, src NSRect, op uint, fraction float64) {
	mainthread.Forward[struct{}](msgSendAddr, obj, sel, dst, src, op, fraction)
}
func msgInitBitmap(obj id, sel objc.SEL, planes uintptr, width, height, bitsPerSample, samplesPerPixel int, alpha, planar bool, colorSpace id, bytesPerRow, bitsPerPixel int) id {
	return mainthread.Forward[id](msgSendAddr, obj, sel, planes, width, height, bitsPerSample, samplesPerPixel, alpha, planar, colorSpace, bytesPerRow, bitsPerPixel)
}
func msgSetRateRange(obj id, sel objc.SEL, r caFrameRateRange) {
	mainthread.Forward[struct{}](msgSendAddr, obj, sel, r)
}

func mustDlsym(lib uintptr, name string) uintptr {
	p, err := purego.Dlsym(lib, name)
	if err != nil {
		panic(fmt.Sprintf("mygo: %v", err))
	}
	return p
}

func mustDlopen(path string) uintptr {
	h, err := purego.Dlopen(path, purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		panic(fmt.Sprintf("mygo: cannot load %s: %v", path, err))
	}
	return h
}

var (
	libObjC, libCF, libCG, libAppKit, libWebKit, libFoundation, libCarbon uintptr
	libCoreServices                                                       uintptr
	loadOnce                                                              sync.Once
)

// load opens the frameworks and prepares the typed message functions.
func load() {
	loadOnce.Do(func() {
		libObjC = mustDlopen("/usr/lib/libobjc.A.dylib")
		libFoundation = mustDlopen("/System/Library/Frameworks/Foundation.framework/Foundation")
		libCF = mustDlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
		libCG = mustDlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics")
		libAppKit = mustDlopen("/System/Library/Frameworks/AppKit.framework/AppKit")
		libWebKit = mustDlopen("/System/Library/Frameworks/WebKit.framework/WebKit")
		libCarbon = mustDlopen("/System/Library/Frameworks/Carbon.framework/Carbon")
		libCoreServices = mustDlopen("/System/Library/Frameworks/CoreServices.framework/CoreServices")
		// Optional frameworks, loaded so their classes are registered.
		_, _ = purego.Dlopen("/System/Library/Frameworks/UniformTypeIdentifiers.framework/UniformTypeIdentifiers", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		_, _ = purego.Dlopen("/System/Library/Frameworks/UserNotifications.framework/UserNotifications", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		_, _ = purego.Dlopen("/System/Library/Frameworks/ServiceManagement.framework/ServiceManagement", purego.RTLD_GLOBAL|purego.RTLD_NOW)

		var err error
		msgSendAddr, err = purego.Dlsym(libObjC, "objc_msgSend")
		if err != nil {
			panic(err)
		}
		// Methods returning structs larger than 16 bytes use a different
		// entry point on amd64.
		msgSendStretAddr = msgSendAddr
		if runtime.GOARCH == "amd64" {
			msgSendStretAddr, err = purego.Dlsym(libObjC, "objc_msgSend_stret")
			if err != nil {
				panic(err)
			}
		}
		msgSendSuperAddr, err = purego.Dlsym(libObjC, "objc_msgSendSuper")
		if err != nil {
			panic(err)
		}

		poolPushFn = mustDlsym(libObjC, "objc_autoreleasePoolPush")
		poolPopFn = mustDlsym(libObjC, "objc_autoreleasePoolPop")
		blockCopyFn = mustDlsym(libObjC, "_Block_copy")
		blockReleaseFn = mustDlsym(libObjC, "_Block_release")
		loadCF()
	})
}

var (
	selMu      sync.RWMutex
	selCache   = map[string]objc.SEL{}
	clsCache   = map[string]id{}
	superCache = map[string]id{}
)

// sel returns the selector for name, cached.
func sel(name string) objc.SEL {
	selMu.RLock()
	s, ok := selCache[name]
	selMu.RUnlock()
	if ok {
		return s
	}
	s = objc.RegisterName(name)
	selMu.Lock()
	selCache[name] = s
	selMu.Unlock()
	return s
}

// class returns the class object for name, cached. It panics for classes
// that do not exist since that is a programming error.
func class(name string) id {
	selMu.RLock()
	c, ok := clsCache[name]
	selMu.RUnlock()
	if ok {
		return c
	}
	c = id(objc.GetClass(name))
	if c == 0 {
		panic("mygo: Objective-C class " + name + " not found")
	}
	selMu.Lock()
	clsCache[name] = c
	selMu.Unlock()
	return c
}

// hasClass reports whether a class exists (for APIs of newer macOS versions).
func hasClass(name string) bool { return objc.GetClass(name) != 0 }

// send sends a message whose arguments are integers or pointers on the
// main thread (mainthread.Send). Go pointers converted in the call, as in
// uintptr(unsafe.Pointer(&x)), are moved to the heap and kept alive until
// it returns (go:uintptrescapes): otherwise stack growth could move x
// while Objective-C uses it.
//
//go:uintptrescapes
func send(obj id, selector string, args ...uintptr) id {
	return sendSel(obj, sel(selector), args...)
}

//go:uintptrescapes
func sendSel(obj id, s objc.SEL, args ...uintptr) id {
	rest := make([]any, len(args))
	for i, a := range args {
		rest[i] = a
	}
	return mainthread.Send[id](obj, s, rest...)
}

// sendRetained sends a message as send does and takes, on the main
// thread, the caller's reference to the object that it returns, which the
// caller releases (mainthread.SendRetained): the autorelease pool of the
// main thread may otherwise drain it before the caller retains it.
//
//go:uintptrescapes
func sendRetained(obj id, selector string, args ...uintptr) id {
	rest := make([]any, len(args))
	for i, a := range args {
		rest[i] = a
	}
	return mainthread.SendRetained(obj, sel(selector), rest...)
}

// sendHere sends a message whose arguments are integers or pointers on
// the calling thread: a message that mainthread.CallingThreadReceivers
// runs there, to a value object, a collection that mygo made or any
// object for retain and respondsToSelector:.
//
//go:uintptrescapes
func sendHere(obj id, selector string, args ...uintptr) id {
	var a [12]uintptr
	a[0], a[1] = uintptr(obj), uintptr(sel(selector))
	n := copy(a[2:], args)
	r, _, _ := purego.SyscallN(msgSendAddr, a[:n+2]...)
	return id(r)
}

// byReceiver sends release, autorelease or commit through
// mainthread.ByReceiver, which runs it on the calling thread or on the
// main thread by the class of obj.
func byReceiver(obj id, selector string) id {
	return id(mainthread.ByReceiver(msgSendAddr, uintptr(obj), uintptr(sel(selector))))
}

// sendBool sends a message returning BOOL.
//
//go:uintptrescapes
func sendBool(obj id, selector string, args ...uintptr) bool {
	return byte(send(obj, selector, args...)) != 0
}

// sendInt sends a message returning NSInteger.
//
//go:uintptrescapes
func sendInt(obj id, selector string, args ...uintptr) int {
	return int(send(obj, selector, args...))
}

// objcSuper mirrors struct objc_super.
type objcSuper struct {
	receiver   id
	superClass id
}

// superOf returns the struct objc_super that calls the superclass of
// className with self, pinned until pin is unpinned: passed as an integer,
// it must stay off the stack, which may move. The superclass is resolved
// from className rather than from the object's class, which the runtime may
// replace with a generated subclass (key-value observing does): resolving
// "super" from it would call the override again and recurse forever.
func superOf(self id, className string, pin *runtime.Pinner) uintptr {
	sup := &objcSuper{receiver: self, superClass: superclass(className)}
	pin.Pin(sup)
	return uintptr(unsafe.Pointer(sup))
}

// superclass returns the superclass of a class, cached: overrides call it
// as often as AppKit calls them, as setFrameSize: while a window resizes.
func superclass(name string) id {
	selMu.RLock()
	c, ok := superCache[name]
	selMu.RUnlock()
	if ok {
		return c
	}
	c = id(objc.Class(class(name)).SuperClass())
	selMu.Lock()
	superCache[name] = c
	selMu.Unlock()
	return c
}

// sendSuper calls the superclass implementation of a method overridden in
// className (see superOf).
//
//go:uintptrescapes
func sendSuper(self id, className string, s objc.SEL, args ...uintptr) id {
	var pin runtime.Pinner
	defer pin.Unpin()
	all := make([]any, 0, len(args)+2)
	all = append(all, superOf(self, className, &pin), s)
	for _, a := range args {
		all = append(all, a)
	}
	return mainthread.Forward[id](msgSendSuperAddr, all...)
}

// sendSuperSize is sendSuper for a method taking an NSSize, which travels in
// floating-point registers and so cannot go through the integer arguments.
func sendSuperSize(self id, className string, s objc.SEL, size NSSize) {
	var pin runtime.Pinner
	defer pin.Unpin()
	msgSuperSetSize(superOf(self, className, &pin), s, size)
}

// sendSuperPoint is sendSuper for a method taking an NSPoint and
// returning an object, as hitTest:.
func sendSuperPoint(self id, className string, s objc.SEL, p NSPoint) id {
	var pin runtime.Pinner
	defer pin.Unpin()
	return msgSuperPoint(superOf(self, className, &pin), s, p)
}

func respondsTo(obj id, selector string) bool {
	return obj != 0 && byte(sendHere(obj, "respondsToSelector:", uintptr(sel(selector)))) != 0
}

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

func retain(obj id) id {
	if obj == 0 {
		return 0
	}
	return sendHere(obj, "retain")
}

func release(obj id) {
	if obj != 0 {
		byReceiver(obj, "release")
	}
}

func autorelease(obj id) id {
	if obj == 0 {
		return 0
	}
	return byReceiver(obj, "autorelease")
}

// appKitString returns an NSString constant that AppKit exports, such as
// NSPrintJobSavingURL, whose value may differ from its name.
func appKitString(name string) id {
	p, err := purego.Dlsym(libAppKit, name)
	if err != nil {
		return nsString(name)
	}
	return **(**id)(unsafe.Pointer(&p))
}

// alloc creates an owned (+1) instance of a class with init.
func alloc(className string) id {
	return send(send(class(className), "alloc"), "init")
}

// withPool runs fn inside an autorelease pool. A pool belongs to the thread
// that pushed it, and popping it on another one crashes, so the goroutine
// stays on its thread meanwhile: off the main thread, where public methods
// such as App.Name read the bundle, it would move between threads.
func withPool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool, _, _ := purego.SyscallN(poolPushFn)
	defer purego.SyscallN(poolPopFn, pool)
	fn()
}

const nsUTF8StringEncoding = 4

// nsString returns an autoreleased NSString.
func nsString(s string) id {
	b := unsafe.StringData(s)
	str := sendHere(sendHere(class("NSString"), "alloc"), "initWithBytes:length:encoding:",
		uintptr(unsafe.Pointer(b)), uintptr(len(s)), nsUTF8StringEncoding)
	runtime.KeepAlive(s)
	return autorelease(str)
}

// goString copies an NSString into a Go string.
func goString(str id) string {
	if str == 0 {
		return ""
	}
	p := sendHere(str, "UTF8String")
	if p == 0 {
		return ""
	}
	n := int(sendHere(str, "lengthOfBytesUsingEncoding:", nsUTF8StringEncoding))
	return string(unsafe.Slice(*(**byte)(unsafe.Pointer(&p)), n))
}

// nsData returns an autoreleased NSData with a copy of b.
func nsData(b []byte) id {
	var p unsafe.Pointer
	if len(b) > 0 {
		p = unsafe.Pointer(&b[0])
	}
	d := sendHere(class("NSData"), "dataWithBytes:length:", uintptr(p), uintptr(len(b)))
	runtime.KeepAlive(b)
	return d
}

// goBytes copies the contents of an NSData.
func goBytes(data id) []byte {
	if data == 0 {
		return nil
	}
	n := sendInt(data, "length")
	if n == 0 {
		return []byte{}
	}
	p := send(data, "bytes")
	return append([]byte(nil), unsafe.Slice(*(**byte)(unsafe.Pointer(&p)), n)...)
}

func nsURL(s string) id { return send(class("NSURL"), "URLWithString:", uintptr(nsString(s))) }

// filePathURLBytes resolves a file reference URL (file:///.file/id=…,
// which Finder puts on the pasteboard when it drags or copies files) to a
// file URL of its path. Other URLs come back unchanged.
func filePathURLBytes(b []byte) []byte {
	s := strings.TrimRight(string(b), "\x00")
	if !strings.HasPrefix(s, "file:///.file/") {
		return b
	}
	u := nsURL(s)
	if u == 0 {
		return b
	}
	if p := send(u, "filePathURL"); p != 0 {
		if r := goString(send(p, "absoluteString")); r != "" {
			return []byte(r)
		}
	}
	return b
}

func fileURL(path string) id {
	return sendHere(class("NSURL"), "fileURLWithPath:", uintptr(nsString(path)))
}

// newDictionary returns an owned (+1) empty NSMutableDictionary, made on
// the calling thread with alloc and init, which the caller releases: a
// dictionary that a class method makes would be autoreleased, in the pool
// of the main thread for a message that goes there, which a caller off the
// worker's steps does not outlive.
func newDictionary() id {
	return sendHere(sendHere(class("NSMutableDictionary"), "alloc"), "init")
}

// setObject sets the value of key in a dictionary that newDictionary made.
func setObject(d, v, key id) {
	sendHere(d, "setObject:forKey:", uintptr(v), uintptr(key))
}

// emptyDictionary is an empty NSDictionary that the program keeps.
var emptyDictionary = sync.OnceValue(func() id {
	return sendHere(sendHere(class("NSDictionary"), "alloc"), "init")
})

// nsArray returns an autoreleased NSArray of objs, which the autorelease
// pool of the main thread holds (mainthread.ByReceiver).
func nsArray(objs ...id) id {
	arr := sendHere(sendHere(class("NSMutableArray"), "alloc"), "initWithCapacity:", uintptr(len(objs)))
	for _, o := range objs {
		sendHere(arr, "addObject:", uintptr(o))
	}
	return autorelease(arr)
}

func arrayItems(arr id) []id {
	if arr == 0 {
		return nil
	}
	n := sendInt(arr, "count")
	out := make([]id, n)
	for i := range n {
		out[i] = send(arr, "objectAtIndex:", uintptr(i))
	}
	return out
}

func nsNumberInt(v int) id { return sendHere(class("NSNumber"), "numberWithLongLong:", uintptr(v)) }

func nsBool(v bool) id { return sendHere(class("NSNumber"), "numberWithBool:", boolArg(v)) }

// callBlock invokes an Objective-C block received from the system, such as
// a WebKit completion handler, on the main thread.
func callBlock(block uintptr, args ...uintptr) {
	lit := *(**[3]uintptr)(unsafe.Pointer(&block))
	all := make([]any, 0, len(args)+1)
	all = append(all, block)
	for _, a := range args {
		all = append(all, a)
	}
	mainthread.Forward[struct{}](lit[2], all...)
}

// newBlock creates a block backed by a Go function, which runs on the
// worker when the main thread calls it (mainthread.Block). The function
// must take objc.Block as its first parameter.
func newBlock(fn any) objc.Block { return mainthread.Block(objc.NewBlock(fn)) }

// blockCopy copies a block, and blockRelease releases one, through
// mainthread.ByReceiver.
func blockCopy(block uintptr) uintptr { return mainthread.ByReceiver(blockCopyFn, block) }

func blockRelease(block uintptr) { mainthread.ByReceiver(blockReleaseFn, block) }

// nsError describes an NSError, preferring the JavaScript exception message
// WebKit attaches.
func nsError(err id) error {
	if err == 0 {
		return nil
	}
	info := send(err, "userInfo")
	if msg := send(info, "objectForKey:", uintptr(nsString("WKJavaScriptExceptionMessage"))); msg != 0 {
		return fmt.Errorf("%s", goString(msg))
	}
	return fmt.Errorf("%s", goString(send(err, "localizedDescription")))
}

// classDef builds an Objective-C class once.
func classDef(name, super string, protocols []string, methods []objc.MethodDef) id {
	var ps []*objc.Protocol
	for _, p := range protocols {
		if proto := objc.GetProtocol(p); proto != nil {
			ps = append(ps, proto)
		}
	}
	// Each method enters through a trampoline of the worker, which runs it
	// on the worker when the main thread calls it.
	c, err := mainthread.RegisterClass(name, objc.GetClass(super), ps, nil, methods)
	if err != nil {
		panic(fmt.Sprintf("mygo: cannot register class %s: %v", name, err))
	}
	selMu.Lock()
	clsCache[name] = id(c)
	selMu.Unlock()
	return id(c)
}

func method(selector string, fn any) objc.MethodDef {
	return objc.MethodDef{Cmd: sel(selector), Fn: fn}
}
