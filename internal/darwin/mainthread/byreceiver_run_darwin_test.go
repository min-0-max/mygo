//go:build darwin

package mainthread

import (
	"runtime"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// receiverCase is a call of ByReceiver and whether it runs on the calling
// thread.
type receiverCase struct {
	name string
	fn   uintptr
	args []uintptr
	here bool
}

// ByReceiver, called on the worker during a step, runs commit, release,
// autorelease, CFRetain, CFRelease, _Block_copy and _Block_release on the
// calling thread for a receiver of each class or type for which U1 runs
// them there and for a block that mygo made, and forwards them, one
// forwarded call each, for a CATransaction, an NSWindow, a run loop
// timer, an NSArray and a block whose dispose function is not a
// trampoline, a block without copy and dispose functions among them.
func TestByReceiverRunsEachReceiverKindWhereU1Says(t *testing.T) {
	const (
		cf        = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
		ct        = "/System/Library/Frameworks/CoreText.framework/CoreText"
		metal     = "/System/Library/Frameworks/Metal.framework/Metal"
		quartz    = "/System/Library/Frameworks/QuartzCore.framework/QuartzCore"
		appKit    = "/System/Library/Frameworks/AppKit.framework/AppKit"
		libObjC   = "/usr/lib/libobjc.A.dylib"
		utf8      = uint32(0x08000100)
		globalBit = 1 << 28
	)
	if err := loadNative(); err != nil {
		t.Fatal(err)
	}
	for _, lib := range []string{quartz, appKit} {
		if _, err := purego.Dlopen(lib, purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
			t.Fatal(err)
		}
	}
	d := &decisions
	call := func(lib, name string, args ...any) uintptr { return direct[uintptr](dlsym(t, lib, name), args...) }
	cstr := func(s string) *byte { b := append([]byte(s), 0); return &b[0] }
	sel := func(s string) uintptr { return uintptr(objc.RegisterName(s)) }
	send := func(obj uintptr, s string, args ...any) uintptr {
		return direct[uintptr](d.msgSend, append([]any{obj, sel(s)}, args...)...)
	}
	class := func(name string) uintptr { return uintptr(objc.GetClass(name)) }
	retain := func(obj uintptr) uintptr { return call(libObjC, "objc_retain", obj) }
	newObject := func(name string) uintptr { return send(send(class(name), "alloc"), "init") }

	// The receivers that run on the calling thread.
	str := call(cf, "CFStringCreateWithCString", uintptr(0), cstr("x"), utf8)
	attributed := call(cf, "CFAttributedStringCreate", uintptr(0), str, uintptr(0))
	number := call(cf, "CFNumberCreate", uintptr(0), uintptr(9), &[1]int64{7}) // kCFNumberLongLongType
	data := call(cf, "CFDataCreate", uintptr(0), cstr("d"), uintptr(1))
	array := call(cf, "CFArrayCreate", uintptr(0), uintptr(0), uintptr(0), uintptr(0))
	dictionary := call(cf, "CFDictionaryCreate", uintptr(0), uintptr(0), uintptr(0), uintptr(0), uintptr(0), uintptr(0))
	set := call(cf, "CFSetCreate", uintptr(0), uintptr(0), uintptr(0), uintptr(0))
	font := call(ct, "CTFontCreateWithName", str, 12.0, uintptr(0))
	descriptor := call(ct, "CTFontDescriptorCreateWithNameAndSize", str, 12.0)
	line := call(ct, "CTLineCreateWithAttributedString", attributed)
	paragraph := call(ct, "CTParagraphStyleCreate", uintptr(0), uintptr(0))
	runCallbacks := [5]uintptr{1} // kCTRunDelegateVersion1, no functions
	runDelegate := call(ct, "CTRunDelegateCreate", &runCallbacks, uintptr(0))
	typesetter := call(ct, "CTTypesetterCreateWithAttributedString", attributed)
	noop := purego.NewCallback(func(observer, activity, info uintptr) {})
	observer := call(cf, "CFRunLoopObserverCreate", uintptr(0), uintptr(1), false, uintptr(0), noop, uintptr(0))
	device := call(metal, "MTLCreateSystemDefaultDevice")
	if device == 0 {
		t.Fatal("no Metal device")
	}
	queue := send(device, "newCommandQueue")
	passDescriptor := newObject("MTLRenderPassDescriptor")
	layer := newObject("CAMetalLayer")
	send(layer, "setDevice:", device)
	direct[struct{}](d.msgSend, layer, objc.RegisterName("setDrawableSize:"), struct{ w, h float64 }{4, 4})
	drawable := retain(send(layer, "nextDrawable"))
	if drawable == 0 {
		t.Fatal("the layer gave no drawable")
	}
	made := Block(objc.NewBlock(func(objc.Block) {}))

	// The receivers that are forwarded.
	window := retain(call(libObjC, "class_createInstance", class("NSWindow"), uintptr(0)))
	nsArray := newObject("NSArray")
	timer := call(cf, "CFRunLoopTimerCreate", uintptr(0), 0.0, 0.0, uintptr(0), uintptr(0), noop, uintptr(0))
	systemDescriptor := [5]uintptr{0, 32, dlsym(t, cf, "CFRelease"), dlsym(t, cf, "CFRelease"), 0}
	system := [4]uintptr{0, blockHasCopyDispose | globalBit, 0, uintptr(unsafe.Pointer(&systemDescriptor))}
	plainDescriptor := [2]uintptr{0, 32}
	plain := [4]uintptr{0, globalBit, 0, uintptr(unsafe.Pointer(&plainDescriptor))}

	cases := []receiverCase{}
	here := func(name string, fn uintptr, args ...uintptr) {
		cases = append(cases, receiverCase{name, fn, args, true})
	}
	forwarded := func(name string, fn uintptr, args ...uintptr) {
		cases = append(cases, receiverCase{name, fn, args, false})
	}
	for _, v := range []struct {
		name string
		obj  uintptr
	}{
		{"NSString", str}, {"NSMutableString", newObject("NSMutableString")},
		{"NSAttributedString", attributed}, {"NSMutableAttributedString", newObject("NSMutableAttributedString")},
		{"NSNumber", number}, {"NSData", data}, {"NSMutableData", newObject("NSMutableData")},
		{"a Metal device", device}, {"a Metal command queue", queue},
		{"a Metal descriptor", passDescriptor}, {"a drawable", drawable},
	} {
		here("autorelease of "+v.name, d.msgSend, retain(v.obj), sel("autorelease"))
		here("release of "+v.name, d.msgSend, retain(v.obj), sel("release"))
	}
	here("commit of a Metal command buffer", d.msgSend, retain(send(queue, "commandBuffer")), sel("commit"))
	for _, v := range []struct {
		name    string
		obj     uintptr
		release bool
	}{
		{"CFString", str, false}, {"CFAttributedString", attributed, false}, {"CFNumber", number, false},
		{"CFData", data, false}, {"CFArray", array, false}, {"CFDictionary", dictionary, false},
		{"CFSet", set, false}, {"CTFont", font, false}, {"CTFontDescriptor", descriptor, false},
		{"CTLine", line, false}, {"CTParagraphStyle", paragraph, false}, {"CTRunDelegate", runDelegate, false},
		{"CTTypesetter", typesetter, false}, {"CFRunLoopObserver", observer, true},
	} {
		if v.obj == 0 {
			t.Fatalf("no %s", v.name)
		}
		if !v.release {
			here("CFRetain of a "+v.name, d.cfRetain, v.obj)
			here("CFRelease of a "+v.name, d.cfRelease, v.obj)
			continue
		}
		here("CFRelease of a "+v.name, d.cfRelease, retain(v.obj))
	}
	here("_Block_copy of a block that mygo made", d.blockCopy, uintptr(made))
	here("_Block_release of a block that mygo made", d.blockRelease, uintptr(made))

	forwarded("commit of CATransaction", d.msgSend, class("CATransaction"), sel("commit"))
	forwarded("autorelease of an NSWindow", d.msgSend, retain(window), sel("autorelease"))
	forwarded("release of an NSWindow", d.msgSend, retain(window), sel("release"))
	forwarded("autorelease of an NSArray", d.msgSend, retain(nsArray), sel("autorelease"))
	forwarded("release of an NSArray", d.msgSend, retain(nsArray), sel("release"))
	forwarded("CFRetain of a run loop timer", d.cfRetain, timer)
	forwarded("CFRelease of a run loop timer", d.cfRelease, timer)
	forwarded("_Block_copy of a block whose dispose function is not a trampoline", d.blockCopy, uintptr(unsafe.Pointer(&system)))
	forwarded("_Block_release of a block whose dispose function is not a trampoline", d.blockRelease, uintptr(unsafe.Pointer(&system)))
	forwarded("_Block_copy of a block without copy and dispose functions", d.blockCopy, uintptr(unsafe.Pointer(&plain)))
	forwarded("_Block_release of a block without copy and dispose functions", d.blockRelease, uintptr(unsafe.Pointer(&plain)))

	onFakeMain(t, func() {
		RunOnWorker(func() {
			for _, c := range cases {
				before := Forwarded()
				ByReceiver(c.fn, c.args...)
				n := Forwarded() - before
				switch {
				case c.here && n != 0:
					t.Errorf("%s: forwarded %d calls, want it on the calling thread", c.name, n)
				case !c.here && n != 1:
					t.Errorf("%s: forwarded %d calls, want 1", c.name, n)
				}
			}
		})
	})
	runtime.KeepAlive(&system)
	runtime.KeepAlive(&systemDescriptor)
	runtime.KeepAlive(&plain)
	runtime.KeepAlive(&plainDescriptor)
}
