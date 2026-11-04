//go:build darwin && arm64

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
// record_darwin_arm64.s reads its offsets from go_asm.h.
type record struct {
	fn    uintptr
	x     [8]uintptr // x0 to x7
	x8    uintptr    // the address of a result that goes in memory
	d     [8]uint64  // d0 to d7
	stack [stackWords]uintptr
	r     [2]uintptr // x0 and x1 of the result
	rd    [4]uint64  // d0 to d3 of the result

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
// from Go values as the arm64 procedure call standard of Apple places
// them: integers, pointers and structs of up to 16 bytes in x0 to x7,
// floats and structs of up to four floats of one type in d0 to d7, a
// larger struct by the address of a copy, and the rest on the stack, each
// at its own alignment and size.
type builder struct {
	rec        *record
	ints, flts int
	stack      uintptr // bytes of stack arguments
	keep       []any
}

func (b *builder) add(v reflect.Value) {
	switch v.Kind() {
	case reflect.Bool:
		u := uint64(0)
		if v.Bool() {
			u = 1
		}
		b.int(u, 1)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.int(uint64(v.Int()), v.Type().Size())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		b.int(v.Uint(), v.Type().Size())
	case reflect.Pointer, reflect.UnsafePointer:
		b.int(uint64(v.Pointer()), 8)
		b.keep = append(b.keep, v.Interface())
	case reflect.Float32:
		b.float(uint64(math.Float32bits(float32(v.Float()))), 4)
	case reflect.Float64:
		b.float(math.Float64bits(v.Float()), 8)
	case reflect.Struct:
		b.addStruct(v)
	default:
		panic(fmt.Sprintf("mainthread: a native call cannot take a %s", v.Type()))
	}
}

// int places an integer of size bytes.
func (b *builder) int(u uint64, size uintptr) {
	if b.ints < len(b.rec.x) {
		b.rec.x[b.ints] = uintptr(u)
		b.ints++
		return
	}
	b.onStack(u, size)
}

// float places a float of size bytes, whose bits u holds.
func (b *builder) float(u uint64, size uintptr) {
	if b.flts < len(b.rec.d) {
		b.rec.d[b.flts] = u
		b.flts++
		return
	}
	b.onStack(u, size)
}

// onStack places size bytes of u at the next offset of the stack
// arguments that size aligns.
func (b *builder) onStack(u uint64, size uintptr) {
	b.stack = (b.stack + size - 1) &^ (size - 1)
	b.putStack(unsafe.Pointer(&u), size)
}

func (b *builder) putStack(p unsafe.Pointer, size uintptr) {
	if b.stack+size > stackWords*8 {
		panic("mainthread: a native call takes more stack arguments than a record carries")
	}
	dst := unsafe.Add(unsafe.Pointer(&b.rec.stack[0]), b.stack)
	copy(unsafe.Slice((*byte)(dst), size), unsafe.Slice((*byte)(p), size))
	b.stack += size
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
	if kind, n := homogeneousFloats(t); kind != 0 {
		if b.flts+n <= len(b.rec.d) {
			for i := range n {
				if kind == 4 {
					b.rec.d[b.flts] = uint64(*(*uint32)(unsafe.Add(mem, 4*i)))
				} else {
					b.rec.d[b.flts] = *(*uint64)(unsafe.Add(mem, 8*i))
				}
				b.flts++
			}
			return
		}
		// A homogeneous aggregate that does not fit takes no more float
		// registers and goes on the stack.
		b.flts = len(b.rec.d)
		b.stack = (b.stack + uintptr(t.Align()) - 1) &^ (uintptr(t.Align()) - 1)
		b.putStack(mem, size)
		return
	}
	if size > 16 {
		b.keep = append(b.keep, p.Interface())
		b.int(uint64(uintptr(mem)), 8)
		return
	}
	words := int((size + 7) / 8)
	if b.ints+words <= len(b.rec.x) {
		var w [2]uint64
		copy(unsafe.Slice((*byte)(unsafe.Pointer(&w[0])), 16), unsafe.Slice((*byte)(mem), size))
		for i := range words {
			b.rec.x[b.ints] = uintptr(w[i])
			b.ints++
		}
		return
	}
	b.ints = len(b.rec.x)
	b.stack = (b.stack + 7) &^ 7
	b.putStack(mem, (size+7)&^7)
}

// homogeneousFloats reports, for a struct whose members at each depth are
// one to four floats of one size, that size in bytes and their number;
// it returns 0 for any other type.
func homogeneousFloats(t reflect.Type) (size, n int) {
	var walk func(t reflect.Type) bool
	walk = func(t reflect.Type) bool {
		switch t.Kind() {
		case reflect.Float32, reflect.Float64:
			s := int(t.Size())
			if size != 0 && size != s {
				return false
			}
			size = s
			n++
			return n <= 4
		case reflect.Struct:
			for i := range t.NumField() {
				if !walk(t.Field(i).Type) {
					return false
				}
			}
			return true
		case reflect.Array:
			for range t.Len() {
				if !walk(t.Elem()) {
					return false
				}
			}
			return true
		}
		return false
	}
	if t.Kind() != reflect.Struct || !walk(t) || n == 0 || uintptr(size*n) != t.Size() {
		return 0, 0
	}
	return size, n
}

// prepare makes the record of a call of fn with args whose result is of
// type t, and returns the values the call must keep alive and, for a
// result that goes in memory, where it goes.
func prepare(fn uintptr, t reflect.Type, args []any) (*record, []any, unsafe.Pointer) {
	rec := &record{fn: fn}
	b := builder{rec: rec}
	for _, a := range args {
		b.add(reflect.ValueOf(a))
	}
	var out unsafe.Pointer
	if t != nil && t.Kind() == reflect.Struct && t.Size() > 16 {
		if k, _ := homogeneousFloats(t); k == 0 {
			p := reflect.New(t)
			b.keep = append(b.keep, p.Interface())
			out = p.UnsafePointer()
			rec.x8 = uintptr(out)
		}
	}
	return rec, b.keep, out
}

// result reads the result of type R of a call that rec made.
func result[R any](rec *record, out unsafe.Pointer) R {
	var r R
	t := reflect.TypeFor[R]()
	p := unsafe.Pointer(&r)
	switch t.Kind() {
	case reflect.Float32:
		*(*uint32)(p) = uint32(rec.rd[0])
	case reflect.Float64:
		*(*uint64)(p) = rec.rd[0]
	case reflect.Struct:
		size := t.Size()
		if kind, n := homogeneousFloats(t); kind != 0 {
			for i := range n {
				if kind == 4 {
					*(*uint32)(unsafe.Add(p, 4*i)) = uint32(rec.rd[i])
				} else {
					*(*uint64)(unsafe.Add(p, 8*i)) = rec.rd[i]
				}
			}
			return r
		}
		if size > 16 {
			return *(*R)(out)
		}
		copy(unsafe.Slice((*byte)(p), size), unsafe.Slice((*byte)(unsafe.Pointer(&rec.r[0])), size))
	default:
		if t.Size() > 8 {
			panic(fmt.Sprintf("mainthread: a native call cannot return a %s", t))
		}
		copy(unsafe.Slice((*byte)(p), t.Size()), unsafe.Slice((*byte)(unsafe.Pointer(&rec.r[0])), t.Size()))
	}
	return r
}
