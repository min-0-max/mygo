//go:build darwin

package mainthread

import (
	"fmt"
	"reflect"

	"github.com/ebitengine/purego/objc"
)

// RegisterClass registers a class as objc.RegisterClass does and sets the
// implementation of each of its methods to a trampoline of the worker
// (Method), so that a method that the main thread calls runs on the
// worker. A method whose Go function returns an objc.ID returns an object.
func RegisterClass(name string, super objc.Class, protocols []*objc.Protocol, ivars []objc.FieldDef, methods []objc.MethodDef) (objc.Class, error) {
	if err := loadNative(); err != nil {
		return 0, err
	}
	c, err := objc.RegisterClass(name, super, protocols, ivars, methods)
	if err != nil {
		return 0, err
	}
	for _, m := range methods {
		meth := native.classGetInstanceMethod(uintptr(c), m.Cmd)
		if meth == 0 {
			return 0, fmt.Errorf("mainthread: class %s has no method for selector %#x", name, uintptr(m.Cmd))
		}
		t := reflect.TypeOf(m.Fn)
		returnsObject := t.NumOut() == 1 && t.Out(0) == reflect.TypeFor[objc.ID]()
		native.methodSetImplementation(meth, Method(native.methodGetImplementation(meth), returnsObject))
	}
	return c, nil
}
