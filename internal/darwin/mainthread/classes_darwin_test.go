//go:build darwin

package mainthread

import (
	"testing"

	"github.com/ebitengine/purego/objc"
)

// Each method of a class that RegisterClass registers enters through a
// trampoline, and runs on the worker when the main thread calls it.
func TestRegisterClassSetsEachMethodToATrampoline(t *testing.T) {
	self := pthreadSelf(t)
	var ranOn uintptr
	answer := objc.RegisterName("mainthreadTestAnswer")
	c, err := RegisterClass("MainthreadTestClass", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{{
		Cmd: answer,
		Fn: func(self_ objc.ID, cmd objc.SEL) uintptr {
			ranOn = direct[uintptr](self)
			return 42
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if imp := native.methodGetImplementation(native.classGetInstanceMethod(uintptr(c), answer)); !isTrampoline(imp) {
		t.Fatalf("the implementation %#x of the method is not a trampoline", imp)
	}
	obj := objc.ID(c).Send(objc.RegisterName("new"))
	defer obj.Send(objc.RegisterName("release"))
	onFakeMain(t, func() {
		var worker, got uintptr
		RunOnWorker(func() {
			worker = direct[uintptr](self)
			got = Send[uintptr](obj, answer)
		})
		if got != 42 || ranOn != worker {
			t.Errorf("the method answered %d on %#x, want 42 on the worker %#x", got, ranOn, worker)
		}
	})
}

// A Constant method gets the native implementation of its value, not a
// trampoline, so the main thread that calls it answers on its own thread
// without the worker, and its Go function is never called.
func TestRegisterClassGivesAConstantMethodANativeImplementation(t *testing.T) {
	flipped := objc.RegisterName("mainthreadTestFlipped")
	plain := objc.RegisterName("mainthreadTestPlain")
	c, err := RegisterClass("MainthreadTestConstantClass", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: flipped, Fn: Constant{Fn: func(self objc.ID, cmd objc.SEL) bool {
			t.Error("the Go function of a Constant method ran")
			return true
		}, Value: true}},
		{Cmd: plain, Fn: Constant{Fn: func(self objc.ID, cmd objc.SEL) int {
			t.Error("the Go function of a Constant method ran")
			return 0
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for cmd, want := range map[objc.SEL]uintptr{flipped: returnOneAddress(), plain: returnZeroAddress()} {
		if imp := native.methodGetImplementation(native.classGetInstanceMethod(uintptr(c), cmd)); imp != want {
			t.Fatalf("the implementation %#x of a Constant method is not its native value %#x", imp, want)
		}
	}
	obj := objc.ID(c).Send(objc.RegisterName("new"))
	defer obj.Send(objc.RegisterName("release"))
	onFakeMain(t, func() {
		var yes bool
		var zero int
		// The main thread sends each message, as AppKit sends isFlipped.
		RunOnWorker(func() {
			yes = Send[bool](obj, flipped)
			zero = Send[int](obj, plain)
		})
		if !yes || zero != 0 {
			t.Errorf("the Constant methods answered %v and %d, want true and 0", yes, zero)
		}
	})
}

// A Constant whose function returns what returnZero and returnOne cannot
// return is refused.
func TestRegisterClassRefusesAConstantOfAFloat(t *testing.T) {
	_, err := RegisterClass("MainthreadTestFloatConstantClass", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: objc.RegisterName("mainthreadTestFloat"), Fn: Constant{Fn: func(self objc.ID, cmd objc.SEL) float64 { return 0 }}},
	})
	if err == nil {
		t.Fatal("a Constant that returns a float64 was registered")
	}
}
