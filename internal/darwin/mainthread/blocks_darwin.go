//go:build darwin

package mainthread

import (
	"unsafe"

	"github.com/ebitengine/purego/objc"
)

// The layout of a block (Block_literal_1 of the block ABI of Apple) and of
// its descriptor, and the flag that says that the descriptor holds copy
// and dispose functions.
const (
	blockFlags          = 8
	blockInvoke         = 16
	blockDescriptor     = 24
	descriptorDispose   = 24
	blockHasCopyDispose = 1 << 25
)

// word returns a pointer to the word at addr, memory that the Objective-C
// runtime owns.
func word(addr uintptr) *uintptr {
	return (*uintptr)(*(*unsafe.Pointer)(unsafe.Pointer(&addr)))
}

// Block sets the invoke function of b, a block that objc.NewBlock made,
// and the dispose function of its descriptor to trampolines, so that a
// block that the main thread calls or disposes runs on the worker, and
// returns b.
func Block(b objc.Block) objc.Block {
	p := uintptr(b)
	if p == 0 {
		return b
	}
	invoke := word(p + blockInvoke)
	if !isTrampoline(*invoke) {
		*invoke = entry(*invoke, 0, StepBlock, "")
	}
	if uint32(*word(p + blockFlags))&blockHasCopyDispose != 0 {
		dispose := word(*word(p + blockDescriptor) + descriptorDispose)
		if !isTrampoline(*dispose) {
			*dispose = uncountedEntry(*dispose, 0, StepBlock, "objc.block dispose")
		}
	}
	return b
}

// madeBlock reports whether the block at p is one that Block set up: its
// descriptor holds a dispose function that is a trampoline.
func madeBlock(p uintptr) bool {
	if uint32(*word(p + blockFlags))&blockHasCopyDispose == 0 {
		return false
	}
	return isTrampoline(*word(*word(p + blockDescriptor) + descriptorDispose))
}
