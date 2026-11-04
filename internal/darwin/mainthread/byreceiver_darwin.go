//go:build darwin

package mainthread

import (
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// decisions is what ByReceiver reads: the functions that it decides, the
// selectors of the messages, and the receivers whose calls run on the
// calling thread.
var decisions struct {
	msgSend                   uintptr
	cfRetain, cfRelease       uintptr
	blockCopy, blockRelease   uintptr
	selectors                 map[uintptr]string
	cfRetainIDs, cfReleaseIDs map[uintptr]bool
	groups                    []decidedGroup
	mu                        sync.Mutex
	byClass                   map[[2]uintptr]bool
	getTypeID                 func(obj uintptr) uintptr
	objectGetClass            func(obj uintptr) uintptr
	classIsMetaClass          func(cls uintptr) bool
	classGetSuperclass        func(cls uintptr) uintptr
	classConformsToProtocol   func(cls, proto uintptr) bool
	sendBool                  func(obj uintptr, sel objc.SEL, arg uintptr) bool
	isKindOfClass             objc.SEL
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
	ct, err := purego.Dlopen("/System/Library/Frameworks/CoreText.framework/CoreText", purego.RTLD_NOW|purego.RTLD_GLOBAL)
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
	purego.RegisterLibFunc(&d.objectGetClass, libObjC, "object_getClass")
	purego.RegisterLibFunc(&d.classIsMetaClass, libObjC, "class_isMetaClass")
	purego.RegisterLibFunc(&d.classGetSuperclass, libObjC, "class_getSuperclass")
	purego.RegisterLibFunc(&d.classConformsToProtocol, libObjC, "class_conformsToProtocol")
	purego.RegisterFunc(&d.sendBool, d.msgSend)
	var getClass, getProtocol func(name string) uintptr
	purego.RegisterLibFunc(&getClass, libObjC, "objc_getClass")
	purego.RegisterLibFunc(&getProtocol, libObjC, "objc_getProtocol")
	d.isKindOfClass = objc.RegisterName("isKindOfClass:")
	d.selectors = map[uintptr]string{}
	for _, s := range ByReceiverSelectors {
		d.selectors[uintptr(objc.RegisterName(s))] = s
	}
	d.cfRetainIDs, d.cfReleaseIDs = map[uintptr]bool{}, map[uintptr]bool{}
	for _, t := range CFTypes {
		lib := cf
		if t.Name[:2] == "CT" {
			lib = ct
		}
		var typeID func() uintptr
		p, e := purego.Dlsym(lib, t.Name+"GetTypeID")
		if e != nil {
			return e
		}
		purego.RegisterFunc(&typeID, p)
		id := typeID()
		if t.Retain {
			d.cfRetainIDs[id] = true
		}
		if t.Release {
			d.cfReleaseIDs[id] = true
		}
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
// mygo made, and through Forward otherwise.
func ByReceiver(fn uintptr, args ...uintptr) uintptr {
	if err := loadNative(); err != nil {
		panic(err)
	}
	if len(args) == 0 || args[0] == 0 || callingThread(fn, args) {
		r, _, _ := purego.SyscallN(fn, args...)
		return r
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
		return d.cfRetainIDs[d.getTypeID(recv)]
	case d.cfRelease:
		return d.cfReleaseIDs[d.getTypeID(recv)]
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
