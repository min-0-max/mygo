//go:build darwin

package mainthread

import (
	"bytes"
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// decisions is what ByReceiver reads: the functions that it decides, the
// selectors of the messages, and the receivers whose calls run on the
// calling thread.
var decisions struct {
	msgSend                 uintptr
	cfRetain, cfRelease     uintptr
	blockCopy, blockRelease uintptr
	selectors               map[uintptr]string
	cfTypes                 map[string]CFType  // CFTypes by name
	cfByID                  map[uintptr]CFType // the entries of CFTypes found by type ID, under mu
	groups                  []decidedGroup
	mu                      sync.Mutex
	byClass                 map[[2]uintptr]bool
	getTypeID               func(obj uintptr) uintptr
	copyTypeIDDescription   func(typeID uintptr) uintptr
	stringGetCString        func(s uintptr, buf *byte, size int, encoding uint32) bool
	objectGetClass          func(obj uintptr) uintptr
	classIsMetaClass        func(cls uintptr) bool
	classGetName            func(cls uintptr) string
	classGetSuperclass      func(cls uintptr) uintptr
	classConformsToProtocol func(cls, proto uintptr) bool
	sendBool                func(obj uintptr, sel objc.SEL, arg uintptr) bool
	isKindOfClass           objc.SEL
	autorelease             uintptr
}

// decidedGroup is a group of CallingThreadReceivers with the messages of
// ByReceiverSelectors that run on the calling thread for it.
type decidedGroup struct {
	classes, protocols []uintptr
	decided            map[string]bool
}

// loadDecisions resolves what ByReceiver reads.
func loadDecisions(cf uintptr) error {
	libObjC, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	libSystem, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
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
	d := &decisions
	d.msgSend = sym(libObjC, "objc_msgSend")
	d.cfRetain = sym(cf, "CFRetain")
	d.cfRelease = sym(cf, "CFRelease")
	d.blockCopy = sym(libSystem, "_Block_copy")
	d.blockRelease = sym(libSystem, "_Block_release")
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&d.getTypeID, cf, "CFGetTypeID")
	purego.RegisterLibFunc(&d.copyTypeIDDescription, cf, "CFCopyTypeIDDescription")
	purego.RegisterLibFunc(&d.stringGetCString, cf, "CFStringGetCString")
	purego.RegisterLibFunc(&d.objectGetClass, libObjC, "object_getClass")
	purego.RegisterLibFunc(&d.classIsMetaClass, libObjC, "class_isMetaClass")
	purego.RegisterLibFunc(&d.classGetName, libObjC, "class_getName")
	purego.RegisterLibFunc(&d.classGetSuperclass, libObjC, "class_getSuperclass")
	purego.RegisterLibFunc(&d.classConformsToProtocol, libObjC, "class_conformsToProtocol")
	purego.RegisterFunc(&d.sendBool, d.msgSend)
	var getClass, getProtocol func(name string) uintptr
	purego.RegisterLibFunc(&getClass, libObjC, "objc_getClass")
	purego.RegisterLibFunc(&getProtocol, libObjC, "objc_getProtocol")
	d.isKindOfClass = objc.RegisterName("isKindOfClass:")
	d.autorelease = uintptr(objc.RegisterName("autorelease"))
	d.selectors = map[uintptr]string{}
	for _, s := range ByReceiverSelectors {
		d.selectors[uintptr(objc.RegisterName(s))] = s
	}
	// The type of a receiver is found by name, never by asking a type for
	// its ID: asking CTFont for its ID (CTFontGetTypeID) before AppKit is
	// loaded leaves CTFont without its bridge to NSFont, and every font
	// that NSFont makes afterwards fails.
	d.cfTypes, d.cfByID = map[string]CFType{}, map[uintptr]CFType{}
	for _, t := range CFTypes {
		d.cfTypes[t.Name] = t
	}
	for _, g := range CallingThreadReceivers {
		if len(g.Decided) == 0 {
			continue
		}
		dg := decidedGroup{decided: map[string]bool{}}
		for _, s := range g.Decided {
			dg.decided[s] = true
		}
		for _, c := range g.Classes {
			if cls := getClass(c); cls != 0 {
				dg.classes = append(dg.classes, cls)
			}
		}
		for _, p := range g.Protocols {
			if proto := getProtocol(p); proto != 0 {
				dg.protocols = append(dg.protocols, proto)
			}
		}
		d.groups = append(d.groups, dg)
	}
	d.byClass = map[[2]uintptr]bool{}
	return nil
}

