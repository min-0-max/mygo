//go:build darwin && mainthreadcheck

package mainthread

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/wait"
)

// checkEnv makes the test binary the helper process of
// TestCheckerFindsAForwardedCallOffTheMainThread.
const checkEnv = "MYGO_MAINTHREAD_TEST_CHECK"

func init() { helpers[checkEnv] = checkHelper }

// checkLoad opens the frameworks of the receivers of these tests and
// returns Core Foundation.
func checkLoad(t *testing.T) uintptr {
	t.Helper()
	if err := loadNative(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"/System/Library/Frameworks/Metal.framework/Metal",
		"/System/Library/Frameworks/QuartzCore.framework/QuartzCore",
		"/System/Library/Frameworks/Foundation.framework/Foundation",
	} {
		if _, err := purego.Dlopen(p, purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
			t.Fatal(err)
		}
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		t.Fatal(err)
	}
	return cf
}

// checkMainLoop returns a Core Foundation object of no value type.
func checkMainLoop() uintptr { return native.runLoopGetMain() }

// panics reports what fn panicked with, the empty string for no panic.
func panics(fn func()) (msg string) {
	defer func() {
		if p := recover(); p != nil {
			msg, _ = p.(string)
			if msg == "" {
				msg = "a panic that is no string"
			}
		}
	}()
	fn()
	return ""
}

func checkObject(class string) uintptr {
	return uintptr(objc.ID(objc.GetClass(class)).Send(objc.RegisterName("new")))
}

// A message is accepted for a receiver that a group of
// CallingThreadReceivers names and refused for any other.
func TestCheckerJudgesTheReceiverOfAMessage(t *testing.T) {
	checkLoad(t)
	cls := func(name string) uintptr { return uintptr(objc.GetClass(name)) }
	layer, object := checkObject("CAMetalLayer"), checkObject("NSObject")
	str := uintptr(objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("string")))
	for _, c := range []struct {
		name string
		recv uintptr
		sel  string
		ok   bool
	}{
		{"an instance of a value object", str, "length", true},
		{"a class of a value object", cls("NSString"), "alloc", true},
		{"a Metal descriptor class", cls("MTLRenderPassDescriptor"), "renderPassDescriptor", true},
		{"the layer for nextDrawable", layer, "nextDrawable", true},
		{"retain to any object", object, "retain", true},
		{"respondsToSelector: to any object", layer, "respondsToSelector:", true},
		{"isKindOfClass: to a class", cls("NSObject"), "isKindOfClass:", true},
		{"another message to the layer", layer, "bounds", false},
		{"a message to an object of no group", object, "hash", false},
		{"a message to a class of no group", cls("NSObject"), "new", false},
		{"a method of a value object sent to the layer", layer, "length", false},
	} {
		sel := uintptr(objc.RegisterName(c.sel))
		msg := panics(func() { CheckSend(c.recv, sel) })
		if c.ok && msg != "" {
			t.Errorf("%s: %s", c.name, msg)
		}
		if !c.ok && !strings.Contains(msg, c.sel) {
			t.Errorf("%s: panic %q, want one that names %s", c.name, msg, c.sel)
		}
	}
}

// A function that Bind binds to objc_msgSend checks its receiver, and one
// bound to a function of the value objects the type of its object.
func TestCheckerJudgesWhatBindBinds(t *testing.T) {
	cf := checkLoad(t)
	var hash func(obj uintptr, sel objc.SEL) uint
	var send func(obj uintptr, sel objc.SEL) uint
	BindAddr(&send, msgSend(), "objc_msgSend")
	hash = send
	object := checkObject("NSObject")
	if msg := panics(func() { hash(object, objc.RegisterName("hash")) }); !strings.Contains(msg, "hash") {
		t.Errorf("a message bound to objc_msgSend: panic %q", msg)
	}
	var count func(a uintptr) int
	Bind(&count, cf, "CFArrayGetCount")
	var create func(alloc, values uintptr, n int, callbacks uintptr) uintptr
	Bind(&create, cf, "CFArrayCreate")
	array := create(0, 0, 0, 0)
	if n := count(array); n != 0 {
		t.Errorf("CFArrayGetCount = %d", n)
	}
	if msg := panics(func() { count(checkMainLoop()) }); !strings.Contains(msg, "CFArrayGetCount") {
		t.Errorf("CFArrayGetCount of a run loop: panic %q", msg)
	}
	// A function of another package, made of purego.SyscallN.
	made := func(uintptr) int { return 0 }
	Check(&made, "CFArrayGetCount")
	if msg := panics(func() { made(checkMainLoop()) }); !strings.Contains(msg, "CFArrayGetCount") {
		t.Errorf("Check of CFArrayGetCount with a run loop: panic %q", msg)
	}
	// Neither its allocator nor a length is an object.
	var maxSize func(length int, encoding uint32) int
	Bind(&maxSize, cf, "CFStringGetMaximumSizeForEncoding")
	if maxSize(4, 0x08000100) == 0 {
		t.Error("CFStringGetMaximumSizeForEncoding = 0")
	}
}

