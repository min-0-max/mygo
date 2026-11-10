//go:build darwin && mainthreadcheck

package mainthread

import (
	"reflect"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Built with this tag, the calls of mygo are held at run time to the lists
// of this package: each forwarded call runs on the main thread, each
// message that runs on the calling thread goes to a receiver of
// CallingThreadReceivers, each function of the value objects gets an
// object of CFValueTypes, and no callback of Go reaches the main thread
// inside a RunOnMain off the worker. A call that is not panics.

// Bind binds the function variable that fn points to to the native
// function name of lib, which runs on the calling thread, through a
// function that checks each call.
func Bind(fn any, lib uintptr, name string) {
	purego.RegisterLibFunc(fn, lib, name)
	Check(fn, name)
}

// BindAddr is Bind for the native function name at addr.
func BindAddr(fn any, addr uintptr, name string) {
	purego.RegisterFunc(fn, addr)
	Check(fn, name)
}

// Check replaces the function that fn points to, which calls the native
// function name on the calling thread, with one that checks each call.
func Check(fn any, name string) {
	message := slices.Contains(MessageSendFunctions, name)
	if !message && !cfValueFunctions.holds(name) {
		return
	}
	v := reflect.ValueOf(fn).Elem()
	inner := reflect.ValueOf(v.Interface())
	v.Set(reflect.MakeFunc(v.Type(), func(args []reflect.Value) []reflect.Value {
		if message {
			CheckSend(addressOf(args[0]), addressOf(args[1]))
		} else {
			checkFunction(name, args)
		}
		if v.Type().IsVariadic() {
			return inner.CallSlice(args)
		}
		return inner.Call(args)
	}))
}

// addressOf returns an argument that holds an address, and 0 for any other.
func addressOf(v reflect.Value) uintptr {
	switch v.Kind() {
	case reflect.Uintptr:
		return uintptr(v.Uint())
	case reflect.UnsafePointer, reflect.Pointer:
		return v.Pointer()
	}
	return 0
}

// checks is what the checks read.
var checks struct {
	once           sync.Once
	mu             sync.Mutex
	messages       map[[2]uintptr]bool
	classes        map[string]uintptr
	types          map[uintptr]string
	selName        func(sel uintptr) string
	getClass       func(name string) uintptr
	getProtocol    func(name string) uintptr
	className      func(cls uintptr) string
	cfRelease      uintptr
	arrayCount     func(a uintptr) int
	arrayAt        func(a uintptr, i int) uintptr
	dictCount      func(d uintptr) int
	dictKeysValues func(d uintptr, keys, values *uintptr)
	setCount       func(s uintptr) int
	setValues      func(s uintptr, values *uintptr)
}

func loadChecks() {
	checks.once.Do(func() {
		if err := loadNative(); err != nil {
			panic(err)
		}
		c := &checks
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			panic(err)
		}
		libObjC, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			panic(err)
		}
		c.messages, c.classes, c.types = map[[2]uintptr]bool{}, map[string]uintptr{}, map[uintptr]string{}
		purego.RegisterLibFunc(&c.selName, libObjC, "sel_getName")
		purego.RegisterLibFunc(&c.getClass, libObjC, "objc_getClass")
		purego.RegisterLibFunc(&c.getProtocol, libObjC, "objc_getProtocol")
		purego.RegisterLibFunc(&c.className, libObjC, "class_getName")
		purego.RegisterLibFunc(&c.arrayCount, cf, "CFArrayGetCount")
		purego.RegisterLibFunc(&c.arrayAt, cf, "CFArrayGetValueAtIndex")
		purego.RegisterLibFunc(&c.dictCount, cf, "CFDictionaryGetCount")
		purego.RegisterLibFunc(&c.dictKeysValues, cf, "CFDictionaryGetKeysAndValues")
		purego.RegisterLibFunc(&c.setCount, cf, "CFSetGetCount")
		purego.RegisterLibFunc(&c.setValues, cf, "CFSetGetValues")
		c.cfRelease = decisions.cfRelease
	})
}

// CheckSend panics unless the message sel to recv runs on the calling
// thread by CallingThreadReceivers.
func CheckSend(recv, sel uintptr) {
	loadChecks()
	c := &checks
	cls := classOf(recv)
	key := [2]uintptr{cls, sel}
	c.mu.Lock()
	ok, seen := c.messages[key]
	c.mu.Unlock()
	if !seen {
		ok = messageRunsHere(recv, cls, c.selName(sel))
		if ok {
			c.mu.Lock()
			c.messages[key] = ok
			c.mu.Unlock()
		}
	}
	if !ok {
		panic("mainthread: the message " + c.selName(sel) + " to " + c.className(cls) + " runs on the calling thread, which no group of CallingThreadReceivers says")
	}
}

// messageRunsHere reports whether a group of CallingThreadReceivers holds
// the message name to recv, of class cls.
func messageRunsHere(recv, cls uintptr, name string) bool {
	d := &decisions
	isClass := d.classIsMetaClass(cls)
	for _, g := range CallingThreadReceivers {
		if !g.AllMessages && !slices.Contains(g.Selectors, name) {
			continue
		}
		if g.AnyObject {
			return true
		}
		if isClass {
			if g.ClassMessages && inClasses(g.Classes, recv) {
				return true
			}
			continue
		}
		if inClasses(g.Classes, cls) || conforms(g.Protocols, cls) {
			return true
		}
	}
	return false
}

