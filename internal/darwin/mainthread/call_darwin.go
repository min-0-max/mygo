//go:build darwin

package mainthread

import (
	"sync"
	"unsafe"
)

// cgocall is the call of the runtime that runs a C function on the system
// stack of the thread, the call that purego.SyscallN makes too.
//
//go:linkname cgocall runtime.cgocall
func cgocall(fn uintptr, arg unsafe.Pointer) int32

// callRecords holds the records of callC between its calls.
var callRecords = sync.Pool{New: func() any { return new(record) }}

// callC calls the C function fn with the integer arguments args on the
// calling thread and returns its first result register, through
// callRecord with a record of callRecords. It allocates nothing once the
// pool holds a record: purego.SyscallN moves its arguments to the heap on
// each call (go:uintptrescapes), and a function that purego.RegisterFunc
// binds allocates its arguments and its result. The record is not on the
// stack of the goroutine, because a callback of Go that the call reaches
// can grow that stack and move it while callRecord still writes the
// result.
func callC(fn uintptr, args ...uintptr) uintptr {
	rec := callRecords.Get().(*record)
	*rec = record{fn: fn}
	if len(args) > len(rec.x) {
		panic("mainthread: callC takes its arguments in registers only")
	}
	copy(rec.x[:], args)
	cgocall(callRecordAddress(), unsafe.Pointer(rec))
	r := rec.r[0]
	callRecords.Put(rec)
	return r
}
