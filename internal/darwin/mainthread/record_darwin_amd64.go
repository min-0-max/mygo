//go:build darwin && amd64

package mainthread

import (
	"fmt"
	"math"
	"reflect"
	"unsafe"
)

// record is one native call: the function, its argument registers and the
// words of its stack arguments, and the registers of its result. A call
// that runs on another thread than the one that makes it travels as a
// record, and callRecord makes the call that a record describes, so the
// thread that runs it needs nothing but the record. callRecord in
// record_darwin_amd64.s reads its offsets from go_asm.h.
type record struct {
	fn    uintptr
	x     [6]uintptr // rdi, rsi, rdx, rcx, r8 and r9
	al    uintptr    // the vector registers that a variadic callee reads
	xmm   [8]uint64  // xmm0 to xmm7
	stack [stackWords]uintptr
	r     [2]uintptr // rax and rdx of the result
	rx    [2]uint64  // xmm0 and xmm1 of the result

	// flags says how the record runs: recordStep for a callback that a
	// trampoline hands to the worker, with recordReturnsObject for one
	// that returns an object, and recordRetainsResult for a call whose
	// result the caller keeps.
	flags uintptr
	// next links the records that threads other than the worker queue
	// for the main thread, and sem is the Mach semaphore that the main
	// thread signals when it ran one.
	next, sem uintptr
}

const (
	recordStep          = 1 << 0
	recordReturnsObject = 1 << 1
	// recordRetainsResult has callRecord retain the object that the call
	// returns, for a caller that keeps it.
	recordRetainsResultBit = 2
	recordRetainsResult    = 1 << recordRetainsResultBit
)

// stackWords is how many words of stack arguments a record carries.
const stackWords = 16

// callRecord is a C function that makes the call that the record at its
// first argument describes and stores the result registers in it.
func callRecord()

// callRecordAddress returns the address of callRecord.
func callRecordAddress() uintptr

// builder fills the argument registers and the stack words of a record
// from Go values as the System V AMD64 ABI places them: each eightbyte of
// an integer, a pointer or a struct of up to 16 bytes in rdi, rsi, rdx,
// rcx, r8 and r9 or, when all of it is floats, in xmm0 to xmm7, a struct
// that does not fit in the registers that are left and a larger struct on
// the stack, and each stack argument in words of its own.
type builder struct {
	rec        *record
	ints, flts int
	stack      int // words of stack arguments
	keep       []any
}

func (b *builder) add(v reflect.Value) {
	switch v.Kind() {
	case reflect.Bool:
		u := uint64(0)
		if v.Bool() {
			u = 1
		}
		b.int(u)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.int(uint64(v.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		b.int(v.Uint())
	case reflect.Pointer, reflect.UnsafePointer:
		b.int(uint64(v.Pointer()))
		b.keep = append(b.keep, v.Interface())
	case reflect.Float32:
		b.float(uint64(math.Float32bits(float32(v.Float()))))
	case reflect.Float64:
		b.float(math.Float64bits(v.Float()))
	case reflect.Struct:
		b.addStruct(v)
	default:
		panic(fmt.Sprintf("mainthread: a native call cannot take a %s", v.Type()))
	}
}

func (b *builder) int(u uint64) {
	if b.ints < len(b.rec.x) {
		b.rec.x[b.ints] = uintptr(u)
		b.ints++
		return
	}
	b.onStack(u)
}

func (b *builder) float(u uint64) {
	if b.flts < len(b.rec.xmm) {
		b.rec.xmm[b.flts] = u
		b.flts++
		return
	}
	b.onStack(u)
}

func (b *builder) onStack(u uint64) {
	if b.stack == stackWords {
		panic("mainthread: a native call takes more stack arguments than a record carries")
	}
	b.rec.stack[b.stack] = uintptr(u)
	b.stack++
}

func (b *builder) addStruct(v reflect.Value) {
	t := v.Type()
	size := t.Size()
	if size == 0 {
		return
	}
	p := reflect.New(t)
	p.Elem().Set(v)
	mem := p.UnsafePointer()
	words := int((size + 7) / 8)
	var w [stackWords]uint64
	if words > len(w) {
		panic("mainthread: a native call takes a struct larger than a record carries")
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(&w[0])), size), unsafe.Slice((*byte)(mem), size))
	if size <= 16 {
		sse := eightbyteClasses(t)
		var ni, nf int
		for i := range words {
			if sse[i] {
				nf++
			} else {
				ni++
			}
		}
		if b.ints+ni <= len(b.rec.x) && b.flts+nf <= len(b.rec.xmm) {
			for i := range words {
				if sse[i] {
					b.float(w[i])
				} else {
					b.int(w[i])
				}
			}
			return
		}
	}
	for i := range words {
		b.onStack(w[i])
	}
}

// eightbyteClasses reports, for each eightbyte of a struct of up to 16
// bytes, whether all of it is floats, the class SSE of the ABI.
func eightbyteClasses(t reflect.Type) (sse [2]bool) {
	sse = [2]bool{true, true}
	var walk func(t reflect.Type, off uintptr)
	walk = func(t reflect.Type, off uintptr) {
		switch t.Kind() {
		case reflect.Struct:
			for i := range t.NumField() {
				f := t.Field(i)
				walk(f.Type, off+f.Offset)
			}
		case reflect.Array:
			for i := range t.Len() {
				walk(t.Elem(), off+uintptr(i)*t.Elem().Size())
			}
		case reflect.Float32, reflect.Float64:
		default:
			if t.Size() > 0 {
				sse[off/8] = false
			}
		}
	}
	walk(t, 0)
	return sse
}

// prepare makes the record of a call of fn with args whose result is of
// type t, and returns the values the call must keep alive and, for a
// result that goes in memory, where it goes.
func prepare(fn uintptr, t reflect.Type, args []any) (*record, []any, unsafe.Pointer) {
	rec := &record{fn: fn}
	b := builder{rec: rec}
	var out unsafe.Pointer
	if t != nil && t.Kind() == reflect.Struct && t.Size() > 16 {
		p := reflect.New(t)
		b.keep = append(b.keep, p.Interface())
		out = p.UnsafePointer()
		b.int(uint64(uintptr(out)))
	}
	for _, a := range args {
		b.add(reflect.ValueOf(a))
	}
	rec.al = uintptr(b.flts)
	return rec, b.keep, out
}

// result reads the result of type R of a call that rec made.
func result[R any](rec *record, out unsafe.Pointer) R {
	var r R
	t := reflect.TypeFor[R]()
	p := unsafe.Pointer(&r)
	switch t.Kind() {
	case reflect.Float32:
		*(*uint32)(p) = uint32(rec.rx[0])
	case reflect.Float64:
		*(*uint64)(p) = rec.rx[0]
	case reflect.Struct:
		size := t.Size()
		if size > 16 {
			return *(*R)(out)
		}
		sse := eightbyteClasses(t)
		var w [2]uint64
		ni, nf := 0, 0
		for i := range int((size + 7) / 8) {
			if sse[i] {
				w[i] = rec.rx[nf]
				nf++
			} else {
				w[i] = uint64(rec.r[ni])
				ni++
			}
		}
		copy(unsafe.Slice((*byte)(p), size), unsafe.Slice((*byte)(unsafe.Pointer(&w[0])), size))
	default:
		if t.Size() > 8 {
			panic(fmt.Sprintf("mainthread: a native call cannot return a %s", t))
		}
		copy(unsafe.Slice((*byte)(p), t.Size()), unsafe.Slice((*byte)(unsafe.Pointer(&rec.r[0])), t.Size()))
	}
	return r
}
