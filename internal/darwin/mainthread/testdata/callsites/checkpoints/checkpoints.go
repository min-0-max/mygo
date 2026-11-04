// Package checkpoints holds the native calls on the calling thread of a
// package other than the worker's, which a check at run time sees or, on
// the lines that end with what the reader must say, does not.
package checkpoints

import (
	"callsites"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var (
	lib, msgSend, cfHash uintptr
	fontSize             func(font uintptr) float64
	retain               func(obj uintptr, s objc.SEL) uintptr
	hash, unchecked      func(obj uintptr) uint
	other                func(obj uintptr) uint
)

func sym(name string) uintptr {
	p, err := purego.Dlsym(lib, name)
	if err != nil {
		panic(err)
	}
	return p
}

func load() {
	msgSend, cfHash = sym("objc_msgSend"), sym("CFHash")
	callsites.Bind(&fontSize, lib, "CTFontGetSize")
	callsites.BindAddr(&retain, msgSend, "objc_msgSend")
	purego.RegisterLibFunc(&fontSize, lib, "CTFontGetSize") // want: binds a function outside Bind and BindAddr
	purego.RegisterFunc(&retain, msgSend)                   // want: binds a function outside Bind and BindAddr
	hash = func(obj uintptr) uint { r, _, _ := purego.SyscallN(cfHash, obj); return uint(r) }
	unchecked = func(obj uintptr) uint { r, _, _ := purego.SyscallN(cfHash, obj); return uint(r) } // want: calls CFHash through purego.SyscallN outside Check
	callsites.Check(&hash, "CFHash")
	callsites.Check(&other, "CFHash")
}

func sends(obj uintptr) {
	s := objc.RegisterName("retain")
	retain(obj, s)
	purego.SyscallN(msgSend, obj, uintptr(s)) // want: sends a message after no CheckSend in sends
	objc.ID(obj).Send(s)                      // want: sends a message after no CheckSend in sends
	callsites.CheckSend(obj, uintptr(s))
	purego.SyscallN(msgSend, obj, uintptr(s))
	objc.ID(obj).Send(s)
}

func direct(obj uintptr) {
	purego.SyscallN(cfHash, obj) // want: calls CFHash through purego.SyscallN outside Check
}