// CFRelease of a collection on the calling thread is refused when it holds
// at any depth an object of a type that CFTypes does not name for it.
func TestCheckerReadsACollectionAtEachDepth(t *testing.T) {
	cf := checkLoad(t)
	sym := func(name string) uintptr {
		p, err := purego.Dlsym(cf, name)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	var arrayCreate func(alloc uintptr, values *uintptr, n int, callbacks uintptr) uintptr
	var dictCreate func(alloc uintptr, keys, values *uintptr, n int, keyCallbacks, valueCallbacks uintptr) uintptr
	var setCreate func(alloc uintptr, values *uintptr, n int, callbacks uintptr) uintptr
	var stringCreate func(alloc uintptr, s string, encoding uint32) uintptr
	purego.RegisterLibFunc(&arrayCreate, cf, "CFArrayCreate")
	purego.RegisterLibFunc(&dictCreate, cf, "CFDictionaryCreate")
	purego.RegisterLibFunc(&setCreate, cf, "CFSetCreate")
	purego.RegisterLibFunc(&stringCreate, cf, "CFStringCreateWithCString")
	arrayCallbacks := sym("kCFTypeArrayCallBacks")
	setCallbacks := sym("kCFTypeSetCallBacks")
	keyCallbacks := sym("kCFTypeDictionaryKeyCallBacks")
	valueCallbacks := sym("kCFTypeDictionaryValueCallBacks")
	array := func(v uintptr) uintptr { return arrayCreate(0, &v, 1, arrayCallbacks) }
	set := func(v uintptr) uintptr { return setCreate(0, &v, 1, setCallbacks) }
	dict := func(k, v uintptr) uintptr { return dictCreate(0, &k, &v, 1, keyCallbacks, valueCallbacks) }
	str := func() uintptr { return stringCreate(0, "key", 0x08000100) }
	boolean := *(*uintptr)(*(*unsafe.Pointer)(unsafe.Pointer(&[]uintptr{sym("kCFBooleanTrue")}[0])))
	release := sym("CFRelease")

	if msg := panics(func() { ByReceiver(release, dict(str(), array(set(boolean)))) }); msg != "" {
		t.Errorf("a dictionary of an array of a set of a boolean: %s", msg)
	}
	object := checkObject("NSObject")
	for name, c := range map[string]uintptr{
		"an array":                          array(object),
		"a set":                             set(object),
		"a dictionary value":                dict(str(), object),
		"an array of a dictionary of a set": array(dict(str(), set(object))),
	} {
		if msg := panics(func() { ByReceiver(release, c) }); !strings.Contains(msg, "CFRelease") {
			t.Errorf("an object of no type in %s: panic %q", name, msg)
		}
	}
}

// A forwarded call runs through a function that fails off the main thread,
// which this process reaches by running the record where it stands.
func TestCheckerFindsAForwardedCallOffTheMainThread(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*wait.Guard)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), checkEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the helper ended well:\n%s", out)
	}
	if !strings.Contains(string(out), "off the main thread") {
		t.Fatalf("%v:\n%s", err, out)
	}
}

// checkHelper runs the record of a forwarded call on a goroutine.
func checkHelper() int {
	if err := loadNative(); err != nil {
		return 2
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pid, err := purego.Dlsym(purego.RTLD_DEFAULT, "getpid")
		if err != nil {
			return
		}
		rec, _, _ := prepareForTest(pid)
		run(checkedOnMain(rec))
	}()
	<-done
	return 0
}

func prepareForTest(fn uintptr) (*record, []any, unsafe.Pointer) {
	return prepare(fn, reflect.TypeFor[int32](), nil)
}
