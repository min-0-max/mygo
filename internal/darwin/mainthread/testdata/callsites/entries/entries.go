// Package entries makes the C callbacks, blocks, method implementations
// and classes that native code calls, which enter through the trampolines
// of the worker package (callsites) or are findings.
package entries

import (
	"callsites"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var (
	callback = callsites.Callback(purego.NewCallback(func() {}))
	method   = callsites.Method(purego.NewCallback(func(self, cmd uintptr) {}), false)
	imp      = callsites.Method(uintptr(objc.NewIMP(func(self objc.ID, cmd objc.SEL) {})), true)
	block    = callsites.Block(objc.NewBlock(func(objc.Block) {}))

	rawCallback = purego.NewCallback(func() {})                                   // want: makes a C callback outside Callback or Method
	rawBlock    = objc.NewBlock(func(objc.Block) {})                              // want: makes a block outside Block
	rawIMP      = objc.NewIMP(func(self objc.ID, cmd objc.SEL) {})                // want: makes a method implementation outside Method
	blockCall   = callsites.Callback(uintptr(objc.NewBlock(func(objc.Block) {}))) // want: makes a block outside Block
)

func classes() {
	callsites.RegisterClass("Entered", 0, nil, nil, []objc.MethodDef{{Cmd: objc.RegisterName("go"), Fn: func(self objc.ID, cmd objc.SEL) {}}})
	objc.RegisterClass("Raw", 0, nil, nil, nil) // want: registers a class outside RegisterClass
}