// inClasses reports whether cls is one of the named classes or a subclass
// of one.
func inClasses(names []string, cls uintptr) bool {
	for _, n := range names {
		want := classNamed(n)
		if want == 0 {
			continue
		}
		for c := cls; c != 0; c = decisions.classGetSuperclass(c) {
			if c == want {
				return true
			}
		}
	}
	return false
}

// conforms reports whether cls or a superclass conforms to a named
// protocol.
func conforms(names []string, cls uintptr) bool {
	for _, n := range names {
		p := checks.getProtocol(n)
		if p == 0 {
			continue
		}
		for c := cls; c != 0; c = decisions.classGetSuperclass(c) {
			if decisions.classConformsToProtocol(c, p) {
				return true
			}
		}
	}
	return false
}

// classNamed finds a class that a framework loaded by now.
func classNamed(name string) uintptr {
	c := &checks
	c.mu.Lock()
	cls := c.classes[name]
	c.mu.Unlock()
	if cls == 0 {
		if cls = c.getClass(name); cls != 0 {
			c.mu.Lock()
			c.classes[name] = cls
			c.mu.Unlock()
		}
	}
	return cls
}

// typeName returns the name of the Core Foundation type of obj, as cfType
// finds it: asking a type for its ID can break it (CTFontGetTypeID).
func typeName(obj uintptr) string {
	const utf8 = 0x08000100 // kCFStringEncodingUTF8
	loadChecks()
	c, d := &checks, &decisions
	id := typeIDOf(obj)
	c.mu.Lock()
	name, seen := c.types[id]
	c.mu.Unlock()
	if seen {
		return name
	}
	desc := d.copyTypeIDDescription(id)
	var buf [256]byte
	if d.stringGetCString(desc, &buf[0], len(buf), utf8) {
		name = string(buf[:strings.IndexByte(string(buf[:]), 0)])
	}
	purego.SyscallN(c.cfRelease, desc)
	c.mu.Lock()
	c.types[id] = name
	c.mu.Unlock()
	return name
}

// checkFunction panics unless the object that the function of
// cfValueFunctions gets is of CFValueTypes. CFHash and CFEqual also take
// the Core Text objects of CFTypes, whose functions run on the calling
// thread and which the text engine compares. An allocator of 0 and an
// argument that holds no address are no object.
func checkFunction(name string, args []reflect.Value) {
	n, compares := 1, name == "CFHash"
	if name == "CFEqual" {
		n, compares = 2, true
	}
	for _, a := range args[:min(n, len(args))] {
		if obj := addressOf(a); obj != 0 {
			t := typeName(obj)
			if !slices.Contains(CFValueTypes, t) && !(compares && coreTextType(t)) {
				panic("mainthread: " + name + " runs on the calling thread with a " + t + ", no type of CFValueTypes")
			}
		}
	}
}

// checkCalling checks a call that ByReceiver runs on the calling thread:
// a collection that CFRelease gets holds at each depth only objects of
// the types that it may release there.
func checkCalling(fn uintptr, args []uintptr) {
	if fn != decisions.cfRelease || len(args) == 0 || args[0] == 0 {
		return
	}
	loadChecks()
	switch typeName(args[0]) {
	case "CFArray", "CFDictionary", "CFSet":
		checkHeld(args[0])
	}
}

// checkHeld panics for a member of collection obj at any depth that a
// CFRelease on the calling thread may not reach.
func checkHeld(obj uintptr) {
	c := &checks
	var members []uintptr
	switch t := typeName(obj); t {
	case "CFArray":
		for i := range c.arrayCount(obj) {
			members = append(members, c.arrayAt(obj, i))
		}
	case "CFDictionary":
		if n := c.dictCount(obj); n > 0 {
			keys, values := make([]uintptr, n), make([]uintptr, n)
			c.dictKeysValues(obj, &keys[0], &values[0])
			members = append(keys, values...)
		}
	case "CFSet":
		if n := c.setCount(obj); n > 0 {
			members = make([]uintptr, n)
			c.setValues(obj, &members[0])
		}
	default:
		if !heldType(t) {
			panic("mainthread: CFRelease on the calling thread reaches a " + t + " in a collection")
		}
		return
	}
	for _, m := range members {
		checkHeld(m)
	}
}

// heldType reports whether a collection may hold an object of type t.
func heldType(t string) bool {
	if slices.Contains(CFValueTypes, t) || slices.Contains(CFCollectionTypes, t) {
		return true
	}
	return coreTextType(t)
}

// coreTextType reports whether CFTypes names t as a Core Text object.
func coreTextType(t string) bool {
	return strings.HasPrefix(t, "CT") && slices.ContainsFunc(CFTypes, func(c CFType) bool { return c.Name == t })
}

// checkedCallRecordAddress is a native C-ABI function taking (rec, expectedThread).
// It returns zero after running rec on the expected thread, or the actual
// thread identity without running rec. Neither path enters a Go callback.
func checkedCallRecordAddress() uintptr

// checkedOnMain wraps the native call with a native identity gate. Both
// architectures place its two integer arguments in the first two x slots.
func checkedOnMain(rec *record) *record {
	outer := &record{fn: checkedCallRecordAddress()}
	outer.x[0] = uintptr(unsafe.Pointer(rec))
	outer.x[1] = link.mainThread
	return outer
}

// checkForwarded reports a native dispatch failure on the caller, after the
// main thread has returned the record. Reporting must never enter Go on the
// main thread, whose native stack JavaScriptCore may scan concurrently.
func checkForwarded(rec *record) {
	if rec.r[0] != 0 {
		panic("mainthread: a forwarded call runs off the main thread")
	}
}