// ByReceiver runs the call of fn with args, a message of
// ByReceiverSelectors or a function of ByReceiverFunctions whose first
// argument is the receiver, on the calling thread when the receiver is
// one for which CallingThreadReceivers or CFTypes say so, or a block that
// mygo made, and through Forward otherwise. An autorelease that Forward
// would run for a thread other than the worker panics: the only owner of
// the object would be a pool of the main thread, which drains without
// that thread.
func ByReceiver(fn uintptr, args ...uintptr) uintptr {
	if err := loadNative(); err != nil {
		panic(err)
	}
	if len(args) == 0 || args[0] == 0 || callingThread(fn, args) {
		checkCalling(fn, args)
		r, _, _ := purego.SyscallN(fn, args...)
		return r
	}
	d := &decisions
	if fn == d.msgSend && len(args) > 1 && args[1] == d.autorelease &&
		atomic.LoadUintptr(&link.running) != 0 && !IsMainThread() {
		panic("mainthread: autorelease of " + d.classGetName(d.objectGetClass(args[0])) + " off the worker")
	}
	all := make([]any, len(args))
	for i, a := range args {
		all[i] = a
	}
	return Forward[uintptr](fn, all...)
}

// callingThread decides a call of ByReceiver.
func callingThread(fn uintptr, args []uintptr) bool {
	d := &decisions
	recv := args[0]
	switch fn {
	case d.cfRetain:
		return cfType(recv).Retain
	case d.cfRelease:
		return cfType(recv).Release
	case d.blockCopy, d.blockRelease:
		return madeBlock(recv)
	case d.msgSend:
		if len(args) < 2 {
			return false
		}
		name, ok := d.selectors[args[1]]
		if !ok {
			return false
		}
		cls := d.objectGetClass(recv)
		key := [2]uintptr{cls, args[1]}
		d.mu.Lock()
		v, seen := d.byClass[key]
		d.mu.Unlock()
		if seen {
			return v
		}
		v = decideClass(recv, cls, name)
		d.mu.Lock()
		d.byClass[key] = v
		d.mu.Unlock()
		return v
	}
	return false
}

// cfType returns the entry of CFTypes for the type of recv, a Core
// Foundation object, or the zero CFType when CFTypes does not name it. The
// type is found by the description of its type ID
// (CFCopyTypeIDDescription), which is its name.
func cfType(recv uintptr) CFType {
	const utf8 = 0x08000100 // kCFStringEncodingUTF8
	d := &decisions
	typeID := d.getTypeID(recv)
	d.mu.Lock()
	t, seen := d.cfByID[typeID]
	d.mu.Unlock()
	if seen {
		return t
	}
	desc := d.copyTypeIDDescription(typeID)
	var buf [256]byte
	if d.stringGetCString(desc, &buf[0], len(buf), utf8) {
		name, _, _ := bytes.Cut(buf[:], []byte{0})
		t = d.cfTypes[string(name)]
	}
	d.mu.Lock()
	d.cfByID[typeID] = t
	d.mu.Unlock()
	// The description is a CFString: once the type of the description
	// of CFString's type ID is found, the release finds it without
	// another description.
	ByReceiver(d.cfRelease, desc)
	return t
}

// decideClass reports whether message name to recv, an instance of cls,
// runs on the calling thread.
func decideClass(recv, cls uintptr, name string) bool {
	d := &decisions
	if d.classIsMetaClass(cls) {
		return false
	}
	for _, g := range d.groups {
		if !g.decided[name] {
			continue
		}
		for _, c := range g.classes {
			if d.sendBool(recv, d.isKindOfClass, c) {
				return true
			}
		}
		for _, p := range g.protocols {
			for c := cls; c != 0; c = d.classGetSuperclass(c) {
				if d.classConformsToProtocol(c, p) {
					return true
				}
			}
		}
	}
	return false
}
