//go:build darwin

package mainthread

import (
	"fmt"
	"reflect"

	"github.com/ebitengine/purego/objc"
)

// Constant is the Fn of a method that answers Value for every receiver and
// argument, whatever thread calls it, as an isFlipped that is always true.
// RegisterClass gives such a method a native implementation that returns
// Value on the calling thread, so a main thread that calls it does not
// wait for the worker for an answer that needs no Go state. Fn is the Go
// function of the method, which gives the method its type encoding and is
// not called; its result is a bool, an integer or nothing, which Value
// returns as 1 for true and 0 for false, zero or nothing.
type Constant struct {
	Fn    any
	Value bool
}

// returnZero and returnOne are C functions that return 0 and 1 in the
// result register and touch nothing else: the native implementations of
// the Constant methods.
func returnZero()
func returnOne()

// returnZeroAddress and returnOneAddress return the addresses of
// returnZero and returnOne.
func returnZeroAddress() uintptr
func returnOneAddress() uintptr

// RegisterClass registers a class as objc.RegisterClass does and sets the
// implementation of each of its methods to a trampoline of the worker
// (Method), so that a method that the main thread calls runs on the
// worker. A method whose Go function returns an objc.ID returns an object.
// A method whose Fn is a Constant gets the native implementation of its
// value instead, and runs on the thread that calls it.
func RegisterClass(name string, super objc.Class, protocols []*objc.Protocol, ivars []objc.FieldDef, methods []objc.MethodDef) (objc.Class, error) {
	if err := loadNative(); err != nil {
		return 0, err
	}
	defs := make([]objc.MethodDef, len(methods))
	for i, m := range methods {
		defs[i] = m
		if k, ok := m.Fn.(Constant); ok {
			if err := constantResult(reflect.TypeOf(k.Fn)); err != nil {
				return 0, fmt.Errorf("mainthread: class %s, selector %#x: %w", name, uintptr(m.Cmd), err)
			}
			defs[i].Fn = k.Fn
		}
	}
	c, err := objc.RegisterClass(name, super, protocols, ivars, defs)
	if err != nil {
		return 0, err
	}
	for i, m := range methods {
		meth := native.classGetInstanceMethod(uintptr(c), m.Cmd)
		if meth == 0 {
			return 0, fmt.Errorf("mainthread: class %s has no method for selector %#x", name, uintptr(m.Cmd))
		}
		if k, ok := m.Fn.(Constant); ok {
			imp := returnZeroAddress()
			if k.Value {
				imp = returnOneAddress()
			}
			native.methodSetImplementation(meth, imp)
			continue
		}
		t := reflect.TypeOf(defs[i].Fn)
		returnsObject := t.NumOut() == 1 && t.Out(0) == reflect.TypeFor[objc.ID]()
		native.methodSetImplementation(meth, method(native.methodGetImplementation(meth), returnsObject, native.selGetName(uintptr(m.Cmd))))
	}
	return c, nil
}

// constantResult refuses a Constant whose function returns what the result
// register of returnZero or returnOne cannot be: a float, a struct, an
// object or more than one value.
func constantResult(t reflect.Type) error {
	if t == nil || t.Kind() != reflect.Func {
		return fmt.Errorf("a Constant needs the Go function of its method")
	}
	if t.NumOut() == 0 {
		return nil
	}
	if t.NumOut() > 1 {
		return fmt.Errorf("a Constant returns one value at most")
	}
	switch t.Out(0).Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return nil
	}
	return fmt.Errorf("a Constant cannot return a %s", t.Out(0))
}
