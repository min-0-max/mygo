//go:build darwin

package mainthread

import (
	"reflect"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
)

// direct makes the call of fn with args on the calling thread.
func direct[R any](fn uintptr, args ...any) R {
	rec, keep, out := prepare(fn, reflect.TypeFor[R](), args)
	run(rec)
	runtime.KeepAlive(keep)
	return result[R](rec, out)
}

// run makes the call that rec describes on the calling thread.
func run(rec *record) {
	purego.SyscallN(callRecordAddress(), uintptr(unsafe.Pointer(rec)))
}
